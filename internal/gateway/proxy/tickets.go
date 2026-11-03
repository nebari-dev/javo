package proxy

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/httpx"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// ticketPath is the path below an app's base where its host redeems a ticket.
const ticketPath = "_javo/ticket"

// ticketTTL is how long a ticket can be redeemed.
const ticketTTL = 60 * time.Second

// TicketRoute is where the UI host mints tickets. The route is mounted outside
// /api's JSON wrapper because a browser follows it: no session sends the
// person to sign in rather than answering 401.
const TicketRoute = "/api/auth/app-ticket"

// Tickets hands a signed-in person a single-use ticket for one app that
// is served on another host, so that host can set its own cookie.
type Tickets struct {
	Apps        *AppLookup
	Sessions    session.Store
	Exposure    exposure.Backend
	URLs        config.URLs
	Roles       config.Roles
	Now         func() time.Time
	Revalidator *authn.Revalidator
	// Secure says the session cookie is Secure, which names it.
	Secure bool
}

// ServeHTTP answers GET /api/auth/app-ticket?to=<absolute app URL>. Only an
// address that is one of our app URLs, for an app the person may open, gets a
// ticket; every other `to` is the not-found page (never a redirect).
func (m *Tickets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(model.CookieName(model.SessionCookieName, m.Secure))
	if err != nil {
		redirectToLogin(w, r)
		return
	}
	ui, err := m.Sessions.Get(r.Context(), c.Value)
	if err != nil || ui.Kind != session.KindUI {
		redirectToLogin(w, r)
		return
	}
	// The ticket carries the person's groups into a session of its own, so
	// they must be as current as the UI's own requests would make them.
	if m.Revalidator.Due(ui) {
		switch ui, _, err = m.Revalidator.Check(r.Context(), session.ID(c.Value), ui); {
		case errors.Is(err, authn.ErrSessionEnded):
			redirectToLogin(w, r)
			return
		case err != nil:
			writePage(w, http.StatusInternalServerError, page{Title: reasons.Msg(reasons.MsgServerError), Link: "/apps", LinkText: "See your apps"})
			return
		}
	}
	to, err := url.Parse(r.URL.Query().Get("to"))
	if err != nil || to.Scheme != m.URLs.URLScheme() || to.Host == "" || to.User != nil {
		notFound(w)
		return
	}
	probe := &http.Request{Host: to.Host, URL: &url.URL{Path: to.Path}}
	target, ok := m.Exposure.Match(probe)
	if !ok {
		notFound(w)
		return
	}
	app, tpl := m.Apps.load(r.Context(), target)
	if app == nil || !authz.Decide(ui.Principal, authz.RolesOf(ui.Principal, m.Roles), authz.ActOpenApp, authz.Object{App: app, Template: tpl}).Allowed {
		notFound(w)
		return
	}
	tok, err := m.Sessions.Create(r.Context(), session.Session{
		Principal: ui.Principal, Provider: ui.Provider, Kind: session.KindTicket,
		Parent: session.ID(c.Value), Workspace: app.Namespace + "/" + app.ObjectName(),
		Expires: m.Now().Add(ticketTTL),
	})
	if err != nil {
		slog.Error("mint app ticket", "err", err)
		writePage(w, http.StatusInternalServerError, page{Title: reasons.Msg(reasons.MsgServerError), Link: "/apps", LinkText: "See your apps"})
		return
	}
	base := m.Exposure.URLFor(app.Owner, app.Name, app.ObjectName())
	base.Path += ticketPath
	base.RawQuery = url.Values{"t": {tok}, "to": {to.RequestURI()}}.Encode()
	http.Redirect(w, r, base.String(), http.StatusFound)
}

// redeem runs on the apps host at <app base>/_javo/ticket: it spends the
// ticket, sets the app cookie and sends the browser on to the page it wanted.
func (p *proxy) redeem(w http.ResponseWriter, r *http.Request, t exposure.Target) {
	expired := func() {
		writePage(w, http.StatusGone, page{Title: reasons.Msg(reasons.MsgTicketExpired)})
	}
	ticket, err := p.cfg.Sessions.Redeem(r.Context(), r.URL.Query().Get("t"))
	switch {
	case errors.Is(err, session.ErrSessionNotFound), errors.Is(err, session.ErrSessionExpired):
		expired()
		return
	case err != nil:
		slog.Error("redeem app ticket", "err", err)
		badGateway(w, t.Name)
		return
	}
	if app, _ := p.apps.load(r.Context(), t); app == nil || ticket.Workspace != app.Namespace+"/"+app.ObjectName() {
		expired()
		return
	}
	tok, err := p.cfg.Sessions.Create(r.Context(), session.Session{
		Principal: ticket.Principal, Provider: ticket.Provider, Kind: session.KindApp,
		Parent: ticket.Parent, Workspace: ticket.Workspace,
	})
	if err != nil {
		slog.Error("create app session", "err", err)
		badGateway(w, t.Name)
		return
	}
	c := p.appCookie(t)
	c.Value = tok
	http.SetCookie(w, &c)
	next := r.URL.Query().Get("to")
	if !httpx.IsLocalPath(next) {
		next = p.cfg.Exposure.BasePath(t.Owner, t.Name)
	}
	http.Redirect(w, r, next, http.StatusFound)
}

// AppLookup finds the app and template a Target names.
type AppLookup struct{ DB *store.Store }

// load returns the app for t and its template. Missing or deleted apps give
// a nil app; a missing template a nil template (capped at Private by authz).
// A path address names the owner and the short name; an app's own host only
// its object name.
func (l *AppLookup) load(ctx context.Context, t exposure.Target) (*model.App, *model.Template) {
	var app model.App
	var err error
	if t.Owner != "" {
		app, err = l.DB.Apps().GetByOwnerName(ctx, t.Owner, t.Name)
	} else {
		app, err = l.DB.Apps().GetByObjectName(ctx, t.ObjectName)
	}
	if err != nil || !app.DeletedAt.IsZero() {
		return nil, nil
	}
	tpl, err := l.DB.Templates().Get(ctx, app.Template)
	if err != nil {
		return &app, nil
	}
	return &app, &tpl
}

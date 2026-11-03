// Package proxy is the app proxy: it serves each app to the people authz
// lets open it, records their activity for "Stop when idle", and mints the
// tickets that carry a session to apps on their own host.
package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/activity"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/nebi"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// Config configures New.
type Config struct {
	// Exposure says which app a request is for and whether apps have their own host.
	Exposure exposure.Backend
	// Roles bind groups to roles; URLs say where the UI host and the apps are.
	Roles config.Roles
	URLs  config.URLs
	// Store holds the apps and templates a request is decided on. Every
	// time a platform admin opens someone else's app it also gets an audit
	// row (at most one per admin and app every adminOpenEvery).
	Store    *store.Store
	Sessions session.Store
	// Revalidator keeps an app session's person as current as the UI's own
	// requests would: a group the provider dropped stops opening apps within
	// the revalidation interval. Share the one given to NewServer. It is
	// required when apps have their own host.
	Revalidator *authn.Revalidator
	// Resolve returns where and how to reach an app, given its template (nil
	// when the template is gone). Nil means the template's service address
	// (see defaultResolve).
	Resolve func(ctx context.Context, app *model.App, tpl *model.Template) (Backend, error)
	// DialWait is how long to keep retrying a connection to an app that
	// cannot be reached yet (an app can turn Ready a moment before its
	// network policy admits the gateway). Zero means 10 seconds.
	DialWait time.Duration
	// RecheckEvery is how often an open websocket is decided again, as a new
	// request would be; it is closed once the answer is no. Zero means 30
	// seconds.
	RecheckEvery time.Duration
	// Activity records every request and open websocket an app is allowed,
	// for "Stop when idle". Required: New panics without it.
	Activity *activity.Tracker
	// NebiTokens renews the owner's Nebi token of an app that gets one when
	// the owner's own session opens it; nil (Nebi off) does nothing.
	NebiTokens *nebi.Tokens
}

// Backend is where a workspace's app listens and how the proxy talks to it.
type Backend struct {
	URL *url.URL
	// Prefix is the path the app sits under, without the trailing slash:
	// "/u/<owner>/<app>" in path mode, "" when the app has its own host.
	Prefix string
	// StripPrefix removes Prefix from the path before forwarding and sends
	// it as X-Forwarded-Prefix instead (template basePath Strip). False
	// forwards the path unchanged (Prefix): the app serves under it.
	StripPrefix bool
	// Headers are set on every forwarded request, replacing any the client
	// sent with the same name.
	Headers http.Header
}

type proxy struct {
	cfg       Config
	apps      *AppLookup
	transport http.RoundTripper

	adminMu   sync.Mutex
	adminSeen map[string]time.Time
}

// adminOpenEvery bounds how often one admin opening one app is recorded: a
// page load sends a request per asset, and each would otherwise be a row.
const adminOpenEvery = 5 * time.Minute

// recordAdminOpen writes the audit row for a platform admin opening someone
// else's app, unless the same admin was recorded for it recently.
func (p *proxy) recordAdminOpen(ctx context.Context, admin string, app *model.App) {
	if p.cfg.Store == nil {
		return
	}
	key := admin + "|" + app.ID
	now := time.Now()
	p.adminMu.Lock()
	if last, ok := p.adminSeen[key]; ok && now.Sub(last) < adminOpenEvery {
		p.adminMu.Unlock()
		return
	}
	if len(p.adminSeen) >= 1024 {
		for k, t := range p.adminSeen {
			if now.Sub(t) >= adminOpenEvery {
				delete(p.adminSeen, k)
			}
		}
	}
	p.adminSeen[key] = now
	p.adminMu.Unlock()
	err := p.cfg.Store.Audit().Append(ctx, store.AuditEvent{
		Actor: admin, Via: "session", Action: "app.admin_open", ObjectKind: "app", ObjectName: app.Name,
		ObjectID: app.ID, ObjectNamespace: app.Namespace, ObjectOwner: app.Owner, Allowed: true,
	})
	if err != nil {
		slog.Warn("record admin open", "app", app.ID, "err", err)
	}
}

// New returns the handler for apps: it opens an app only when authz says
// the session may. Mount it with Server.Apps.
func New(cfg Config) http.Handler {
	if cfg.Activity == nil {
		panic("proxy.New: Config.Activity is required")
	}
	if cfg.Resolve == nil {
		cfg.Resolve = defaultResolve(cfg.Exposure)
	}
	if cfg.DialWait == 0 {
		cfg.DialWait = 10 * time.Second
	}
	if cfg.RecheckEvery == 0 {
		cfg.RecheckEvery = 30 * time.Second
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = retryDial(cfg.DialWait)
	return &proxy{cfg: cfg, apps: &AppLookup{DB: cfg.Store}, transport: t, adminSeen: map[string]time.Time{}}
}

// retryDial dials until it connects or wait has passed. Nothing has been
// sent to the app before a connection exists, so retrying is safe for any
// request.
func retryDial(wait time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		deadline := time.Now().Add(wait)
		pause := 100 * time.Millisecond
		for {
			conn, err := d.DialContext(ctx, network, addr)
			if err == nil || ctx.Err() != nil || time.Now().Add(pause).After(deadline) {
				return conn, err
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(pause):
			}
			pause = min(2*pause, time.Second)
		}
	}
}

func defaultResolve(exp exposure.Backend) func(context.Context, *model.App, *model.Template) (Backend, error) {
	return func(_ context.Context, app *model.App, t *model.Template) (Backend, error) {
		if t == nil {
			return Backend{}, fmt.Errorf("template %q is gone", app.Template)
		}
		if t.Spec.Port <= 0 {
			return Backend{}, fmt.Errorf("template %q has no port", t.Name)
		}
		base := exp.BasePath(app.Owner, app.Name)
		rendered, err := workspace.ProxyHeadersOf(t, base)
		if err != nil {
			return Backend{}, err
		}
		hdr := http.Header{}
		for k, v := range rendered {
			hdr.Set(k, v)
		}
		return Backend{
			URL:         &url.URL{Scheme: "http", Host: net.JoinHostPort(fmt.Sprintf("%s.%s.svc", app.ObjectName(), app.Namespace), fmt.Sprint(workspace.AppPortOf(app, t)))},
			Prefix:      strings.TrimSuffix(base, "/"),
			StripPrefix: t.Spec.BasePath == "Strip",
			Headers:     hdr,
		}, nil
	}
}

func notFound(w http.ResponseWriter) {
	writePage(w, http.StatusNotFound, page{Title: reasons.Msg(reasons.MsgAppNotFound), Link: "/apps", LinkText: "See your apps"})
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t, ok := p.cfg.Exposure.Match(r)
	if !ok {
		notFound(w)
		return
	}
	if p.appsShareOrigin() && fromAnotherApp(r, p.cfg.Exposure.BasePath(t.Owner, t.Name)) {
		refuseFromAnotherApp(w, r)
		return
	}
	if p.cfg.Exposure.SeparateHost() && t.Rest == ticketPath {
		p.redeem(w, r, t)
		return
	}

	// Every request is decided afresh from the session, the app and its
	// template as they are now, so a revoked share or ended session stops the
	// very next request. An open websocket is decided again every
	// RecheckEvery (see activityWriter).
	a := p.access(r, t)
	app, tpl, who, signedIn, d := a.app, a.tpl, a.who, a.signedIn, a.decision
	if !d.Allowed {
		// Missing, private and not-shared look the same to someone who is not
		// signed in: they are sent to sign in. A Public app that is capped
		// below Public gets the plain not-found page.
		if !signedIn && (app == nil || app.Sharing.Access != model.AccessPublic) {
			p.redirectToSignIn(w, r)
			return
		}
		notFound(w)
		return
	}
	if a.refused {
		writePage(w, http.StatusForbidden, page{Title: reasons.Msg(a.refusal), Link: "/apps", LinkText: "See your apps"})
		return
	}
	if d.Via == "platform-admin" {
		p.recordAdminOpen(r.Context(), who.ID, app)
	}
	if signedIn {
		p.cfg.NebiTokens.Touch(r.Context(), app, tpl, who.ID, p.idToken(a.uiID))
	}
	backend, err := p.cfg.Resolve(r.Context(), app, tpl)
	if err != nil {
		badGateway(w, t.Name)
		return
	}

	p.cfg.Activity.Touch(app.ID)
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(backend.URL)
			pr.Out.URL.Path, pr.Out.URL.RawPath = pr.In.URL.Path, pr.In.URL.RawPath
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
			// This hop is plain http behind a TLS proxy; apps build links and
			// redirects from these, so they get the configured public scheme
			// and no port claimed by whoever is in front.
			pr.Out.Header.Set("X-Forwarded-Proto", p.cfg.URLs.URLScheme())
			pr.Out.Header.Del("X-Forwarded-Port")
			stripSessionCookie(pr.Out)
			stripAPIToken(pr.Out)
			if !signedIn {
				// Nobody is signed in: the app gets nothing that says who is asking.
				pr.Out.Header.Del("Cookie")
			}
			// Only the gateway says what prefix the app sits under.
			pr.Out.Header.Del("X-Forwarded-Prefix")
			if backend.StripPrefix && backend.Prefix != "" {
				pr.Out.URL.Path = stripPrefix(pr.Out.URL.Path, backend.Prefix)
				if pr.Out.URL.RawPath != "" {
					pr.Out.URL.RawPath = stripPrefix(pr.Out.URL.RawPath, backend.Prefix)
				}
				pr.Out.Header.Set("X-Forwarded-Prefix", backend.Prefix)
			}
			for k, v := range backend.Headers {
				pr.Out.Header[k] = slices.Clone(v)
			}
		},
		Transport:      p.transport,
		ModifyResponse: sanitizeResponse,
		ErrorHandler:   func(w http.ResponseWriter, _ *http.Request, _ error) { badGateway(w, t.Name) },
	}
	aw := &activityWriter{ResponseWriter: w, activity: p.cfg.Activity, app: app.ID,
		recheckEvery: p.cfg.RecheckEvery, allowed: func() bool {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), recheckTimeout)
			defer cancel()
			return p.access(r.WithContext(ctx), t).opens()
		}}
	if tpl != nil && tpl.Spec.Activity == model.ActivityJupyter {
		aw.probe = jupyterProbe(&http.Client{Transport: p.transport}, backend, time.Now)
	}
	rp.ServeHTTP(aw, r)
}

// access is what ServeHTTP decides a request on: the app, the person and
// whether they may open it.
type access struct {
	app      *model.App
	tpl      *model.Template
	who      authz.Principal
	uiID     string
	signedIn bool
	decision authz.Decision
	// refusal is set (refused true) when authz allows the open but the
	// apps' shared origin does not (sharedOriginRefusal).
	refusal reasons.MessageCode
	refused bool
}

// opens is true when the request may reach the app.
func (a access) opens() bool { return a.decision.Allowed && !a.refused }

// access decides r for the app t names, from the session, the app and its
// template as they are now.
func (p *proxy) access(r *http.Request, t exposure.Target) access {
	var a access
	a.app, a.tpl = p.apps.load(r.Context(), t)
	var sess session.Session
	sess, a.uiID, a.signedIn = p.session(r, a.app)
	a.who = authz.Principal{Anonymous: true}
	if a.signedIn {
		a.who = sess.Principal
	}
	if a.app == nil {
		return a
	}
	roles := authz.RolesOf(a.who, p.cfg.Roles)
	a.decision = authz.Decide(a.who, roles, authz.ActOpenApp, authz.Object{App: a.app, Template: a.tpl})
	if a.decision.Allowed {
		a.refusal, a.refused = p.sharedOriginRefusal(a.app, a.decision, roles)
	}
	return a
}

// sharedOriginRefusal refuses what must not open while an app shares its
// origin with the UI (one host) or with the other apps (path mode with an apps
// host). There a page of one app can act as whoever views it: on one host it
// calls the API with their session, on an apps host it scripts their other
// apps. So a viewer holding any admin role (roles) opens only their own apps,
// since someone else's page would act with that role, and nobody opens a
// preview of a pull request, which runs code the owner has not necessarily
// read (a stranger's, for a fork).
func (p *proxy) sharedOriginRefusal(app *model.App, d authz.Decision, roles []authz.Role) (reasons.MessageCode, bool) {
	if !p.appsShareOrigin() {
		return "", false
	}
	switch {
	case d.Via != "owner" && authz.Elevated(roles):
		return reasons.MsgAdminOpenNeedsAppsHost, true
	case app.ParentID != "":
		return reasons.MsgPreviewNeedsAppsHost, true
	}
	return "", false
}

// appsShareOrigin is true unless every app has an origin of its own: apps on
// the UI host share the UI's origin, and apps served under a path of one apps
// host share that host's origin with each other.
func (p *proxy) appsShareOrigin() bool {
	return !p.cfg.Exposure.SeparateHost() || p.cfg.Exposure.BasePath("o", "n") != "/"
}

// session returns the live session the request carries for this app: the app
// cookie when apps are on their own host (it must be minted for this very
// app), the UI cookie when they share the UI's origin. uiID is the ID of the
// ui session behind it.
func (p *proxy) session(r *http.Request, app *model.App) (sess session.Session, uiID string, ok bool) {
	name, kind := model.SessionCookieName, session.KindUI
	if p.cfg.Exposure.SeparateHost() {
		name, kind = model.AppCookieName, session.KindApp
	}
	c, err := r.Cookie(name)
	if err != nil {
		return session.Session{}, "", false
	}
	sess, err = p.cfg.Sessions.Get(r.Context(), c.Value)
	if err != nil {
		if !errors.Is(err, session.ErrSessionNotFound) && !errors.Is(err, session.ErrSessionExpired) {
			slog.Error("session lookup", "err", err)
		}
		return session.Session{}, "", false
	}
	if sess.Kind != kind || kind == session.KindApp && (app == nil || sess.Workspace != app.Namespace+"/"+app.ObjectName()) {
		return session.Session{}, "", false
	}
	if kind == session.KindApp {
		sess, ok = p.revalidateParent(r.Context(), sess)
		return sess, sess.Parent, ok
	}
	return sess, session.ID(c.Value), true
}

// idToken returns the ID token of the ui session uiID, renewed first when it
// is about to expire (when the proxy has a Revalidator).
func (p *proxy) idToken(uiID string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		sess, err := p.cfg.Sessions.GetByID(ctx, uiID)
		if err != nil || sess.IDToken == "" {
			return "", err
		}
		if p.cfg.Revalidator != nil {
			sess, err = p.cfg.Revalidator.FreshIDToken(ctx, uiID, sess)
		}
		return sess.IDToken, err
	}
}

// revalidateParent makes sure the ui session an app session was minted from
// has been revalidated when due, since the app tab may be the only thing the
// person uses and nothing else would ask the provider. It returns the app
// session with the person as the provider now describes them, or false when
// the person is gone.
func (p *proxy) revalidateParent(ctx context.Context, sess session.Session) (session.Session, bool) {
	if p.cfg.Revalidator == nil { // misconfigured: refuse rather than skip the check
		slog.Error("app proxy has no Revalidator")
		return session.Session{}, false
	}
	parent, err := p.cfg.Sessions.GetByID(ctx, sess.Parent)
	if err != nil {
		return session.Session{}, false
	}
	if p.cfg.Revalidator.Due(parent) {
		if parent, _, err = p.cfg.Revalidator.Check(ctx, sess.Parent, parent); err != nil {
			if !errors.Is(err, authn.ErrSessionEnded) {
				slog.Error("revalidate parent session", "err", err)
			}
			return session.Session{}, false
		}
	}
	sess.Principal = parent.Principal
	return sess, true
}

// redirectToSignIn sends a browser that has no usable session to sign in: the
// UI's ticket route when apps have their own host, the login page otherwise.
func (p *proxy) redirectToSignIn(w http.ResponseWriter, r *http.Request) {
	if !p.cfg.Exposure.SeparateHost() {
		redirectToLogin(w, r)
		return
	}
	if p.cfg.URLs.UIHost == "" {
		notFound(w)
		return
	}
	here := url.URL{Scheme: p.cfg.URLs.URLScheme(), Host: r.Host, Path: r.URL.Path, RawQuery: r.URL.RawQuery}
	ticket := url.URL{Scheme: p.cfg.URLs.URLScheme(), Host: p.cfg.URLs.UIHost, Path: TicketRoute,
		RawQuery: url.Values{"to": {here.String()}}.Encode()}
	http.Redirect(w, r, ticket.String(), http.StatusFound)
}

// stripPrefix removes prefix from p; what is left always starts with "/".
func stripPrefix(p, prefix string) string {
	return "/" + strings.TrimPrefix(strings.TrimPrefix(p, prefix), "/")
}

func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
}

// isGatewayCookie is true for the cookies the gateway itself owns.
func isGatewayCookie(name string) bool {
	return name == model.SessionCookieName || name == model.AppCookieName
}

// stripSessionCookie removes the gateway session pair from the Cookie header
// so apps never see it; every other pair is passed on unchanged.
func stripSessionCookie(r *http.Request) {
	var kept []string
	for _, line := range r.Header.Values("Cookie") {
		for _, pair := range strings.Split(line, ";") {
			name, _, _ := strings.Cut(strings.TrimSpace(pair), "=")
			if !isGatewayCookie(strings.TrimSpace(name)) && strings.TrimSpace(pair) != "" {
				kept = append(kept, strings.TrimSpace(pair))
			}
		}
	}
	r.Header.Del("Cookie")
	if len(kept) > 0 {
		r.Header.Set("Cookie", strings.Join(kept, "; "))
	}
}

// stripAPIToken removes an Authorization header that carries a personal API
// token: tokens never open apps, and an app must not receive its visitor's
// token. Any other Authorization (an app's own scheme) is passed on.
func stripAPIToken(r *http.Request) {
	if t, ok := authn.BearerToken(r); ok && strings.HasPrefix(t, token.Prefix) {
		r.Header.Del("Authorization")
	}
}

// sanitizeResponse stops an app from replacing the gateway session, claiming
// the whole origin for a service worker, or wiping the browser's site data.
func sanitizeResponse(resp *http.Response) error {
	resp.Header.Del("Service-Worker-Allowed")
	resp.Header.Del("Clear-Site-Data")
	var kept []string
	for _, line := range resp.Header.Values("Set-Cookie") {
		name, _, _ := strings.Cut(strings.TrimSpace(line), "=")
		if !isGatewayCookie(strings.TrimSpace(name)) {
			kept = append(kept, line)
		}
	}
	resp.Header.Del("Set-Cookie")
	for _, l := range kept {
		resp.Header.Add("Set-Cookie", l)
	}
	return nil
}

// page is a small branded HTML answer for browser requests under /u/.
type page struct {
	Title, What, Todo, Link, LinkText string
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<link rel="icon" type="image/svg+xml" href="/favicon.svg">
<link rel="icon" href="/favicon.ico" sizes="32x32">
<link rel="icon" href="/favicon.png" type="image/png" sizes="180x180">
<link rel="apple-touch-icon" href="/favicon.png">
<style>:root{--font-sans:"Fira Sans",system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
body{font-family:var(--font-sans);max-width:32rem;margin:15vh auto;padding:0 1rem;color:#1a1a1a}
a{color:inherit}@media(prefers-color-scheme:dark){body{background:#111;color:#eee}}</style></head>
<body><h1>{{.Title}}</h1>
{{with .What}}<p>{{.}}</p>{{end}}{{with .Todo}}<p>{{.}}</p>{{end}}
<p><a href="{{.Link}}">{{.LinkText}}</a></p></body></html>`))

func writePage(w http.ResponseWriter, status int, p page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = pageTmpl.Execute(w, p)
}

func badGateway(w http.ResponseWriter, workspace string) {
	r, _ := reasons.Lookup(reasons.AppUnreachable)
	writePage(w, http.StatusBadGateway, page{Title: r.Title, What: r.What, Todo: r.Todo,
		Link: "/apps/" + url.PathEscape(workspace), LinkText: "View app status"})
}

// activityWriter wraps the ResponseWriter so that an upgraded (websocket)
// connection counts as use for as long as it is open, or, with a probe, has
// the app asked about its use for as long as it is open. Every recheckEvery
// it asks allowed whether the request would still open the app, and closes
// the connection when not.
type activityWriter struct {
	http.ResponseWriter
	activity     *activity.Tracker
	app          string
	probe        activity.Probe
	recheckEvery time.Duration
	allowed      func() bool
}

// recheckTimeout bounds one recheck of an open websocket: a check that cannot
// finish in time (the database is unreachable) counts as a no.
const recheckTimeout = 10 * time.Second

func (a *activityWriter) Unwrap() http.ResponseWriter { return a.ResponseWriter }

func (a *activityWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(a.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	c := &activityConn{Conn: conn, done: make(chan struct{})}
	if a.probe != nil {
		c.closed = a.activity.Watch(a.app, a.probe)
	} else {
		c.closed = a.activity.Open(a.app)
	}
	go c.recheck(a.recheckEvery, a.allowed)
	return c, rw, nil
}

// activityConn ends its use when it is closed (the reverse proxy closes both
// sides when either ends).
type activityConn struct {
	net.Conn
	closed func()
	done   chan struct{}
	once   sync.Once
}

func (c *activityConn) Close() error {
	c.once.Do(func() { close(c.done) })
	c.closed()
	return c.Conn.Close()
}

// recheck closes the connection the first time allowed says no, asking every
// interval until the connection is closed. Closing it ends the reverse
// proxy's copy, which then closes the app's side too.
func (c *activityConn) recheck(every time.Duration, allowed func() bool) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-tick.C:
			if !allowed() {
				_ = c.Close()
				return
			}
		}
	}
}

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
	"path"
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
	// Sharing is the platform's sharing settings: on one host a share opens
	// only with allowOneHost.
	Sharing config.SharingCfg
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
	// RecheckEvery is how often a request still open (a websocket,
	// server-sent events, a long download) is decided again, as a new request
	// would be; it is ended once the answer is no. Zero means 30 seconds.
	RecheckEvery time.Duration
	// Activity records every request and open websocket an app is allowed,
	// for "Stop when idle". Required: New panics without it.
	Activity *activity.Tracker
	// NebiTokens renews the owner's Nebi token of an app that gets one when
	// the owner's own session opens it; nil (Nebi off) does nothing.
	NebiTokens *nebi.Tokens
	// SecureCookies is the gateway's (see config.URLs.SecureCookies): it names
	// the UI session cookie apps read when they share the UI's origin.
	SecureCookies bool
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
	if p.startedByAnotherApp(r, t) || foreignOrigin(r) {
		refuseFromAnotherApp(w, r)
		return
	}
	if p.cfg.Exposure.SeparateHost() && t.Rest == ticketPath {
		p.redeem(w, r, t)
		return
	}

	// Every request is decided afresh from the session, the app and its
	// template as they are now, so a revoked share or ended session stops the
	// very next request. A request still open (a websocket, server-sent
	// events, a download) is decided again every RecheckEvery (see recheck).
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
		// A platform admin, who sees the app in All apps, is told why.
		if d.Code == reasons.MsgOwnerOnlyOpens {
			writePage(w, http.StatusForbidden, page{Title: reasons.Msg(d.Code), Link: "/apps", LinkText: "See your apps"})
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
	if app.Paused {
		// Stopped by its owner: nothing listens, so do not wait to dial.
		badGateway(w, t.Name)
		return
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
			// Only the gateway says what prefix the app sits under, and
			// nobody but the front proxy says who the client is or what
			// address they asked for.
			pr.Out.Header.Del("X-Forwarded-Prefix")
			for _, h := range clientClaimHeaders {
				pr.Out.Header.Del(h)
			}
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
		Transport: p.transport,
		ModifyResponse: func(resp *http.Response) error {
			return sanitizeResponse(resp, p.cfg.Exposure.BasePath(t.Owner, t.Name))
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) { badGateway(w, t.Name) },
	}
	aw := &activityWriter{ResponseWriter: w, activity: p.cfg.Activity, app: app.ID}
	if tpl != nil && tpl.Spec.Activity == model.ActivityJupyter {
		aw.probe = jupyterProbe(&http.Client{Transport: p.transport}, backend, time.Now)
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go recheck(ctx, p.cfg.RecheckEvery, cancel, func() bool {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), recheckTimeout)
		defer cancel()
		return p.access(r.WithContext(ctx), t).opens()
	})
	rp.ServeHTTP(aw, r.WithContext(ctx))
}

// recheck asks allowed every interval, for as long as ctx lives, whether the
// request would still open the app, and calls end the first time it says no.
// Ending the request's context stops whatever is still open: a websocket (the
// reverse proxy closes both sides), server-sent events or a long download.
func recheck(ctx context.Context, every time.Duration, end context.CancelFunc, allowed func() bool) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if !allowed() {
				end()
				return
			}
		}
	}
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
	// refusal is set (refused true) when authz allows the open but where
	// apps are served does not (authz.OpenOn).
	refusal reasons.MessageCode
	refused bool
}

// opens is true when the request may reach the app.
func (a access) opens() bool { return a.decision.Allowed && !a.refused }

// access decides r for the app t names, from the session, the app and its
// template as they are now.
func (p *proxy) access(r *http.Request, t exposure.Target) access {
	var a access
	obj := p.apps.object(r.Context(), t)
	a.app, a.tpl = obj.App, obj.Template
	var sess session.Session
	sess, a.uiID, a.signedIn = p.session(r, t, a.app)
	a.who = authz.Principal{Anonymous: true}
	if a.signedIn {
		a.who = sess.Principal
	}
	if a.app == nil {
		return a
	}
	a.decision, a.refusal = authz.OpenOn(a.who, authz.RolesOf(a.who, p.cfg.Roles), obj, authz.OriginOf(p.cfg.Exposure, p.cfg.Sharing.AllowOneHost))
	a.refused = a.refusal != ""
	return a
}

// startedByAnotherApp is true when a page of another app could have sent r:
// fromAnotherApp while apps share an origin, fromSiblingApp in subdomain mode.
func (p *proxy) startedByAnotherApp(r *http.Request, t exposure.Target) bool {
	if p.appsShareOrigin() {
		return fromAnotherApp(r, p.cfg.Exposure.BasePath(t.Owner, t.Name))
	}
	return fromSiblingApp(r)
}

// appsShareOrigin is true unless every app has an origin of its own: apps on
// the UI host share the UI's origin, and apps served under a path of one apps
// host share that host's origin with each other.
func (p *proxy) appsShareOrigin() bool {
	return exposure.SharesOrigin(p.cfg.Exposure)
}

// session returns the live session the request carries for this app: the app
// cookie when apps are on their own host (it must be minted for this very
// app), the UI cookie when they share the UI's origin. uiID is the ID of the
// ui session behind it.
func (p *proxy) session(r *http.Request, t exposure.Target, app *model.App) (sess session.Session, uiID string, ok bool) {
	name, kind := model.CookieName(model.SessionCookieName, p.cfg.URLs.SecureCookies(p.cfg.SecureCookies)), session.KindUI
	if p.cfg.Exposure.SeparateHost() {
		name, kind = p.appCookie(t).Name, session.KindApp
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
	id := session.ID(c.Value)
	sess, ok = p.revalidateUI(r.Context(), id, sess)
	return sess, id, ok
}

// revalidateUI asks the provider about the ui session sess, whose ID is id,
// when it is due, as the API does: on one host an app tab may be the only
// thing the person uses. It returns the session as the provider now
// describes the person, or false when they are gone.
func (p *proxy) revalidateUI(ctx context.Context, id string, sess session.Session) (session.Session, bool) {
	if p.cfg.Revalidator == nil { // misconfigured: refuse rather than skip the check
		slog.Error("app proxy has no Revalidator")
		return session.Session{}, false
	}
	if !p.cfg.Revalidator.Due(sess) {
		return sess, true
	}
	fresh, _, err := p.cfg.Revalidator.Check(ctx, id, sess)
	if err != nil {
		if !errors.Is(err, authn.ErrSessionEnded) {
			slog.Error("revalidate session", "err", err)
		}
		return session.Session{}, false
	}
	return fresh, true
}

// appCookie is the app cookie for t without its value. A Secure cookie that
// covers the whole of one host gets the __Host- prefix.
func (p *proxy) appCookie(t exposure.Target) http.Cookie {
	domain, path := p.cfg.Exposure.CookieScope(t)
	secure := p.cfg.URLs.URLScheme() == "https"
	return http.Cookie{
		Name: model.CookieName(model.AppCookieName, secure && domain == "" && path == "/"), Domain: domain, Path: path,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
	}
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
	parent, err := p.cfg.Sessions.GetByID(ctx, sess.Parent)
	if err != nil {
		return session.Session{}, false
	}
	if parent, ok := p.revalidateUI(ctx, sess.Parent, parent); ok {
		sess.Principal = parent.Principal
		return sess, true
	}
	return session.Session{}, false
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
	t, _ := p.cfg.Exposure.Match(r)
	nonce, err := newTicketNonce()
	if err != nil {
		slog.Error("ticket nonce", "err", err)
		badGateway(w, t.Name)
		return
	}
	c := p.nonceCookie(t)
	c.Value = nonce
	http.SetCookie(w, &c)
	here := url.URL{Scheme: p.cfg.URLs.URLScheme(), Host: r.Host, Path: r.URL.Path, RawQuery: r.URL.RawQuery}
	ticket := url.URL{Scheme: p.cfg.URLs.URLScheme(), Host: p.cfg.URLs.UIHost, Path: TicketRoute,
		RawQuery: url.Values{"to": {here.String()}, ticketNonceParam: {nonce}}.Encode()}
	http.Redirect(w, r, ticket.String(), http.StatusFound)
}

// stripPrefix removes prefix from p; what is left always starts with "/".
func stripPrefix(p, prefix string) string {
	return "/" + strings.TrimPrefix(strings.TrimPrefix(p, prefix), "/")
}

func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
}

// isGatewayCookie is true for the cookies the gateway itself owns, under
// either name each can have.
func isGatewayCookie(name string) bool {
	for _, secure := range []bool{false, true} {
		if name == model.CookieName(model.SessionCookieName, secure) || name == model.CookieName(model.AppCookieName, secure) ||
			name == model.CookieName(ticketNonceCookie, secure) ||
			strings.HasPrefix(name, model.CookieName(model.OIDCFlowCookiePrefix, secure)) {
			return true
		}
	}
	return false
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
// Every Authorization value that names one is removed, whatever its scheme
// or spacing, so a second header or a tab cannot carry one past.
func stripAPIToken(r *http.Request) {
	var kept []string
	for _, v := range r.Header.Values("Authorization") {
		if !strings.Contains(v, token.Prefix) {
			kept = append(kept, v)
		}
	}
	r.Header.Del("Authorization")
	for _, v := range kept {
		r.Header.Add("Authorization", v)
	}
}

// clientClaimHeaders are request headers some proxies and frameworks read as
// the client's address, scheme or original URL. ReverseProxy's Rewrite
// already drops X-Forwarded-* and Forwarded; these come only from the client
// here, so they are dropped too.
var clientClaimHeaders = []string{
	"Forwarded", "X-Real-Ip", "X-Client-Ip", "True-Client-Ip", "X-Scheme", "X-Forwarded-Scheme", "X-Forwarded-Ssl",
	"X-Original-Url", "X-Rewrite-Url", "X-Original-Host",
}

// foreignOrigin is true for a websocket handshake or a request with an
// unsafe method whose Origin is not the request's own host (or is "null", an
// opaque origin). Browsers send Origin on both even where they send no
// Fetch Metadata, so this holds for them too.
func foreignOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	upgrade := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		if !upgrade {
			return false
		}
	}
	u, err := url.Parse(origin)
	return err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host)
}

// sanitizeResponse stops an app from replacing the gateway session, setting
// cookies outside its own scope (scopeCookie; base is its BasePath), claiming
// the whole origin for a service worker, or wiping the browser's site data.
func sanitizeResponse(resp *http.Response, base string) error {
	resp.Header.Del("Service-Worker-Allowed")
	resp.Header.Del("Clear-Site-Data")
	// The host's HTTPS pinning is the gateway's: an app could clear it
	// (max-age=0) or pin sibling hosts (includeSubDomains).
	resp.Header.Del("Strict-Transport-Security")
	var kept []string
	for _, line := range resp.Header.Values("Set-Cookie") {
		if l, ok := scopeCookie(line, base); ok {
			kept = append(kept, l)
		}
	}
	if !setsFrameAncestors(resp.Header) {
		// Framing by the app's own origin only, unless the app chose.
		resp.Header.Add("Content-Security-Policy", "frame-ancestors 'self'")
	}
	resp.Header.Del("Set-Cookie")
	for _, l := range kept {
		resp.Header.Add("Set-Cookie", l)
	}
	return nil
}

// scopeCookie keeps a Set-Cookie line of an app within the app: with no
// Domain attribute (host-only), and, when the app is served under a path
// (base is not "/"), with a Path inside base, else base itself; a cookie
// with no Path would get the directory of the request, which can be above
// the app. It refuses (ok false) a cookie with no name (sent back as its
// value alone, so "=javo_session=x" would come back as the gateway's
// cookie), one named like the gateway's, and a __Host- cookie under a path,
// which needs Path=/ and so cannot be scoped to the app.
func scopeCookie(line, base string) (string, bool) {
	parts := strings.Split(line, ";")
	name, _, _ := strings.Cut(parts[0], "=")
	name = strings.TrimSpace(name)
	if name == "" || gatewayLike(name) {
		return "", false
	}
	scoped := base != "/"
	if scoped && hasPrefixFold(name, "__Host-") {
		return "", false
	}
	app := strings.TrimSuffix(base, "/")
	out := []string{strings.TrimSpace(parts[0])}
	for _, attr := range parts[1:] {
		attr = strings.TrimSpace(attr)
		k, v, _ := strings.Cut(attr, "=")
		switch k = strings.TrimSpace(k); {
		case attr == "":
		case strings.EqualFold(k, "Domain"):
		case strings.EqualFold(k, "Path") && scoped:
			if v = strings.TrimSpace(v); v == app || strings.HasPrefix(v, base) && path.Clean(v) == v {
				app = v
			}
		default:
			out = append(out, attr)
		}
	}
	if scoped {
		out = append(out, "Path="+app)
	}
	return strings.Join(out, "; "), true
}

// gatewayLike is true for a cookie name the gateway uses or might: javo_...,
// in any case and under either cookie prefix.
func gatewayLike(name string) bool {
	for _, p := range []string{"__Host-", "__Secure-"} {
		if hasPrefixFold(name, p) {
			name = name[len(p):]
			break
		}
	}
	return isGatewayCookie(name) || hasPrefixFold(name, model.ProductName+"_")
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// setsFrameAncestors is true when h already says who may frame the page.
func setsFrameAncestors(h http.Header) bool {
	if h.Get("X-Frame-Options") != "" {
		return true
	}
	for _, v := range h.Values("Content-Security-Policy") {
		if strings.Contains(strings.ToLower(v), "frame-ancestors") {
			return true
		}
	}
	return false
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

// pageCSP lets a page show its inline style and the favicon, and nothing else.
const pageCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"

func writePage(w http.ResponseWriter, status int, p page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", pageCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
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
// the app asked about its use for as long as it is open.
type activityWriter struct {
	http.ResponseWriter
	activity *activity.Tracker
	app      string
	probe    activity.Probe
}

// recheckTimeout bounds one recheck of an open request: a check that cannot
// finish in time (the database is unreachable) counts as a no.
const recheckTimeout = 10 * time.Second

func (a *activityWriter) Unwrap() http.ResponseWriter { return a.ResponseWriter }

// WriteHeader drops the app's informational answers (103 Early Hints and
// the like): ReverseProxy sends their headers straight on, past
// sanitizeResponse. 101, the websocket upgrade, goes through.
func (a *activityWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		return
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a *activityWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(a.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	c := &activityConn{Conn: conn}
	if a.probe != nil {
		c.closed = a.activity.Watch(a.app, a.probe)
	} else {
		c.closed = a.activity.Open(a.app)
	}
	return c, rw, nil
}

// activityConn ends its use when it is closed (the reverse proxy closes both
// sides when either ends).
type activityConn struct {
	net.Conn
	closed func()
}

func (c *activityConn) Close() error {
	c.closed()
	return c.Conn.Close()
}

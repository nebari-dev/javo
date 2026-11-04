// Package authoidc is the auth provider for OpenID Connect: authorization
// code flow with PKCE, refresh-token revalidation and RP-initiated logout.
// Discovery, ID token verification, the code flow and refresh are
// github.com/coreos/go-oidc/v3 and golang.org/x/oauth2; nothing here parses
// a JWT.
package authoidc

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/httpx"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// ID is the provider's registry key and Session.Provider value.
const ID = "oidc"

const (
	startPath    = "/auth/oidc/start"
	callbackPath = "/auth/oidc/callback"
	flowPrefix   = model.OIDCFlowCookiePrefix
	flowMaxAge   = 10 * time.Minute
	// maxIATAhead is how far ahead of this server's clock a token may be
	// issued before the clocks are called out of step.
	maxIATAhead    = 5 * time.Minute
	defaultTimeout = 10 * time.Second
)

// discovered is what discovery produced; it is nil until discovery succeeds.
type discovered struct {
	provider   *oidc.Provider
	verifier   *oidc.IDTokenVerifier
	endSession string
}

// Provider signs people in through one OpenID Connect provider.
type Provider struct {
	cfg    config.OIDC
	urls   config.URLs
	client *http.Client

	retryMin, retryMax time.Duration
	ctx                context.Context
	cancel             context.CancelFunc
	ready              chan struct{}

	mu            sync.RWMutex
	host          authprovider.Mounter
	disc          *discovered
	problem       reasons.Code
	problemDetail string
}

var (
	_ authprovider.Provider         = (*Provider)(nil)
	_ authprovider.LogoutRedirector = (*Provider)(nil)
)

// Option adjusts a Provider.
type Option func(*Provider)

// WithRetryBackoff sets the delay between discovery attempts: it starts at
// initial and doubles up to max (default 1 second and 1 minute).
func WithRetryBackoff(initial, max time.Duration) Option {
	return func(p *Provider) { p.retryMin, p.retryMax = initial, max }
}

// New returns the provider and starts discovery in the background, so the
// gateway starts and serves other sign-in methods while the identity
// provider is down. hc may be nil. cfg.Dial redirects connections for the
// listed host:port pairs; the issuer string never changes. Close stops the
// background retry.
func New(cfg config.OIDC, urls config.URLs, hc *http.Client, opts ...Option) *Provider {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Provider{
		cfg: cfg, urls: urls, client: buildClient(hc, cfg.Dial),
		retryMin: time.Second, retryMax: time.Minute,
		ctx: ctx, cancel: cancel, ready: make(chan struct{}),
		problem: reasons.IdpUnreachable,
	}
	for _, o := range opts {
		o(p)
	}
	go p.discover()
	return p
}

// buildClient copies hc (or starts a new client), gives it a timeout, and
// applies the Dial overrides to its dialer.
func buildClient(hc *http.Client, dial map[string]string) *http.Client {
	c := &http.Client{}
	if hc != nil {
		cp := *hc
		c = &cp
	}
	if c.Timeout == 0 {
		c.Timeout = defaultTimeout
	}
	if len(dial) == 0 {
		return c
	}
	var tr *http.Transport
	switch t := c.Transport.(type) {
	case nil:
		tr = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		tr = t.Clone()
	default:
		slog.Warn("oidc dial overrides ignored: the http client has a custom transport")
		return c
	}
	d := &net.Dialer{Timeout: 5 * time.Second}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if to, ok := dial[addr]; ok {
			addr = to
		}
		return d.DialContext(ctx, network, addr)
	}
	c.Transport = tr
	return c
}

// Close stops background discovery.
func (p *Provider) Close() { p.cancel() }

// Ready is closed once discovery has succeeded.
func (p *Provider) Ready() <-chan struct{} { return p.ready }

// Problem says why sign-in is unavailable: a catalog code and an operator
// detail. It is meaningless once Ready is closed.
func (p *Provider) Problem() (reasons.Code, string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.problem, p.problemDetail
}

func (p *Provider) discovery() *discovered {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.disc
}

func (p *Provider) now() time.Time {
	p.mu.RLock()
	h := p.host
	p.mu.RUnlock()
	if h != nil {
		return h.Now()
	}
	return time.Now()
}

// discover retries until the provider's discovery document is read, with
// exponential backoff.
func (p *Provider) discover() {
	delay := p.retryMin
	for {
		prov, err := oidc.NewProvider(oidc.ClientContext(p.ctx, p.client), p.cfg.Issuer)
		if err == nil {
			p.mu.Lock()
			p.disc = p.build(prov)
			p.mu.Unlock()
			slog.Info("oidc discovery succeeded", "issuer", p.cfg.Issuer)
			close(p.ready)
			return
		}
		if p.ctx.Err() != nil { // closed
			return
		}
		code, detail := classify(p.cfg.Issuer, err)
		p.mu.Lock()
		p.problem, p.problemDetail = code, detail
		p.mu.Unlock()
		slog.Warn("oidc discovery failed", "reason", code, "detail", detail, "retry_in", delay)
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, p.retryMax)
	}
}

func (p *Provider) build(prov *oidc.Provider) *discovered {
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
	}
	if err := prov.Claims(&meta); err != nil {
		slog.Warn("oidc discovery document unreadable for end_session_endpoint", "err", err)
	}
	return &discovered{
		provider:   prov,
		verifier:   prov.Verifier(&oidc.Config{ClientID: p.cfg.ClientID, Now: p.now}),
		endSession: meta.EndSession,
	}
}

// classify turns a discovery error into a catalog reason and an operator
// detail that names the exact strings involved.
func classify(issuer string, err error) (reasons.Code, string) {
	var mm *oidc.IssuerMismatchError
	if errors.As(err, &mm) {
		return reasons.IssuerMismatch, fmt.Sprintf("configured issuer %q, but the provider reports %q", mm.Provided, mm.Discovered)
	}
	return reasons.IdpUnreachable, fmt.Sprintf("discovery for issuer %q failed: %v", issuer, err)
}

func (p *Provider) ID() string { return ID }

func (p *Provider) Option() authprovider.LoginOption {
	label := p.cfg.DisplayName
	if label == "" {
		label = reasons.Msg(reasons.MsgOIDCSignInLabel)
	}
	opt := authprovider.LoginOption{ID: ID, Label: label, Kind: authprovider.KindRedirect, StartURL: startPath}
	if p.discovery() == nil {
		code, _ := p.Problem()
		opt.Disabled, opt.Reason = true, string(code)
	}
	return opt
}

func (p *Provider) Mount(m authprovider.Mounter) {
	p.mu.Lock()
	p.host = m
	p.mu.Unlock()
	m.Handle("GET "+startPath, http.HandlerFunc(p.start))
	m.Handle("GET "+callbackPath, http.HandlerFunc(p.callback))
}

// flow is what one sign-in attempt keeps in its cookie.
type flow struct{ state, verifier, nonce, next string }

func (f flow) encode() string {
	return url.Values{"s": {f.state}, "v": {f.verifier}, "n": {f.nonce}, "x": {f.next}}.Encode()
}

func decodeFlow(v string) (flow, bool) {
	q, err := url.ParseQuery(v)
	if err != nil {
		return flow{}, false
	}
	f := flow{state: q.Get("s"), verifier: q.Get("v"), nonce: q.Get("n"), next: q.Get("x")}
	return f, f.state != "" && f.verifier != "" && f.nonce != ""
}

// scheme is the platform's (urls.URLScheme, https unless set): never the
// request's, since behind a TLS-terminating proxy the hop to the gateway is
// plain http.
func (p *Provider) scheme(*http.Request) string { return p.urls.URLScheme() }

// baseURL is where the provider sends the browser back: urls.uiHost, which
// the config requires with OIDC, never the request's Host header.
func (p *Provider) baseURL(r *http.Request) string {
	return p.scheme(r) + "://" + p.urls.UIHost
}

func (p *Provider) oauth(d *discovered, redirect string) *oauth2.Config {
	scopes := p.cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{"profile", "email"}
	}
	if !contains(scopes, oidc.ScopeOpenID) {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}
	return &oauth2.Config{
		ClientID: p.cfg.ClientID, ClientSecret: p.secret(), Endpoint: d.provider.Endpoint(),
		RedirectURL: redirect, Scopes: scopes,
	}
}

func contains(l []string, s string) bool {
	for _, e := range l {
		if e == s {
			return true
		}
	}
	return false
}

// secret reads the client secret from its file at each use, so a rotated
// Secret is picked up. A public client has none.
func (p *Provider) secret() string {
	if p.cfg.ClientSecretFile == "" {
		return ""
	}
	b, err := os.ReadFile(p.cfg.ClientSecretFile)
	if err != nil {
		slog.Error("read oidc client secret file", "err", err)
		return ""
	}
	return strings.TrimSpace(string(b))
}

// MaxFlows is how many sign-ins may be under way in one browser: a start
// that finds that many flow cookies clears them, so a page cannot pile them
// up (a cookie bomb). maxNext is the longest next kept.
const (
	MaxFlows = 3
	maxNext  = 1024
)

// safeNext keeps only a local path of at most maxNext bytes, so a login link
// cannot send people to another site or bloat the flow cookie; anything else
// becomes "/".
func safeNext(next string) string {
	if len(next) > maxNext || !httpx.IsLocalPath(next) {
		return "/"
	}
	return next
}

// flowCookie is the cookie of the sign-in flow with this state, without its
// value. Over TLS it is a __Host- cookie, which must have Path "/", so no
// sibling host can plant a flow; the proxy keeps it from apps.
func (p *Provider) flowCookie(r *http.Request, state string) http.Cookie {
	secure := p.scheme(r) == "https"
	return http.Cookie{
		Name: model.CookieName(flowPrefix+state[:8], secure), Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	}
}

func (p *Provider) start(w http.ResponseWriter, r *http.Request) {
	d := p.discovery()
	if d == nil {
		code, _ := p.Problem()
		renderPage(w, http.StatusServiceUnavailable, code, false)
		return
	}
	var flows []*http.Cookie
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, flowPrefix) || strings.HasPrefix(c.Name, model.CookieName(flowPrefix, true)) {
			flows = append(flows, c)
		}
	}
	if len(flows) >= MaxFlows {
		for _, old := range flows {
			http.SetCookie(w, &http.Cookie{Name: old.Name, Path: "/", MaxAge: -1, HttpOnly: true, Secure: p.scheme(r) == "https", SameSite: http.SameSiteLaxMode})
		}
	}
	f := flow{state: rand.Text(), verifier: oauth2.GenerateVerifier(), nonce: rand.Text(), next: safeNext(r.URL.Query().Get("next"))}
	c := p.flowCookie(r, f.state)
	c.Value, c.MaxAge = f.encode(), int(flowMaxAge.Seconds())
	http.SetCookie(w, &c)
	u := p.oauth(d, p.baseURL(r)+callbackPath).AuthCodeURL(f.state, oauth2.S256ChallengeOption(f.verifier), oidc.Nonce(f.nonce))
	http.Redirect(w, r, u, http.StatusFound)
}

func (p *Provider) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	if len(state) < len(flowPrefix) {
		renderPage(w, http.StatusBadRequest, reasons.SignInLinkExpired, true)
		return
	}
	clear := p.flowCookie(r, state)
	c, err := r.Cookie(clear.Name)
	// The flow is single use whatever happens next.
	clear.MaxAge = -1
	http.SetCookie(w, &clear)
	if err != nil {
		renderPage(w, http.StatusBadRequest, reasons.SignInLinkExpired, true)
		return
	}
	f, ok := decodeFlow(c.Value)
	if !ok || subtle.ConstantTimeCompare([]byte(f.state), []byte(state)) != 1 {
		renderPage(w, http.StatusBadRequest, reasons.SignInLinkExpired, true)
		return
	}
	if e := q.Get("error"); e != "" {
		slog.Info("identity provider refused the sign-in", "error", e)
		renderPage(w, http.StatusBadRequest, reasons.SignInLinkExpired, true)
		return
	}
	d := p.discovery()
	if d == nil {
		code, _ := p.Problem()
		renderPage(w, http.StatusServiceUnavailable, code, false)
		return
	}
	ctx := oidc.ClientContext(r.Context(), p.client)
	tok, err := p.oauth(d, p.baseURL(r)+callbackPath).Exchange(ctx, q.Get("code"), oauth2.VerifierOption(f.verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.Response != nil && re.Response.StatusCode < 500 {
			slog.Info("code exchange refused", "status", re.Response.StatusCode, "error", re.ErrorCode)
			renderPage(w, http.StatusBadRequest, reasons.SignInLinkExpired, true)
			return
		}
		slog.Warn("code exchange failed", "err", errText(err))
		renderPage(w, http.StatusBadGateway, reasons.IdpUnreachable, false)
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	idt, err := d.verifier.Verify(ctx, rawID)
	if err != nil {
		var expired *oidc.TokenExpiredError
		if errors.As(err, &expired) {
			slog.Warn("id token already expired at sign-in", "expiry", expired.Expiry)
			renderPage(w, http.StatusBadRequest, reasons.ClockSkew, false)
			return
		}
		slog.Info("id token refused", "err", errText(err))
		renderPage(w, http.StatusBadRequest, reasons.SignInLinkExpired, true)
		return
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(f.nonce)) != 1 {
		slog.Info("id token nonce does not match the flow")
		renderPage(w, http.StatusBadRequest, reasons.SignInLinkExpired, true)
		return
	}
	if ahead := idt.IssuedAt.Sub(p.now()); ahead > maxIATAhead {
		slog.Warn("id token issued in the future: clocks differ", "ahead", ahead.Round(time.Second))
		renderPage(w, http.StatusBadRequest, reasons.ClockSkew, false)
		return
	}
	var raw map[string]any
	if err := idt.Claims(&raw); err != nil {
		slog.Info("id token claims unreadable", "err", err)
		renderPage(w, http.StatusBadRequest, reasons.SignInLinkExpired, true)
		return
	}
	claims, err := Extract(raw, p.cfg.UsernameClaim, p.cfg.GroupsClaim, p.cfg.RolesClaim)
	switch {
	case errors.Is(err, ErrGroupsOverage):
		renderPage(w, http.StatusForbidden, reasons.GroupsOverage, false)
		return
	case err != nil:
		slog.Info("id token has no usable subject", "err", err)
		renderPage(w, http.StatusBadRequest, reasons.SignInLinkExpired, true)
		return
	}
	claims.Issuer = p.cfg.Issuer
	signed, err := p.host.Complete(r.Context(), claims, session.Session{
		Provider: ID, Kind: session.KindUI, Refresh: tok.RefreshToken, IDToken: rawID,
		RevalidateAt: p.now().Add(p.cfg.RevalidateInterval.Duration),
	})
	if err != nil {
		slog.Error("complete sign-in", "err", err)
		http.Error(w, reasons.Msg(reasons.MsgServerError), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &signed.Cookie)
	http.Redirect(w, r, f.next, http.StatusFound)
}

// errText is err's text without a response body: the standard library puts
// the URL in transport errors, and oauth2 puts the response in RetrieveError.
func errText(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return fmt.Sprintf("token endpoint answered %d (%s)", re.Response.StatusCode, re.ErrorCode)
	}
	return err.Error()
}

// Revalidate asks the identity provider again with the stored refresh token.
// invalid_grant means the person is no longer valid; any other failure is an
// outage. A rotated refresh token and the groups of a refreshed ID token are
// returned in the session to store.
func (p *Provider) Revalidate(ctx context.Context, s session.Session) (identity.Claims, session.Session, error) {
	cur := identity.Claims{
		Issuer: p.cfg.Issuer, Username: s.Principal.Username, Email: s.Principal.Email,
		DisplayName: s.Principal.DisplayName, Picture: s.Principal.Picture, Groups: s.Principal.Groups,
		ProviderRoles: s.Principal.ProviderRoles,
	}
	d := p.discovery()
	if d == nil {
		return identity.Claims{}, s, authprovider.ErrUnavailable
	}
	// Without a refresh token the provider cannot be asked whether the person
	// is still valid, so the session ends rather than living on trust.
	if s.Refresh == "" {
		return identity.Claims{}, s, authprovider.ErrRevoked
	}
	ctx = oidc.ClientContext(ctx, p.client)
	tok, err := p.oauth(d, "").TokenSource(ctx, &oauth2.Token{RefreshToken: s.Refresh}).Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
			return identity.Claims{}, s, authprovider.ErrRevoked
		}
		slog.Warn("refresh failed", "err", errText(err))
		return identity.Claims{}, s, authprovider.ErrUnavailable
	}
	if tok.RefreshToken != "" {
		s.Refresh = tok.RefreshToken
	}
	if rawID, _ := tok.Extra("id_token").(string); rawID != "" {
		if c, ok := p.claimsFrom(ctx, d, rawID); ok {
			c.Username, c.Email, c.DisplayName, c.Picture = cur.Username, cur.Email, cur.DisplayName, cur.Picture
			cur = c
			s.IDToken = rawID
			s.Principal = authz.Principal{
				ID: s.Principal.ID, Username: s.Principal.Username, Email: s.Principal.Email,
				DisplayName: s.Principal.DisplayName, Picture: s.Principal.Picture, Issuer: s.Principal.Issuer, Groups: c.Groups,
				ProviderRoles: c.ProviderRoles,
			}
		}
	}
	s.RevalidateAt = p.now().Add(p.cfg.RevalidateInterval.Duration)
	return cur, s, nil
}

// claimsFrom verifies a refreshed ID token and reads its claims. When it
// cannot be used the session keeps the groups it had.
func (p *Provider) claimsFrom(ctx context.Context, d *discovered, rawID string) (identity.Claims, bool) {
	idt, err := d.verifier.Verify(ctx, rawID)
	if err != nil {
		slog.Warn("refreshed id token refused, keeping the session's groups", "err", errText(err))
		return identity.Claims{}, false
	}
	var raw map[string]any
	if err := idt.Claims(&raw); err != nil {
		return identity.Claims{}, false
	}
	c, err := Extract(raw, p.cfg.UsernameClaim, p.cfg.GroupsClaim, p.cfg.RolesClaim)
	if err != nil {
		slog.Warn("refreshed id token has no usable claims, keeping the session's groups", "err", err)
		return identity.Claims{}, false
	}
	c.Issuer = p.cfg.Issuer
	return c, true
}

// LogoutURL is the identity provider's end-session URL for RP-initiated
// logout, or "" when discovery has none or has not succeeded.
func (p *Provider) LogoutURL(s session.Session) string {
	d := p.discovery()
	if d == nil || d.endSession == "" {
		return ""
	}
	u, err := url.Parse(d.endSession)
	if err != nil {
		return ""
	}
	q := u.Query()
	if s.IDToken != "" {
		q.Set("id_token_hint", s.IDToken)
	}
	q.Set("client_id", p.cfg.ClientID)
	if p.urls.UIHost != "" {
		q.Set("post_logout_redirect_uri", p.baseURL(nil)+"/")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Package authn knows who calls the API: sign-in and sign-out, sessions
// and their revalidation against the identity provider, personal API
// tokens and their routes, the same-origin guard, and the caller each
// handler reads with User.
package authn

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/core/registry"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/gateway/ratelimit"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// SessionScheme names the session cookie in the OpenAPI document;
// BearerScheme the personal API token.
const (
	SessionScheme = "session"
	BearerScheme  = "token"
)

// RequireSession is the document-wide security requirement: a session or a
// token. Public operations override it with NoSession, the token operations
// with SessionOnly.
var (
	RequireSession = []map[string][]string{{SessionScheme: {}}, {BearerScheme: {}}}
	SessionOnly    = []map[string][]string{{SessionScheme: {}}}
	// NoSession is the requirement of the publicRoutes operations.
	NoSession = []map[string][]string{}
)

// publicRoutes are the only /api routes that work without a session.
// Everything else under /api/, including routes added later, needs one.
var publicRoutes = map[string]bool{
	"POST /api/auth/local/login": true,
	"POST /api/auth/logout":      true,
	"GET /api/auth/options":      true,
	"GET /api/reasons":           true,
	"GET /api/branding":          true,
	"GET /api/openapi.json":      true,
	"GET /api/openapi.yaml":      true,
}

// IdpUnreachableHeader tells the UI the identity provider cannot be asked and
// until when the session keeps working.
const IdpUnreachableHeader = "Javo-Idp-Unreachable-Until"

// IdpUnreachableMessageHeader carries the banner text for the outage, from the
// message catalog, so the UI shows the server's words.
const IdpUnreachableMessageHeader = "Javo-Idp-Unreachable-Message"

// LogoutURLHeader carries, on the logout response, where the UI sends the
// browser to end the identity provider's own session (OIDC RP-initiated
// logout). It is absent when the provider has no such endpoint.
const LogoutURLHeader = "Javo-Logout-Url"

// LogoutReasonHeader carries, on the logout response, the catalog code
// SignedOutLocallyOnly when the session's provider has no end-session endpoint,
// so the UI can say the identity provider's own session is still open.
const LogoutReasonHeader = "Javo-Logout-Reason"

type (
	userKey   struct{}
	tokenKey  struct{}
	clientKey struct{}
	peerKey   struct{}
	// apiTokenKey holds the id of the API token a request came with.
	apiTokenKey struct{}
	// appTokenKey is set (true) when that token was made for an app or a
	// job (token.Token.Workload).
	appTokenKey struct{}
)

// Authenticator turns the javo_session cookie or an API token into a
// Principal, and serves sign-in and sign-out.
type Authenticator struct {
	Providers   *registry.Registry[authprovider.Provider]
	People      People
	Roles       config.Roles
	Sessions    session.Store
	Secure      bool
	Limiter     *ratelimit.Login
	Trusted     []netip.Prefix
	Clock       func() time.Time
	Revalidator *Revalidator
	Tokens      *token.DBStore
	// GroupsMaxAge is how long after the owner's last sign-in a token works.
	GroupsMaxAge time.Duration
	// Apps reports whether the app an app token is bound to still wants it.
	Apps token.AppCheck
	// Active, when set, hears of every request a signed-in browser makes.
	Active func(userID string)
}

func (a *Authenticator) now() time.Time {
	if a.Clock != nil {
		return a.Clock()
	}
	return time.Now()
}

// RequireUser wraps every /api route. It stores the session token and
// client IP in the request context; outside publicRoutes it answers 401
// unless the token names a live session, whose Principal it stores too.
// A request with an Authorization: Bearer header is the token alone: its
// cookies are not read, so it cannot end or use the browser's session.
func (a *Authenticator) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer, hasBearer := BearerToken(r)
		var token string
		if c, err := r.Cookie(a.sessionCookie()); err == nil && !hasBearer {
			token = c.Value
		}
		ctx := context.WithValue(r.Context(), tokenKey{}, token)
		ctx = context.WithValue(ctx, clientKey{}, ratelimit.ClientIP(r, a.Trusted))
		ctx = context.WithValue(ctx, peerKey{}, r.RemoteAddr)
		if publicRoutes[r.Method+" "+r.URL.Path] {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		if hasBearer {
			p, tok, ok := a.tokenPrincipal(ctx, w, r, bearer)
			if !ok {
				return
			}
			ctx = context.WithValue(ctx, apiTokenKey{}, tok.ID)
			ctx = context.WithValue(ctx, appTokenKey{}, tok.Workload())
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, userKey{}, p)))
			return
		}
		// Only a UI session opens the API: an app session or a ticket carried
		// in the session cookie is refused like no session at all.
		sess, err := a.Sessions.Get(ctx, token)
		switch {
		case err == nil && sess.Kind == session.KindUI:
		case err == nil, errors.Is(err, session.ErrSessionNotFound), errors.Is(err, session.ErrSessionExpired):
			problem.Write(w, http.StatusUnauthorized, reasons.Msg(reasons.MsgSignInRequired))
			return
		default:
			slog.Error("session lookup", "err", err)
			problem.Write(w, http.StatusInternalServerError, reasons.Msg(reasons.MsgServerError))
			return
		}
		// A UI session with no deadline is due now, so a provider that forgot
		// to set one cannot keep a deleted account signed in.
		if a.Revalidator.Due(sess) {
			var ok bool
			if sess, ok = a.revalidate(ctx, w, token, sess); !ok {
				return
			}
		}
		if a.Active != nil {
			a.Active(sess.Principal.ID)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, userKey{}, sess.Principal)))
	})
}

// BearerToken returns the RFC 6750 bearer token of r, if it carries one. The
// scheme name is case-insensitive (RFC 9110 section 11.1).
func BearerToken(r *http.Request) (string, bool) {
	scheme, cred, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return strings.TrimSpace(cred), true
}

// tokenPrincipal checks the API token text for a request with method and
// returns its owner and the token, or false after answering the request itself:
// 401 for a token that is not valid (RFC 6750 invalid_token), 403 for a read
// token on anything but GET and HEAD (insufficient_scope). The owner is
// built from their Identity, with the groups recorded at their last sign-in;
// a sign-in older than groupsMaxAge pauses the token.
func (a *Authenticator) tokenPrincipal(ctx context.Context, w http.ResponseWriter, r *http.Request, text string) (authz.Principal, token.Token, bool) {
	refuse := func(status int, bearerErr string, code reasons.MessageCode) (authz.Principal, token.Token, bool) {
		w.Header().Set("WWW-Authenticate", `Bearer error="`+bearerErr+`"`)
		problem.Write(w, status, reasons.Msg(code))
		return authz.Principal{}, token.Token{}, false
	}
	fail := func(what string, err error) (authz.Principal, token.Token, bool) {
		slog.Error(what, "err", err)
		problem.Write(w, http.StatusInternalServerError, reasons.Msg(reasons.MsgServerError))
		return authz.Principal{}, token.Token{}, false
	}
	tok, err := a.Tokens.Authenticate(ctx, text)
	switch {
	case errors.Is(err, token.ErrInvalid):
		return refuse(http.StatusUnauthorized, "invalid_token", reasons.MsgTokenInvalid)
	case err != nil:
		return fail("token lookup", err)
	}
	if isTokenRoute(r.URL.Path) {
		problem.Write(w, http.StatusForbidden, reasons.Msg(reasons.MsgTokenCannotManageTokens))
		return authz.Principal{}, token.Token{}, false
	}
	if IsPlanDecision(r.URL.Path) {
		problem.Write(w, http.StatusForbidden, reasons.Msg(reasons.MsgPlanSessionOnly))
		return authz.Principal{}, token.Token{}, false
	}
	if !tok.Scope.Allows(r.Method) {
		return refuse(http.StatusForbidden, "insufficient_scope", reasons.MsgTokenReadOnly)
	}
	if tok.App != nil {
		switch ok, err := a.Apps(ctx, *tok.App, tok.Scope); {
		case err != nil:
			return fail("token app lookup", err)
		case !ok:
			return refuse(http.StatusUnauthorized, "invalid_token", reasons.MsgTokenInvalid)
		}
	}
	person, err := a.People.ByName(ctx, tok.Owner)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return refuse(http.StatusUnauthorized, "invalid_token", reasons.MsgTokenInvalid)
	case err != nil:
		return fail("token owner lookup", err)
	}
	if a.now().Sub(person.LastLoginAt) > a.GroupsMaxAge {
		return refuse(http.StatusUnauthorized, "invalid_token", reasons.MsgTokenSignInAgain)
	}
	a.Tokens.Touch(ctx, tok)
	return identity.PrincipalOf(person), tok, true
}

// isTokenRoute is true for the routes that create, list and revoke tokens,
// which only a browser session may use.
func isTokenRoute(p string) bool {
	p = path.Clean("/" + p)
	return p == tokensRoute || strings.HasPrefix(p, tokensRoute+"/")
}

// viaAppToken reports whether the request came with the API token of an app
// or a job.
func viaAppToken(ctx context.Context) bool {
	app, _ := ctx.Value(appTokenKey{}).(bool)
	return app
}

// viaToken reports whether the request came with an API token.
func viaToken(ctx context.Context) bool {
	id, _ := ctx.Value(apiTokenKey{}).(string)
	return id != ""
}

// revalidate asks the Revalidator about the session behind token. It returns
// the session to use, or false after answering the request itself.
func (a *Authenticator) revalidate(ctx context.Context, w http.ResponseWriter, token string, sess session.Session) (session.Session, bool) {
	fresh, unreachableUntil, err := a.Revalidator.Check(ctx, session.ID(token), sess)
	switch {
	case errors.Is(err, ErrSessionEnded):
		problem.Write(w, http.StatusUnauthorized, reasons.Msg(reasons.MsgSessionEnded))
		return sess, false
	case err != nil:
		problem.Write(w, http.StatusInternalServerError, reasons.Msg(reasons.MsgServerError))
		return sess, false
	}
	if !unreachableUntil.IsZero() {
		w.Header().Set(IdpUnreachableHeader, unreachableUntil.UTC().Format(time.RFC3339))
		w.Header().Set(IdpUnreachableMessageHeader, reasons.Msg(reasons.MsgIdpOutageBanner, unreachableUntil.UTC().Format("15:04")+" UTC"))
	}
	return fresh, true
}

// IDToken is the OpenID Connect ID token of the browser session behind the
// request, renewed first when it is about to expire; empty for a request
// with an API token or a session without one (a local account).
func (a *Authenticator) IDToken(ctx context.Context) (string, error) {
	token, _ := ctx.Value(tokenKey{}).(string)
	if token == "" {
		return "", nil
	}
	sess, err := a.Sessions.Get(ctx, token)
	if err != nil || sess.IDToken == "" {
		return "", err
	}
	sess, err = a.Revalidator.FreshIDToken(ctx, session.ID(token), sess)
	return sess.IDToken, err
}

// User returns the Principal RequireUser stored; the zero Principal
// (nobody) on public routes.
func User(ctx context.Context) authz.Principal {
	p, _ := ctx.Value(userKey{}).(authz.Principal)
	return p
}

// sessionCookie is the name the session cookie is set and read under.
func (a *Authenticator) sessionCookie() string {
	return model.CookieName(model.SessionCookieName, a.Secure)
}

// cookie builds the session cookie; an empty token clears it.
func (a *Authenticator) cookie(token string) http.Cookie {
	c := http.Cookie{
		Name: a.sessionCookie(), Value: token, Path: "/",
		HttpOnly: true, Secure: a.Secure, SameSite: http.SameSiteStrictMode,
	}
	if token == "" {
		c.MaxAge = -1
	}
	return c
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// me is how the API reports a Principal; roles come from the config at the
// time of the request, never from the session.
func (a *Authenticator) me(p authz.Principal) authprovider.Me {
	roles := []string{}
	for _, r := range authz.RolesOf(p, a.Roles) {
		roles = append(roles, string(r))
	}
	return authprovider.Me{ID: p.ID, Username: p.Username, DisplayName: p.DisplayName, Picture: p.Picture, Groups: nonNilStrings(p.Groups), Roles: roles}
}

type logoutOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	LogoutURL string      `header:"Javo-Logout-Url"`
	Reason    string      `header:"Javo-Logout-Reason"`
}

type meOutput struct{ Body authprovider.Me }

// ReasonCopy is the title, explanation and next step of one catalog reason.
type ReasonCopy struct {
	Title string `json:"title"`
	What  string `json:"what"`
	Todo  string `json:"todo"`
}

type reasonCopyOutput struct{ Body ReasonCopy }

type optionsOutput struct{ Body []authprovider.LoginOption }

// mounter is what providers see of the host.
type mounter struct {
	a   *Authenticator
	api huma.API
	mux *http.ServeMux
}

func (m *mounter) API() huma.API { return m.api }

// Handle mounts a browser route. Like the /api routes it sees the session
// cookie's token in its context, so Complete ends the session the browser had.
func (m *mounter) Handle(pattern string, h http.Handler) {
	m.mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var token string
		if c, err := r.Cookie(m.a.sessionCookie()); err == nil {
			token = c.Value
		}
		ctx := context.WithValue(r.Context(), tokenKey{}, token)
		ctx = context.WithValue(ctx, clientKey{}, ratelimit.ClientIP(r, m.a.Trusted))
		ctx = context.WithValue(ctx, peerKey{}, r.RemoteAddr)
		h.ServeHTTP(w, r.WithContext(ctx))
	}))
}

func (m *mounter) Now() time.Time { return m.a.now() }

func (m *mounter) Reject(status int, code reasons.MessageCode) error {
	return problem.New(status, reasons.Msg(code))
}

// LimitLogin counts one attempt and waits out recent failures at the
// username; the peer is logged because behind a proxy it is the address to
// list in --trusted-proxies.
func (m *mounter) LimitLogin(ctx context.Context, username string) (func(bool), error) {
	ip, _ := ctx.Value(clientKey{}).(string)
	peer, _ := ctx.Value(peerKey{}).(string)
	slog.Info("login attempt", "client", ip, "peer", peer)
	username = strings.ToLower(username)
	if wait, ok := m.a.Limiter.Allow(ip, username); !ok {
		return nil, problem.New(http.StatusTooManyRequests, reasons.Msg(reasons.MsgTooManyAttempts)).
			WithHeader("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	}
	if err := m.a.Limiter.Wait(ctx, username); err != nil {
		return nil, err
	}
	return func(right bool) {
		if right {
			m.a.Limiter.Succeeded(username)
		} else {
			m.a.Limiter.Failed(username)
		}
	}, nil
}

func (m *mounter) Complete(ctx context.Context, c identity.Claims, s session.Session) (authprovider.Signed, error) {
	person, err := m.a.People.Ensure(ctx, c.ForStore())
	if err != nil {
		return authprovider.Signed{}, err
	}
	s.Principal = authz.Principal{ID: person.Name, Username: c.Username, Email: c.Email, DisplayName: c.DisplayName, Picture: c.Picture, Issuer: c.Issuer, Groups: c.Groups, ProviderRoles: c.ProviderRoles}
	// A login replaces whatever session this browser had.
	if old, _ := ctx.Value(tokenKey{}).(string); old != "" {
		if err := m.a.Sessions.Delete(ctx, old); err != nil {
			slog.Warn("delete previous session", "err", err)
		}
	}
	token, err := m.a.Sessions.Create(ctx, s)
	if err != nil {
		return authprovider.Signed{}, err
	}
	return authprovider.Signed{Cookie: m.a.cookie(token), Me: m.a.me(s.Principal)}, nil
}

// Register mounts the provider-neutral routes and every provider's own.
func (a *Authenticator) Register(api huma.API, mux *http.ServeMux) {
	m := &mounter{a: a, api: api, mux: mux}
	for _, p := range a.Providers.All() {
		p.Mount(m)
	}

	huma.Register(api, huma.Operation{
		OperationID: "authOptions", Method: http.MethodGet, Path: "/api/auth/options",
		Summary: "The ways to sign in", Tags: []string{"auth"}, Security: NoSession,
	}, func(context.Context, *struct{}) (*optionsOutput, error) {
		out := &optionsOutput{Body: []authprovider.LoginOption{}}
		for _, p := range a.Providers.All() {
			out.Body = append(out.Body, p.Option())
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "reasonCopy", Method: http.MethodGet, Path: "/api/reasons",
		Summary: "The words for one catalog reason", Tags: []string{"auth"}, Security: NoSession,
		Errors: []int{http.StatusNotFound},
	}, func(_ context.Context, in *struct {
		Code string `query:"code" maxLength:"64"`
	}) (*reasonCopyOutput, error) {
		r, ok := reasons.Lookup(reasons.Code(in.Code))
		if !ok {
			return nil, problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgNoSuchAddress))
		}
		return &reasonCopyOutput{Body: ReasonCopy{Title: r.Title, What: r.What, Todo: r.Todo}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "logout", Method: http.MethodPost, Path: "/api/auth/logout",
		Summary: "Sign out and end the session", Tags: []string{"auth"}, Security: NoSession,
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, _ *struct{}) (*logoutOutput, error) {
		token, _ := ctx.Value(tokenKey{}).(string)
		out := &logoutOutput{SetCookie: a.cookie("")}
		if sess, err := a.Sessions.Get(ctx, token); err == nil {
			if p, ok := a.Providers.Get(sess.Provider); ok {
				if lr, ok := p.(authprovider.LogoutRedirector); ok {
					if out.LogoutURL = lr.LogoutURL(sess); out.LogoutURL == "" {
						out.Reason = string(reasons.SignedOutLocallyOnly)
					}
				}
			}
		}
		if err := a.Sessions.Delete(ctx, token); err != nil {
			return nil, problem.Internal("delete session", err)
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "me", Method: http.MethodGet, Path: "/api/me",
		Summary: "The signed-in user", Tags: []string{"auth"},
		Errors: []int{http.StatusUnauthorized},
	}, func(ctx context.Context, _ *struct{}) (*meOutput, error) {
		return &meOutput{Body: a.me(User(ctx))}, nil
	})
}

// People is the part of the people table the gateway uses; *store.People
// satisfies it.
type People interface {
	Ensure(ctx context.Context, c store.Claims) (model.Person, error)
	ByName(ctx context.Context, name string) (model.Person, error)
	Search(ctx context.Context, prefix string, limit int) ([]model.Person, error)
	Reconfirmer
}

// Actor is who a change is made by, for the audit row: the person, and the
// API token when the request came with one.
func Actor(ctx context.Context) model.Actor {
	id, _ := ctx.Value(apiTokenKey{}).(string)
	return model.Actor{Person: User(ctx).ID, Token: id}
}

const PlansRoute = "/api/plans"

// IsPlanDecision is true for the approve and reject routes, which only a
// signed-in person in the browser may use: no token, so no agent, can make
// a proposed change happen.
func IsPlanDecision(p string) bool {
	rest, ok := strings.CutPrefix(p, PlansRoute+"/")
	if !ok {
		return false
	}
	_, verb, ok := strings.Cut(rest, "/")
	return ok && (verb == "approve" || verb == "reject")
}

// NoAppTokens refuses a request made with an app's or a job's own API token.
// That token lives in the app, where anyone the app is shared with can make
// it act, or in the job's runs, whose code sits in the owner's home, so it
// must not reach its owner's secrets (it could give them to its own app).
func NoAppTokens(ctx context.Context) error {
	if viaAppToken(ctx) {
		return problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgSecretsNotForAppTokens))
	}
	return nil
}

// adminPrefix is where every platform admin operation lives.
const adminPrefix = "/api/admin/"

// RegisterAdmin is the only way to add an operation under /api/admin/: it
// asks authz.Decide for ActAdminConsole before h runs, so a route cannot be
// added without the check. A member gets 403 (the route's existence is in
// the public OpenAPI document anyway, so a 404 would hide nothing). A path
// outside /api/admin/ is a wiring mistake and panics at start-up.
func RegisterAdmin[I, O any](api huma.API, roles config.Roles, op huma.Operation, h func(context.Context, *I) (*O, error)) {
	if !strings.HasPrefix(op.Path, adminPrefix) {
		panic("authn.RegisterAdmin: " + op.Path + " is not under " + adminPrefix)
	}
	op.Tags = append(op.Tags, "admin")
	op.Errors = append(op.Errors, http.StatusUnauthorized, http.StatusForbidden)
	// The check runs as operation middleware, before the input is read and
	// validated, so a member learns nothing from validation errors either.
	op.Middlewares = append(huma.Middlewares{func(hctx huma.Context, next func(huma.Context)) {
		p := User(hctx.Context())
		if d := authz.Decide(p, authz.RolesOf(p, roles), authz.ActAdminConsole, authz.Object{}); !d.Allowed {
			hctx.SetHeader("Content-Type", "application/problem+json")
			hctx.SetStatus(http.StatusForbidden)
			_ = api.Marshal(hctx.BodyWriter(), "application/problem+json", problem.New(http.StatusForbidden, reasons.Msg(d.Code, d.Args...)))
			return
		}
		next(hctx)
	}}, op.Middlewares...)
	huma.Register(api, op, h)
}

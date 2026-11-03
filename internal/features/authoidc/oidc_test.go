package authoidc_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/features/authoidc"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/reasons"
)

// fakeHost is the gateway as the provider sees it.
type fakeHost struct {
	mux       *http.ServeMux
	completed []identity.Claims
	sessions  []session.Session
}

func (h *fakeHost) API() huma.API { return nil }
func (h *fakeHost) Handle(pattern string, hd http.Handler) {
	h.mux.Handle(pattern, hd)
}
func (h *fakeHost) LimitLogin(context.Context, string) (func(bool), error) {
	return func(bool) {}, nil
}
func (h *fakeHost) Now() time.Time                           { return time.Now() }
func (h *fakeHost) Reject(status int, c reasons.MessageCode) error {
	return errors.New(reasons.Msg(c))
}
func (h *fakeHost) Complete(_ context.Context, c identity.Claims, s session.Session) (authprovider.Signed, error) {
	h.completed = append(h.completed, c)
	h.sessions = append(h.sessions, s)
	return authprovider.Signed{Cookie: http.Cookie{Name: "javo_session", Value: "tok", Path: "/"}}, nil
}

type fixture struct {
	t    *testing.T
	op   *op
	host *fakeHost
	p    *authoidc.Provider
}

func oidcConfig(o *op) config.OIDC {
	return config.OIDC{
		Issuer: opIssuer, ClientID: opClientID, UsernameClaim: "preferred_username", GroupsClaim: "groups",
		RevalidateInterval: metav1Duration(5 * time.Minute), Dial: o.dial(),
	}
}

func newFixture(t *testing.T, cfg func(*config.OIDC)) *fixture {
	t.Helper()
	o := newOP(t)
	c := oidcConfig(o)
	if cfg != nil {
		cfg(&c)
	}
	return newFixtureFor(t, o, c, &http.Client{Timeout: 2 * time.Second})
}

func newFixtureFor(t *testing.T, o *op, c config.OIDC, hc *http.Client) *fixture {
	t.Helper()
	p := authoidc.New(c, config.URLs{Scheme: "http", UIHost: "javo.test"}, hc,
		authoidc.WithRetryBackoff(5*time.Millisecond, 20*time.Millisecond))
	t.Cleanup(p.Close)
	h := &fakeHost{mux: http.NewServeMux()}
	p.Mount(h)
	return &fixture{t: t, op: o, host: h, p: p}
}

func (f *fixture) waitReady() {
	f.t.Helper()
	select {
	case <-f.p.Ready():
	case <-time.After(5 * time.Second):
		code, detail := f.p.Problem()
		f.t.Fatalf("discovery never succeeded: %s %s", code, detail)
	}
}

func (f *fixture) get(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	f.host.mux.ServeHTTP(w, req)
	return w
}

func cookieNamed(w *httptest.ResponseRecorder, prefix string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if strings.HasPrefix(c.Name, prefix) {
			return c
		}
	}
	return nil
}

// start begins a login and returns the redirect to the provider and the
// per-flow cookie.
func (f *fixture) start(next string) (string, *http.Cookie) {
	f.t.Helper()
	path := "/auth/oidc/start"
	if next != "" {
		path += "?next=" + url.QueryEscape(next)
	}
	w := f.get(path)
	if w.Code != http.StatusFound {
		f.t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	return w.Header().Get("Location"), cookieNamed(w, "javo_oidc_")
}

// login runs a whole sign-in and returns the callback's response.
func (f *fixture) login(next string, claims map[string]any) *httptest.ResponseRecorder {
	f.t.Helper()
	f.waitReady()
	loc, flow := f.start(next)
	code := f.op.authorize(loc, claims)
	state := mustQuery(f.t, loc).Get("state")
	return f.get("/auth/oidc/callback?code="+code+"&state="+state, flow)
}

func mustQuery(t *testing.T, loc string) url.Values {
	t.Helper()
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func TestOptionIsDisabledUntilDiscoverySucceeds(t *testing.T) {
	o := newOP(t)
	o.set(func() { o.down = true })
	f := newFixtureFor(t, o, oidcConfig(o), &http.Client{Timeout: time.Second})

	opt := f.p.Option()
	if !opt.Disabled || opt.Reason != string(reasons.IdpUnreachable) {
		t.Fatalf("option while the provider is down: %+v", opt)
	}
	if opt.Kind != authprovider.KindRedirect || opt.ID != "oidc" {
		t.Fatalf("option %+v", opt)
	}

	o.set(func() { o.down = false })
	f.waitReady() // the background retry found the provider with nobody restarting anything
	opt = f.p.Option()
	if opt.Disabled || opt.Reason != "" || opt.StartURL != "/auth/oidc/start" {
		t.Fatalf("option once reachable: %+v", opt)
	}
}

func TestStartWhileUnreachableIsAProblemNotAFlow(t *testing.T) {
	o := newOP(t)
	o.set(func() { o.down = true })
	f := newFixtureFor(t, o, oidcConfig(o), &http.Client{Timeout: time.Second})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, d := f.p.Problem(); d != "" {
			break
		} else if time.Now().After(deadline) {
			t.Fatal("discovery failure never recorded")
		}
	}
	w := f.get("/auth/oidc/start")
	if w.Code != http.StatusServiceUnavailable || cookieNamed(w, "javo_oidc_") != nil {
		t.Fatalf("%d, flow cookie %v", w.Code, cookieNamed(w, "javo_oidc_"))
	}
	if !strings.Contains(w.Body.String(), title(reasons.IdpUnreachable)) {
		t.Fatalf("body %s", w.Body)
	}
	// The raw discovery error names internal addresses; the page must not.
	if _, detail := f.p.Problem(); detail == "" || strings.Contains(w.Body.String(), opHost) || strings.Contains(w.Body.String(), o.srv.Listener.Addr().String()) {
		t.Fatalf("detail %q, body %s", detail, w.Body)
	}
}

func TestCodeFlowUsesPKCEAndCreatesAnIdentityForIssuerAndSubject(t *testing.T) {
	f := newFixture(t, nil)
	f.waitReady()

	loc, flow := f.start("/apps/mine")
	q := mustQuery(t, loc)
	if !strings.HasPrefix(loc, opIssuer+"/protocol/openid-connect/auth?") {
		t.Fatalf("redirect %s", loc)
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		q.Get("state") == "" || q.Get("nonce") == "" || q.Get("client_id") != opClientID ||
		q.Get("redirect_uri") != "http://javo.test/auth/oidc/callback" || q.Get("response_type") != "code" {
		t.Fatalf("authorization request %v", q)
	}
	if !strings.Contains(q.Get("scope"), "openid") {
		t.Fatalf("scope %q", q.Get("scope"))
	}
	if flow == nil || flow.Name != "javo_oidc_"+q.Get("state")[:8] || !flow.HttpOnly ||
		flow.SameSite != http.SameSiteLaxMode || flow.Path != "/auth/oidc/" || flow.MaxAge != 600 {
		t.Fatalf("flow cookie %+v", flow)
	}

	code := f.op.authorize(loc, map[string]any{"preferred_username": "alice", "email": "alice@example.com"})
	w := f.get("/auth/oidc/callback?code="+code+"&state="+q.Get("state"), flow)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/apps/mine" {
		t.Fatalf("callback %d -> %q: %s", w.Code, w.Header().Get("Location"), w.Body)
	}
	if c := cookieNamed(w, "javo_session"); c == nil || c.Value != "tok" {
		t.Fatal("the session cookie from Complete was not set")
	}
	if c := cookieNamed(w, "javo_oidc_"); c == nil || c.MaxAge >= 0 {
		t.Fatalf("flow cookie not cleared: %+v", c)
	}
	if len(f.host.completed) != 1 {
		t.Fatalf("completed %d", len(f.host.completed))
	}
	got := f.host.completed[0]
	if got.Issuer != opIssuer || got.Subject != "u-alice" || got.Username != "alice" || got.Email != "alice@example.com" {
		t.Fatalf("claims %+v", got)
	}
	s := f.host.sessions[0]
	if s.Provider != "oidc" || s.Kind != session.KindUI || s.Refresh == "" || s.IDToken == "" || s.RevalidateAt.IsZero() {
		t.Fatalf("session %+v", s)
	}
	if f.op.pkceSeen[0] != "S256" {
		t.Fatalf("pkce %v", f.op.pkceSeen)
	}
}

func TestCallbackReplayedWithoutTheVerifierIsRefusedByTheProvider(t *testing.T) {
	f := newFixture(t, nil)
	w := f.login("", nil)
	if w.Code != http.StatusFound {
		t.Fatalf("first login %d", w.Code)
	}
	// The code was single use: sending it again gets nothing.
	loc, flow := f.start("")
	_ = loc
	w = f.get("/auth/oidc/callback?code=stolen&state="+mustQuery(t, loc).Get("state"), flow)
	if w.Code == http.StatusFound || len(f.host.completed) != 1 {
		t.Fatalf("unknown code accepted: %d", w.Code)
	}
}

func TestIssuerThatDiffersFromDiscoveryIsReportedWithBothStrings(t *testing.T) {
	for name, issuer := range map[string]string{
		"prefix missing": "http://" + opHost + "/realms/javo",
		"trailing slash": opIssuer + "/",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, func(c *config.OIDC) { c.Issuer = issuer })
			deadline := time.Now().Add(5 * time.Second)
			for f.p.Option().Reason != string(reasons.IssuerMismatch) {
				if time.Now().After(deadline) {
					code, detail := f.p.Problem()
					t.Fatalf("reason %s (%s)", code, detail)
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !f.p.Option().Disabled {
				t.Fatal("a misconfigured provider must not offer sign-in")
			}
			code, detail := f.p.Problem()
			if code != reasons.IssuerMismatch || !strings.Contains(detail, issuer) || !strings.Contains(detail, opIssuer) {
				t.Fatalf("problem %s: %q", code, detail)
			}
			w := f.get("/auth/oidc/start")
			// The sign-in page is for everyone: the strings are admin detail.
			if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), title(reasons.IssuerMismatch)) ||
				strings.Contains(w.Body.String(), opIssuer) || strings.Contains(w.Body.String(), "configured issuer") {
				t.Fatalf("start %d: %s", w.Code, w.Body)
			}
		})
	}
}

func TestCallbackWithoutAFlowCookieOffersToStartAgain(t *testing.T) {
	f := newFixture(t, nil)
	f.waitReady()
	loc, _ := f.start("")
	code := f.op.authorize(loc, nil)
	w := f.get("/auth/oidc/callback?code=" + code + "&state=" + mustQuery(t, loc).Get("state")) // no cookie
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, title(reasons.SignInLinkExpired)) ||
		!strings.Contains(body, `href="/auth/oidc/start"`) || !strings.Contains(body, "Start again") {
		t.Fatalf("body %s", body)
	}
	if len(f.host.completed) != 0 {
		t.Fatal("signed in without a flow cookie")
	}
}

func TestCallbackWithAnotherFlowsCookieIsRefused(t *testing.T) {
	f := newFixture(t, nil)
	f.waitReady()
	locA, flowA := f.start("")
	locB, _ := f.start("")
	code := f.op.authorize(locB, nil)
	// B's state with A's cookie: names differ, so B's cookie is missing.
	w := f.get("/auth/oidc/callback?code="+code+"&state="+mustQuery(t, locB).Get("state"), flowA)
	if w.Code != http.StatusBadRequest || len(f.host.completed) != 0 {
		t.Fatalf("status %d", w.Code)
	}
	_ = locA
}

func TestParallelFlowsKeepTheirOwnCookies(t *testing.T) {
	f := newFixture(t, nil)
	f.waitReady()
	locA, flowA := f.start("/a")
	locB, flowB := f.start("/b")
	if flowA.Name == flowB.Name {
		t.Fatal("two tabs share a flow cookie, so the second login would overwrite the first")
	}
	for _, tc := range []struct {
		loc  string
		flow *http.Cookie
		next string
	}{{locA, flowA, "/a"}, {locB, flowB, "/b"}} {
		code := f.op.authorize(tc.loc, nil)
		w := f.get("/auth/oidc/callback?code="+code+"&state="+mustQuery(t, tc.loc).Get("state"), flowA, flowB)
		if w.Header().Get("Location") != tc.next {
			t.Fatalf("flow for %s went to %q", tc.next, w.Header().Get("Location"))
		}
	}
}

func TestBadIDTokensAreRefused(t *testing.T) {
	cases := map[string]func(map[string]any){
		"wrong nonce":    func(c map[string]any) { c["nonce"] = "someone-elses" },
		"wrong audience": func(c map[string]any) { c["aud"] = "another-client" },
		"expired": func(c map[string]any) {
			c["exp"] = time.Now().Add(-time.Hour).Unix()
			c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
		},
		"wrong issuer": func(c map[string]any) { c["iss"] = "http://" + opHost + "/auth/realms/other" },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			f.op.set(func() { f.op.tokenMod = mod })
			w := f.login("", nil)
			if w.Code < 400 || len(f.host.completed) != 0 || cookieNamed(w, "javo_session") != nil {
				t.Fatalf("%d: signed in with a %s token", w.Code, name)
			}
		})
	}
}

func TestIssuedAtFarInTheFutureIsAClockSkewProblem(t *testing.T) {
	f := newFixture(t, nil)
	f.op.set(func() {
		f.op.tokenMod = func(c map[string]any) {
			c["iat"] = time.Now().Add(10 * time.Minute).Unix()
			c["exp"] = time.Now().Add(2 * time.Hour).Unix()
		}
	})
	w := f.login("", nil)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), title(reasons.ClockSkew)) {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	if len(f.host.completed) != 0 {
		t.Fatal("signed in")
	}
}

func TestIssuedAtWithinFiveMinutesIsAccepted(t *testing.T) {
	f := newFixture(t, nil)
	f.op.set(func() { f.op.tokenMod = func(c map[string]any) { c["iat"] = time.Now().Add(2 * time.Minute).Unix() } })
	if w := f.login("", nil); w.Code != http.StatusFound {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
}

func TestNextMustBeALocalPath(t *testing.T) {
	for _, next := range []string{"//evil.example.com", "https://evil.example.com", `/\evil.example.com`, "evil", "/ok\n"} {
		f := newFixture(t, nil)
		w := f.login(next, nil)
		if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
			t.Errorf("next=%q redirected to %q (%d)", next, w.Header().Get("Location"), w.Code)
		}
	}
	f := newFixture(t, nil)
	if w := f.login("/apps/x?tab=sharing", nil); w.Header().Get("Location") != "/apps/x?tab=sharing" {
		t.Errorf("local path with query went to %q", w.Header().Get("Location"))
	}
}

func TestGroupsAreKeptVerbatim(t *testing.T) {
	f := newFixture(t, nil)
	f.login("", map[string]any{"groups": []string{"/org/data-science", "/org/ml"}})
	g := f.host.completed[0].Groups
	if len(g) != 2 || g[0] != "/org/data-science" || g[1] != "/org/ml" {
		t.Fatalf("groups %v", g)
	}
}

func TestGroupsFromADottedClaim(t *testing.T) {
	f := newFixture(t, func(c *config.OIDC) { c.GroupsClaim = "resource_access.javo.roles" })
	f.login("", map[string]any{"resource_access": map[string]any{"javo": map[string]any{"roles": []string{"Editors"}}}})
	if g := f.host.completed[0].Groups; len(g) != 1 || g[0] != "Editors" {
		t.Fatalf("groups %v", g)
	}
}

// With rolesClaim set, sign-in reads the provider's roles next to the
// groups, and a refreshed ID token replaces them as it replaces the groups.
func TestProviderRolesAtSignInAndOnRevalidate(t *testing.T) {
	f := newFixture(t, func(c *config.OIDC) { c.RolesClaim = "roles" })
	if w := f.login("", map[string]any{"groups": []string{"/org/ds"}, "roles": []string{"gpu-users"}}); w.Code != http.StatusFound {
		t.Fatalf("login %d: %s", w.Code, w.Body)
	}
	if r := f.host.completed[0].ProviderRoles; len(r) != 1 || r[0] != "gpu-users" {
		t.Fatalf("provider roles at sign-in %v", r)
	}
	s := f.host.sessions[0]
	s.Principal.ProviderRoles = []string{"gpu-users"}
	f.op.set(func() { f.op.refreshGroups, f.op.refreshRoles = []string{"/org/ds"}, []string{"viewers"} })
	c, fresh, err := f.p.Revalidate(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ProviderRoles) != 1 || c.ProviderRoles[0] != "viewers" || len(fresh.Principal.ProviderRoles) != 1 || fresh.Principal.ProviderRoles[0] != "viewers" {
		t.Fatalf("provider roles after refresh %v / %v", c.ProviderRoles, fresh.Principal.ProviderRoles)
	}
}

func TestGroupsOverageFailsSignInWithItsOwnMessage(t *testing.T) {
	f := newFixture(t, nil)
	w := f.login("", map[string]any{
		"_claim_names":   map[string]any{"groups": "src1"},
		"_claim_sources": map[string]any{"src1": map[string]any{"endpoint": "https://graph.example.com/users/u-alice/getMemberObjects"}},
	})
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), title(reasons.GroupsOverage)) {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	if len(f.host.completed) != 0 {
		t.Fatal("signed in")
	}
}

func liveSession(refresh string) session.Session {
	return session.Session{Provider: "oidc", Kind: session.KindUI, Refresh: refresh}
}

// signedIn runs a login and returns the session Complete was given.
func (f *fixture) signedIn() session.Session {
	f.t.Helper()
	if w := f.login("", map[string]any{"groups": []string{"old"}}); w.Code != http.StatusFound {
		f.t.Fatalf("login %d: %s", w.Code, w.Body)
	}
	s := f.host.sessions[0]
	s.Principal.Groups = []string{"old"}
	return s
}

func TestRevalidateRotatesTheRefreshTokenAndTakesGroupsFromTheNewIDToken(t *testing.T) {
	f := newFixture(t, nil)
	s := f.signedIn()
	f.op.set(func() { f.op.refreshGroups = []string{"/org/ml"} })
	c, fresh, err := f.p.Revalidate(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Refresh == "" || fresh.Refresh == s.Refresh {
		t.Fatal("the rotated refresh token was not handed back to be stored")
	}
	if len(c.Groups) != 1 || c.Groups[0] != "/org/ml" || len(fresh.Principal.Groups) != 1 || fresh.Principal.Groups[0] != "/org/ml" {
		t.Fatalf("groups %v / %v", c.Groups, fresh.Principal.Groups)
	}
	if fresh.IDToken == s.IDToken || !fresh.RevalidateAt.After(time.Now().Add(4*time.Minute)) {
		t.Fatalf("session %+v", fresh)
	}
}

func TestRevalidateWithoutAnIDTokenKeepsTheGroupsTheSessionHad(t *testing.T) {
	f := newFixture(t, nil)
	s := f.signedIn()
	c, fresh, err := f.p.Revalidate(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Groups) != 1 || c.Groups[0] != "old" || len(fresh.Principal.Groups) != 1 || fresh.Principal.Groups[0] != "old" {
		t.Fatalf("groups %v / %v", c.Groups, fresh.Principal.Groups)
	}
}

func TestRevalidateInvalidGrantRevokes(t *testing.T) {
	f := newFixture(t, nil)
	s := f.signedIn()
	f.op.set(func() { f.op.refreshStatus, f.op.refreshError = http.StatusBadRequest, "invalid_grant" })
	if _, _, err := f.p.Revalidate(context.Background(), s); !errors.Is(err, authprovider.ErrRevoked) {
		t.Fatalf("err %v", err)
	}
}

// Without a refresh token the provider cannot be asked again, so the session
// must not be kept alive on trust: a deleted account would stay signed in.
func TestRevalidateWithoutARefreshTokenEndsTheSession(t *testing.T) {
	f := newFixture(t, nil)
	s := f.signedIn()
	s.Refresh = ""
	if _, _, err := f.p.Revalidate(context.Background(), s); !errors.Is(err, authprovider.ErrRevoked) {
		t.Fatalf("err %v, want ErrRevoked", err)
	}
}

func TestRevalidateOutagesAreUnavailableNotRevoked(t *testing.T) {
	cases := map[string]func(*op){
		"server error": func(o *op) {
			o.refreshStatus, o.refreshError = http.StatusServiceUnavailable, "temporarily_unavailable"
		},
		"timeout":     func(o *op) { o.refreshDelay = 3 * time.Second },
		"unreachable": func(o *op) { o.down = true },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			o := newOP(t)
			f := newFixtureFor(t, o, oidcConfig(o), &http.Client{Timeout: 300 * time.Millisecond})
			s := f.signedIn()
			o.set(func() { setup(o) })
			if _, _, err := f.p.Revalidate(context.Background(), s); !errors.Is(err, authprovider.ErrUnavailable) {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestRevalidateBeforeDiscoveryIsUnavailable(t *testing.T) {
	o := newOP(t)
	o.set(func() { o.down = true })
	f := newFixtureFor(t, o, oidcConfig(o), &http.Client{Timeout: time.Second})
	if _, _, err := f.p.Revalidate(context.Background(), liveSession("r")); !errors.Is(err, authprovider.ErrUnavailable) {
		t.Fatalf("err %v", err)
	}
}

func TestLogoutURLCarriesTheHintAndWhereToComeBack(t *testing.T) {
	f := newFixture(t, nil)
	s := f.signedIn()
	got := f.p.LogoutURL(s)
	q := mustQuery(t, got)
	if !strings.HasPrefix(got, opIssuer+"/protocol/openid-connect/logout?") ||
		q.Get("id_token_hint") != s.IDToken || q.Get("client_id") != opClientID ||
		q.Get("post_logout_redirect_uri") != "http://javo.test/" {
		t.Fatalf("logout url %s", got)
	}
}

func TestProviderWithoutEndSessionHasNoLogoutURL(t *testing.T) {
	o := newOP(t)
	o.set(func() { o.noEndSession = true })
	f := newFixtureFor(t, o, oidcConfig(o), &http.Client{Timeout: 2 * time.Second})
	s := f.signedIn()
	if got := f.p.LogoutURL(s); got != "" {
		t.Fatalf("logout url %q", got)
	}
}

// lockedBuffer is a log sink the provider's goroutines and the test can use
// at once.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestLoginAndRefreshLogsHoldNoTokens(t *testing.T) {
	var buf lockedBuffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	o := newOP(t)
	o.set(func() { o.refreshGroups = []string{"g"} })
	f := newFixtureFor(t, o, oidcConfig(o), &http.Client{Timeout: 2 * time.Second})
	s := f.signedIn()
	if _, _, err := f.p.Revalidate(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	// A failed login and a failed refresh log too.
	f.get("/auth/oidc/callback?code=x&state=y")
	o.set(func() { o.refreshStatus, o.refreshError = http.StatusBadRequest, "invalid_grant" })
	_, _, _ = f.p.Revalidate(context.Background(), s)

	if len(o.issued) == 0 {
		t.Fatal("test provider issued nothing")
	}
	for _, secret := range o.issued {
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("a token or code is in the log: %.12s...", secret)
		}
	}
}

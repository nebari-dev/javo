package authn_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestMeShowsGroupsAndRolesFromConfig(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	c := f.LoginAlice()
	me := apitest.Decode[struct {
		ID, Username  string
		Groups, Roles []string
	}](t, f.Do("GET", "/api/me", "", c))
	if me.ID != local.UserID("alice") || me.Username != "alice" {
		t.Fatalf("me %+v", me)
	}
	if len(me.Groups) != 1 || me.Groups[0] != "javo-admins" {
		t.Fatalf("groups %v", me.Groups)
	}
	sort.Strings(me.Roles)
	if len(me.Roles) != 2 || me.Roles[0] != "member" || me.Roles[1] != "platform-admin" {
		t.Fatalf("roles %v", me.Roles)
	}
}

func TestLoginCreatesThePersonTheSessionNames(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	f.LoginAlice()
	p, err := f.People.ByName(context.Background(), local.UserID("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Subjects) != 1 || p.Subjects[0].Issuer != "local" || p.Subjects[0].Subject != "alice" {
		t.Fatalf("subjects %+v", p.Subjects)
	}
	if len(p.Groups) != 1 || p.Groups[0] != "javo-admins" {
		t.Fatalf("groups %+v", p.Groups)
	}
}

func TestOptionsListLocalOnlyWhenEnabled(t *testing.T) {
	t.Parallel()
	on := apitest.New(t, apitest.Options{})
	w := on.Do("GET", "/api/auth/options", "", nil) // public: no cookie
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	got := apitest.Decode[[]authprovider.LoginOption](t, w)
	if len(got) != 1 || got[0].ID != "local" || got[0].Kind != "password" {
		t.Fatalf("options %+v", got)
	}

	off := apitest.New(t, apitest.Options{NoLocal: true})
	if got := apitest.Decode[[]authprovider.LoginOption](t, off.Do("GET", "/api/auth/options", "", nil)); len(got) != 0 {
		t.Fatalf("options with local disabled: %+v", got)
	}
	if w := off.Do("POST", "/api/auth/local/login", `{"username":"alice","password":"alice-password"}`, nil); w.Code != http.StatusNotFound {
		t.Fatalf("login with local disabled: %d", w.Code)
	}
}

func TestDeletedLocalAccountEndsSessionAtNextRevalidation(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	c := f.LoginAlice()
	if w := f.Do("GET", "/api/me", "", c); w.Code != http.StatusOK {
		t.Fatalf("before revalidation: %d", w.Code)
	}
	users := &local.DBUsers{Accounts: f.Db.LocalAccounts()}
	if err := users.Remove(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	// Until RevalidateAt passes the session is still good.
	if w := f.Do("GET", "/api/me", "", c); w.Code != http.StatusOK {
		t.Fatalf("before RevalidateAt: %d", w.Code)
	}
	f.ExpireRevalidation(c)
	w := f.Do("GET", "/api/me", "", c)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("after revalidation: %d", w.Code)
	}
	if p := apitest.Decode[apitest.Problem](t, w); p.Title != "Your session ended. Sign in again." || p.Title != reasons.Msg(reasons.MsgSessionEnded) {
		t.Fatalf("title %q", p.Title)
	}
	if _, err := f.Store.Get(context.Background(), c.Value); err == nil {
		t.Fatal("revoked session still stored")
	}
}

func TestRevalidationPicksUpChangedGroups(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	c := f.LoginAlice()
	users := &local.DBUsers{Accounts: f.Db.LocalAccounts()}
	h, _ := local.HashPassword("alice-password")
	if err := users.Add(context.Background(), "alice", h, []string{"javo-viewers"}, true); err != nil {
		t.Fatal(err)
	}
	f.ExpireRevalidation(c)
	me := apitest.Decode[struct{ Groups, Roles []string }](t, f.Do("GET", "/api/me", "", c))
	if len(me.Groups) != 1 || me.Groups[0] != "javo-viewers" || len(me.Roles) != 1 || me.Roles[0] != "member" {
		t.Fatalf("me %+v", me)
	}
}

func TestUnreachableProviderKeepsSessionUntilGraceEnds(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	clock := now
	f := apitest.New(t, apitest.Options{Extra: []authprovider.Provider{apitest.StubProvider{Err: authprovider.ErrUnavailable}}, Now: func() time.Time { return clock }})
	tok, err := f.Store.Create(context.Background(), session.Session{
		Principal: apitest.PrincipalFor("bob-1"), Provider: "stub", Kind: session.KindUI,
		Expires: now.Add(24 * time.Hour), RevalidateAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	c := f.Cookie(tok)

	w := f.Do("GET", "/api/me", "", c)
	if w.Code != http.StatusOK {
		t.Fatalf("inside grace: %d", w.Code)
	}
	want := now.Add(-time.Minute).Add(15 * time.Minute).Format(time.RFC3339)
	if got := w.Header().Get(authn.IdpUnreachableHeader); got != want {
		t.Fatalf("%s = %q, want %q", authn.IdpUnreachableHeader, got, want)
	}
	wantText := reasons.Msg(reasons.MsgIdpOutageBanner, "12:14 UTC")
	if got := w.Header().Get(authn.IdpUnreachableMessageHeader); got != wantText {
		t.Fatalf("%s = %q, want %q", authn.IdpUnreachableMessageHeader, got, wantText)
	}

	clock = now.Add(15 * time.Minute) // RevalidateAt + grace has passed
	if w := f.Do("GET", "/api/me", "", c); w.Code != http.StatusUnauthorized {
		t.Fatalf("after grace: %d", w.Code)
	}
}

func TestSessionOfASwitchedOffProviderEnds(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	tok, err := f.Store.Create(context.Background(), session.Session{
		Principal: apitest.PrincipalFor("bob-1"), Provider: "gone", Kind: session.KindUI,
		RevalidateAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := f.Do("GET", "/api/me", "", f.Cookie(tok)); w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", w.Code)
	}
}

func TestSessionWithoutRevalidateAtIsCheckedAndEndsForDeletedAccount(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	c := f.LoginAlice()
	ctx := context.Background()
	s, err := f.Store.Get(ctx, c.Value)
	if err != nil {
		t.Fatal(err)
	}
	s.RevalidateAt = time.Time{}
	if err := f.Store.Update(ctx, c.Value, s); err != nil {
		t.Fatal(err)
	}
	users := &local.DBUsers{Accounts: f.Db.LocalAccounts()}
	if err := users.Remove(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if w := f.Do("GET", "/api/me", "", c); w.Code != http.StatusUnauthorized {
		t.Fatalf("deleted account with zero RevalidateAt: %d", w.Code)
	}
}

func TestLoginAndRevalidationLogsHoldNoSecrets(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f := apitest.New(t, apitest.Options{})
	c := f.LoginAlice()
	f.Do("POST", "/api/auth/local/login", `{"username":"alice","password":"wrong-password-xyz"}`, nil)
	f.ExpireRevalidation(c)
	if w := f.Do("GET", "/api/me", "", c); w.Code != http.StatusOK {
		t.Fatalf("revalidation: %d", w.Code)
	}
	for _, secret := range []string{"alice-password", "wrong-password-xyz", c.Value} {
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("log output contains a secret %q:\n%s", secret, buf.String())
		}
	}
}

func TestReasonCopyIsPublicAndComesFromTheCatalog(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	w := f.Do("GET", "/api/reasons?code=SignInLinkExpired", "", nil) // no cookie
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	want, _ := reasons.Lookup(reasons.SignInLinkExpired)
	got := apitest.Decode[struct{ Title, What, Todo string }](t, w)
	if got.Title != want.Title || got.What != want.What || got.Todo != want.Todo {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if w := f.Do("GET", "/api/reasons?code=NoSuchReason", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown code: %d", w.Code)
	}
}

// Over TLS the session cookie carries the __Host- prefix, so the browser
// refuses one of that name from a sibling host; a plain-named cookie, which a
// sibling host can set for the parent domain, is never read.
func TestSecureSessionCookieIsHostOnly(t *testing.T) {
	f := apitest.New(t, apitest.Options{Secure: true})
	w := f.Do("POST", "/api/auth/local/login", `{"username":"alice","password":"alice-password"}`, nil)
	var c *http.Cookie
	for _, k := range w.Result().Cookies() {
		if k.Name == "__Host-javo_session" {
			c = k
		}
	}
	if c == nil || !c.Secure || c.Path != "/" || c.Domain != "" {
		t.Fatalf("session cookie %+v in %v", c, w.Result().Cookies())
	}
	if w := f.Do("GET", "/api/me", "", c); w.Code != http.StatusOK {
		t.Fatalf("host cookie: %d", w.Code)
	}
	if w := f.Do("GET", "/api/me", "", &http.Cookie{Name: "javo_session", Value: c.Value}); w.Code != http.StatusUnauthorized {
		t.Fatalf("plain-named cookie: %d", w.Code)
	}
}

// A browser drops a __Host- cookie that is not Secure, so over plain http
// the session cookie keeps its plain name.
func TestPlainHTTPSessionCookieKeepsItsName(t *testing.T) {
	f := apitest.New(t, apitest.Options{Urls: config.URLs{Scheme: "http"}})
	w := f.Do("POST", "/api/auth/local/login", `{"username":"alice","password":"alice-password"}`, nil)
	cs := w.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != "javo_session" || cs[0].Secure {
		t.Fatalf("cookies %v", cs)
	}
	if w := f.Do("GET", "/api/me", "", cs[0]); w.Code != http.StatusOK {
		t.Fatalf("me: %d", w.Code)
	}
}

func TestAppAndTicketSessionsCannotUseTheAPI(t *testing.T) {
	f := apitest.New(t, apitest.Options{})
	ui := f.LoginAlice()
	for _, kind := range []string{session.KindApp, session.KindTicket} {
		tok, err := f.Store.Create(context.Background(), session.Session{
			Principal: apitest.PrincipalFor(local.UserID("alice")), Provider: "local", Kind: kind,
			Parent: session.ID(ui.Value), Workspace: "javo-apps/sales", Expires: time.Now().Add(time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		if w := f.Do("GET", "/api/me", "", &http.Cookie{Name: ui.Name, Value: tok}); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s session: %d", kind, w.Code)
		}
	}
}

// The plan decision routes are recognised in any spelling the mux could
// route to them, as the token routes are.
func TestIsPlanDecisionCleansThePath(t *testing.T) {
	for _, p := range []string{"/api/plans/x/approve", "/api/plans/x/reject", "/api/plans/x/./approve", "/api//plans/x/approve", "/api/plans/y/../x/reject", "/api/plans/x/approve/"} {
		if !authn.IsPlanDecision(p) {
			t.Errorf("%s is a plan decision", p)
		}
	}
	for _, p := range []string{"/api/plans", "/api/plans/x", "/api/plans/x/approvex", "/api/plans/x/approve/more"} {
		if authn.IsPlanDecision(p) {
			t.Errorf("%s is not a plan decision", p)
		}
	}
}

func TestIsTrustChangeCleansThePath(t *testing.T) {
	for _, p := range []string{"/api/workspaces/pi/trust", "/api/workspaces//pi/trust", "/api/workspaces/x/../pi/trust", "api/workspaces/pi/trust/"} {
		if !authn.IsTrustChange(p) {
			t.Errorf("%q is not a trust change", p)
		}
	}
	for _, p := range []string{"/api/workspaces/trust", "/api/workspaces/pi/trusted", "/api/workspaces/pi/trust/x", "/api/jobs/pi/trust"} {
		if authn.IsTrustChange(p) {
			t.Errorf("%q is a trust change", p)
		}
	}
}

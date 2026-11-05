package authn_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/buildinfo"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestTokenShownOnce(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	c := f.LoginAlice()
	created := f.NewToken(c, `{"name":"laptop","scope":"read"}`)
	if !strings.HasPrefix(created.Token, token.Prefix) || !strings.HasPrefix(created.Token, created.Prefix) || created.Name != "laptop" || created.Scope != "read" {
		t.Fatalf("created %+v", created)
	}
	w := f.Do("GET", "/api/tokens", "", c)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), created.Token) {
		t.Fatalf("list shows the token: %s", w.Body)
	}
	list := apitest.Decode[[]apitest.TokenJSON](t, w)
	if len(list) != 1 || list[0].ID != created.ID || list[0].Prefix != created.Prefix || list[0].Token != "" {
		t.Fatalf("list %+v", list)
	}
	// The row keeps the hash of the token, never the token.
	row, err := f.Db.Tokens().Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, stored := range []string{row.ID, row.Name, row.Hash, row.Prefix} {
		if strings.Contains(stored, created.Token[len(created.Prefix):]) {
			t.Fatalf("the row holds the token: %q", stored)
		}
	}
}

func TestBearerTokenActsAsItsOwner(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	tok := f.NewToken(f.LoginAlice(), `{"name":"ci","scope":"read"}`).Token
	w := f.Bearer("GET", "/api/me", "", tok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("me: %d %s", w.Code, w.Body)
	}
	me := apitest.Decode[struct {
		ID     string
		Groups []string
		Roles  []string
	}](t, w)
	// The groups are the ones recorded at alice's last sign-in.
	if me.ID != apitest.LocalAlice() || len(me.Groups) != 1 || me.Groups[0] != "javo-admins" {
		t.Fatalf("me %+v", me)
	}
}

func TestWriteTokenActsAsOwner(t *testing.T) {
	t.Parallel()
	bobs := gatewaytest.Workspace("sales", "bob-5e6f7a8b", "Sales")
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), bobs)
	tok := f.NewToken(f.LoginAlice(), `{"name":"ci","scope":"read-write"}`).Token
	viaSession := f.Do("POST", "/api/workspaces/sales/stop", "", f.Session(apitest.LocalAlice()))
	viaToken := f.Bearer("POST", "/api/workspaces/sales/stop", "", tok, nil)
	if viaToken.Code != http.StatusNotFound || viaToken.Code != viaSession.Code || viaToken.Body.String() != viaSession.Body.String() {
		t.Fatalf("token %d %s, session %d %s", viaToken.Code, viaToken.Body, viaSession.Code, viaSession.Body)
	}
	if w := f.Bearer("POST", "/api/workspaces", `{"displayName":"Mine","template":"streamlit","file":"app.py"}`, tok, nil); w.Code != http.StatusCreated {
		t.Fatalf("create with a write token: %d %s", w.Code, w.Body)
	}
	if w := f.Bearer("DELETE", "/api/workspaces/mine", "", tok, nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete with a write token: %d %s", w.Code, w.Body)
	}
}

func TestReadTokenCannotWrite(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"))
	tok := f.NewToken(f.LoginAlice(), `{"name":"ci","scope":"read"}`).Token
	w := f.Bearer("POST", "/api/workspaces", `{"displayName":"Mine","template":"streamlit","file":"app.py"}`, tok, nil)
	if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgTokenReadOnly) {
		t.Fatalf("POST with a read token: %d %s", w.Code, w.Body)
	}
	if got := w.Header().Get("WWW-Authenticate"); !strings.Contains(got, `error="insufficient_scope"`) {
		t.Fatalf("WWW-Authenticate %q", got)
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("a read token created an app")
	}
	if w := f.Bearer("GET", "/api/workspaces", "", tok, nil); w.Code != http.StatusOK {
		t.Fatalf("GET with a read token: %d %s", w.Code, w.Body)
	}
}

func TestTokenCannotManageTokens(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	c := f.LoginAlice()
	tok := f.NewToken(c, `{"name":"ci","scope":"read-write"}`)
	for _, req := range []struct{ method, path, body string }{
		{"GET", "/api/tokens", ""},
		{"POST", "/api/tokens", `{"name":"more","scope":"read-write"}`},
		{"DELETE", "/api/tokens/" + tok.ID, ""},
	} {
		// The cookie is ignored: a bearer request is the token alone.
		w := f.Bearer(req.method, req.path, req.body, tok.Token, c)
		if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgTokenCannotManageTokens) {
			t.Fatalf("%s %s: %d %s", req.method, req.path, w.Code, w.Body)
		}
	}
	if list := apitest.Decode[[]apitest.TokenJSON](t, f.Do("GET", "/api/tokens", "", c)); len(list) != 1 {
		t.Fatalf("tokens %+v", list)
	}
}

func TestRevokedTokenFailsAtOnce(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	c := f.LoginAlice()
	tok := f.NewToken(c, `{"name":"ci","scope":"read"}`)
	if w := f.Bearer("GET", "/api/me", "", tok.Token, nil); w.Code != http.StatusOK {
		t.Fatalf("before revoke: %d", w.Code)
	}
	if w := f.Do("DELETE", "/api/tokens/"+tok.ID, "", c); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	w := f.Bearer("GET", "/api/me", "", tok.Token, nil)
	if w.Code != http.StatusUnauthorized || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgTokenInvalid) {
		t.Fatalf("after revoke: %d %s", w.Code, w.Body)
	}
	if got := w.Header().Get("WWW-Authenticate"); !strings.Contains(got, `error="invalid_token"`) {
		t.Fatalf("WWW-Authenticate %q", got)
	}
	if w := f.Do("DELETE", "/api/tokens/"+tok.ID, "", c); w.Code != http.StatusNotFound {
		t.Fatalf("second revoke: %d", w.Code)
	}
}

func TestOthersCannotRevokeYourToken(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	tok := f.NewToken(f.LoginAlice(), `{"name":"ci","scope":"read"}`)
	if w := f.Do("DELETE", "/api/tokens/"+tok.ID, "", f.Session("bob-5e6f7a8b")); w.Code != http.StatusNotFound {
		t.Fatalf("bob revoking alice's token: %d", w.Code)
	}
	if w := f.Bearer("GET", "/api/me", "", tok.Token, nil); w.Code != http.StatusOK {
		t.Fatalf("after bob's attempt: %d", w.Code)
	}
}

func TestExpiredToken(t *testing.T) {
	t.Parallel()
	now := time.Now()
	f := apitest.New(t, apitest.Options{Now: func() time.Time { return now }})
	tok := f.NewToken(f.LoginAlice(), `{"name":"ci","scope":"read","expiresInDays":1}`)
	if tok.ExpiresAt == nil || tok.ExpiresAt.Sub(now) < 23*time.Hour {
		t.Fatalf("expiresAt %v", tok.ExpiresAt)
	}
	if w := f.Bearer("GET", "/api/me", "", tok.Token, nil); w.Code != http.StatusOK {
		t.Fatalf("before expiry: %d", w.Code)
	}
	now = now.Add(25 * time.Hour)
	if w := f.Bearer("GET", "/api/me", "", tok.Token, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("after expiry: %d", w.Code)
	}
}

func TestStaleGroupsRefused(t *testing.T) {
	t.Parallel()
	now := time.Now()
	f := apitest.New(t, apitest.Options{Now: func() time.Time { return now }, GroupsMaxAge: 30 * 24 * time.Hour})
	tok := f.NewToken(f.LoginAlice(), `{"name":"ci","scope":"read"}`)
	now = now.Add(29 * 24 * time.Hour)
	if w := f.Bearer("GET", "/api/me", "", tok.Token, nil); w.Code != http.StatusOK {
		t.Fatalf("29 days after sign-in: %d", w.Code)
	}
	now = now.Add(2 * 24 * time.Hour)
	w := f.Bearer("GET", "/api/me", "", tok.Token, nil)
	if w.Code != http.StatusUnauthorized || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgTokenSignInAgain) {
		t.Fatalf("31 days after sign-in: %d %s", w.Code, w.Body)
	}
	// Signing in again records the time of that sign-in (the real clock, which
	// the people table uses) and the token works again.
	f.LoginAlice()
	now = time.Now().Add(time.Minute)
	if w := f.Bearer("GET", "/api/me", "", tok.Token, nil); w.Code != http.StatusOK {
		t.Fatalf("after signing in again: %d %s", w.Code, w.Body)
	}
}

func TestBearerIgnoresCookie(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	c := f.LoginAlice()
	// A bad token with a good cookie is refused: the cookie is not consulted.
	if w := f.Bearer("GET", "/api/me", "", "javo_nope", c); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad bearer with a good cookie: %d", w.Code)
	}
	// Logout with a bearer header leaves the cookie's session alone.
	tok := f.NewToken(c, `{"name":"ci","scope":"read-write"}`).Token
	if w := f.Bearer("POST", "/api/auth/logout", "", tok, c); w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := f.Do("GET", "/api/me", "", c); w.Code != http.StatusOK {
		t.Fatalf("session after a bearer logout: %d", w.Code)
	}
}

func TestBearerSkipsTheCookieCSRFCheckOnly(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"))
	c := f.LoginAlice()
	tok := f.NewToken(c, `{"name":"ci","scope":"read-write"}`).Token
	body := `{"displayName":"From cli","template":"streamlit","file":"app.py"}`
	// A cookie request from another site is refused...
	if w := f.Do("POST", "/api/workspaces", body, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "/api/workspaces", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site cookie request: %d", w.Code)
	}
	// ...while a token, which a browser never attaches by itself, is not.
	if w := f.Bearer("POST", "/api/workspaces", body, tok, nil, "Sec-Fetch-Site", "cross-site", "Origin", "https://elsewhere.example.com"); w.Code != http.StatusCreated {
		t.Fatalf("cross-site bearer request: %d %s", w.Code, w.Body)
	}
	// A bad token is still refused there.
	if w := f.Bearer("POST", "/api/workspaces", body, "javo_nope", nil, "Sec-Fetch-Site", "cross-site"); w.Code != http.StatusUnauthorized {
		t.Fatalf("cross-site bad bearer: %d", w.Code)
	}
}

func TestCreateTokenChecksItsFields(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{MaxLifetime: 90 * 24 * time.Hour})
	c := f.LoginAlice()
	for _, tc := range []struct {
		body, field string
		msg         reasons.MessageCode
	}{
		{`{"name":"","scope":"read","expiresInDays":30}`, "name", reasons.MsgTokenNameInvalid},
		{`{"name":"` + strings.Repeat("x", 65) + `","scope":"read","expiresInDays":30}`, "name", reasons.MsgTokenNameInvalid},
		{`{"name":"ci","scope":"root","expiresInDays":30}`, "scope", reasons.MsgTokenScopeInvalid},
		{`{"name":"ci","scope":"read","expiresInDays":0}`, "expiresInDays", reasons.MsgTokenExpiryInvalid},
		{`{"name":"ci","scope":"read","expiresInDays":91}`, "expiresInDays", reasons.MsgTokenExpiryTooLong},
		{`{"name":"ci","scope":"read"}`, "expiresInDays", reasons.MsgTokenExpiryTooLong},
	} {
		w := f.Do("POST", "/api/tokens", tc.body, c)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body)
		}
		msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body." + tc.field)
		if !ok || !strings.HasPrefix(msg, strings.SplitN(reasons.Msg(tc.msg), "%", 2)[0]) {
			t.Fatalf("%s: errors %s", tc.body, w.Body)
		}
	}
	if list := apitest.Decode[[]apitest.TokenJSON](t, f.Do("GET", "/api/tokens", "", c)); len(list) != 0 {
		t.Fatalf("tokens %+v", list)
	}
}

func TestTokenNotAcceptedOnAppPaths(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("sales", apitest.LocalAlice(), "Sales")
	ws.Status.Phase = "Running"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Saw-Authorization", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), ws)
	f.MountProxy(backend.URL)
	c := f.LoginAlice()
	tok := f.NewToken(c, `{"name":"ci","scope":"read-write"}`).Token
	path := "/u/" + apitest.LocalAlice() + "/sales/"
	// A token alone does not open an app: the browser is sent to sign in.
	if w := f.Bearer("GET", path, "", tok, nil); w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/login") {
		t.Fatalf("token on an app path: %d %v", w.Code, w.Header())
	}
	// Signed in with a cookie, the app opens, and the token is never passed to it.
	w := f.Bearer("GET", path, "", tok, c)
	if w.Code != http.StatusOK {
		t.Fatalf("cookie on an app path: %d %s", w.Code, w.Body)
	}
	if got := w.Header().Get("X-Saw-Authorization"); got != "" {
		t.Fatalf("the app saw Authorization %q", got)
	}
}

func TestDeletingAnAppRevokesItsTokens(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("agent", apitest.LocalAlice(), "Agent")
	ws.Template = "agent"
	f := apitest.New(t, apitest.Options{}, apitest.AgentTemplate(), ws)
	c := f.LoginAlice()
	text, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{Name: "agent", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: ws.Namespace, Name: ws.ObjectName(), Short: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	if w := f.Bearer("GET", "/api/me", "", text, nil); w.Code != http.StatusOK {
		t.Fatalf("app token: %d %s", w.Code, w.Body)
	}
	list := apitest.Decode[[]apitest.TokenJSON](t, f.Do("GET", "/api/tokens", "", c))
	if len(list) != 1 || list[0].App != "agent" {
		t.Fatalf("tokens %+v", list)
	}
	if w := f.Do("DELETE", "/api/workspaces/agent", "", c); w.Code != http.StatusNoContent {
		t.Fatalf("delete app: %d %s", w.Code, w.Body)
	}
	if w := f.Bearer("GET", "/api/me", "", text, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("app token after its app: %d", w.Code)
	}
	if list := apitest.Decode[[]apitest.TokenJSON](t, f.Do("GET", "/api/tokens", "", c)); len(list) != 0 {
		t.Fatalf("tokens after the app went %+v", list)
	}
}

func TestAppTokenStopsWhenItsAppIsGone(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("agent", apitest.LocalAlice(), "Agent")
	ws.Template = "agent"
	f := apitest.New(t, apitest.Options{}, apitest.AgentTemplate(), ws)
	f.LoginAlice()
	text, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{Name: "agent", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: ws.Namespace, Name: ws.ObjectName(), Short: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	if w := f.Bearer("GET", "/api/me", "", text, nil); w.Code != http.StatusOK {
		t.Fatalf("token of a live app: %d", w.Code)
	}
	// The controller has not removed the app's objects yet; the token is already dead.
	app, err := f.Db.Apps().GetByObjectName(context.Background(), ws.ObjectName())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Db.Apps().MarkDeleted(context.Background(), model.Actor{Person: apitest.LocalAlice()}, app.ID); err != nil {
		t.Fatal(err)
	}
	if w := f.Bearer("GET", "/api/me", "", text, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("token of a deleted app: %d", w.Code)
	}
}

func TestRevokingAnAppTokenAsksForANewOne(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("agent", apitest.LocalAlice(), "Agent")
	f := apitest.New(t, apitest.Options{}, ws)
	c := f.LoginAlice()
	_, created, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{Name: "agent", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: ws.Namespace, Name: ws.ObjectName(), Short: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	if w := f.Do("DELETE", "/api/tokens/"+created.ID, "", c); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	got, err := f.Db.Apps().GetByObjectName(context.Background(), ws.ObjectName())
	if err != nil {
		t.Fatal(err)
	}
	if got.TokenRevoked != created.ID {
		t.Fatalf("app records revoked token %q, want %q", got.TokenRevoked, created.ID)
	}
}

func TestInfoSaysWhetherAPIDocsAreOn(t *testing.T) {
	t.Parallel()
	for _, on := range []bool{false, true} {
		f := apitest.New(t, apitest.Options{ApiDocs: on})
		got := apitest.Decode[struct{ APIDocs bool }](t, f.Do("GET", "/api/info", "", f.LoginAlice()))
		if got.APIDocs != on {
			t.Fatalf("docs %v: info %+v", on, got)
		}
	}
}

func TestInfoSaysWhichBuildIsRunning(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	got := apitest.Decode[struct{ Version, Commit string }](t, f.Do("GET", "/api/info", "", f.LoginAlice()))
	want := buildinfo.Get()
	if got.Version != want.Version || got.Commit != want.Commit || got.Version == "" {
		t.Fatalf("info %+v, want %+v", got, want)
	}
}

func TestCreatingAndRevokingATokenIsAudited(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	c := f.LoginAlice()
	created := f.NewToken(c, `{"name":"laptop","scope":"read"}`)
	if w := f.Do("DELETE", "/api/tokens/"+created.ID, "", c); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	events, err := f.Db.Audit().List(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range events {
		if e.ObjectKind == "token" && e.ObjectID == created.ID && e.Actor == apitest.LocalAlice() && e.Via == "session" {
			seen[e.Action] = true
		}
	}
	if !seen["token.create"] || !seen["token.revoke"] {
		t.Fatalf("audit rows for the token: %v", seen)
	}
}

// A token acts with the groups the provider last confirmed: when a browser
// session revalidates and the provider no longer lists a group, the person's
// tokens lose it too, without waiting for a new sign-in.
func TestTokenLosesTheGroupsARevalidationDrops(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	c := f.LoginAlice()
	tok := f.NewToken(c, `{"name":"ci","scope":"read"}`).Token
	groups := func() []string {
		t.Helper()
		w := f.Bearer("GET", "/api/me", "", tok, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("token: %d %s", w.Code, w.Body)
		}
		return apitest.Decode[struct{ Groups []string }](t, w).Groups
	}
	if got := groups(); len(got) != 1 || got[0] != "javo-admins" {
		t.Fatalf("before: %v", got)
	}
	ctx := context.Background()
	acct, err := f.Db.LocalAccounts().Get(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	acct.Groups = nil
	if err := f.Db.LocalAccounts().Put(ctx, model.Actor{System: model.SystemCLI}, acct); err != nil {
		t.Fatal(err)
	}
	f.ExpireRevalidation(c)
	if w := f.Do("GET", "/api/me", "", c); w.Code != http.StatusOK {
		t.Fatalf("revalidate: %d %s", w.Code, w.Body)
	}
	if got := groups(); len(got) != 0 {
		t.Fatalf("after the provider dropped the group: %v", got)
	}
	if w := f.Bearer("GET", "/api/admin/report", "", tok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("admin route with the token: %d", w.Code)
	}
}

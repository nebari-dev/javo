package apps_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestCreateWithAPIAccess(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("lab", "jupyterlab"), apitest.AgentTemplate())
	alice := f.Session("alice-aaaaaaaa")
	cases := []struct{ name, body, want string }{
		{"asked", `{"displayName":"Reader","template":"lab","apiAccess":"Read"}`, model.APIAccessRead},
		{"plain default", `{"displayName":"Plain","template":"lab"}`, model.APIAccessNone},
		{"agent default", `{"displayName":"Agent","template":"agent"}`, model.APIAccessReadWrite},
		{"agent off", `{"displayName":"Quiet","template":"agent","apiAccess":"None"}`, model.APIAccessNone},
	}
	for _, c := range cases {
		w := f.Do("POST", "/api/workspaces", c.body, alice)
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: status %d: %s", c.name, w.Code, w.Body)
		}
		if got := apitest.Decode[apitest.AccessJSON](t, w).APIAccess; got != c.want {
			t.Fatalf("%s: apiAccess %q, want %q", c.name, got, c.want)
		}
	}
	if got := f.GetWorkspace("alice-aaaaaaaa", "reader").APIAccess; got != model.APIAccessRead {
		t.Fatalf("stored apiAccess %q", got)
	}
}

func TestCreateRefusesAPIAccessAboveTheTemplateCap(t *testing.T) {
	t.Parallel()
	capped := gatewaytest.Template("lab", "jupyterlab")
	capped.Spec.MaxAPIAccess = model.APIAccessRead
	f := apitest.New(t, apitest.Options{}, capped)
	alice := f.Session("alice-aaaaaaaa")
	for _, access := range []string{"ReadWrite", "Admin"} {
		w := f.Do("POST", "/api/workspaces", `{"displayName":"Lab","template":"lab","apiAccess":"`+access+`"}`, alice)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status %d: %s", access, w.Code, w.Body)
		}
		if _, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.apiAccess"); !ok {
			t.Fatalf("%s: no apiAccess field error: %s", access, w.Body)
		}
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("created")
	}
}

func TestChangingAPIAccess(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("agent", "alice-aaaaaaaa", "Agent")
	ws.Template = "agent"
	capped := apitest.AgentTemplate()
	capped.Spec.MaxAPIAccess = model.APIAccessRead
	f := apitest.New(t, apitest.Options{}, capped, ws)
	alice := f.Session("alice-aaaaaaaa")
	w := f.Do("PUT", "/api/workspaces/agent/api-access", `{"apiAccess":"None"}`, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := apitest.Decode[apitest.AccessJSON](t, w).APIAccess; got != model.APIAccessNone {
		t.Fatalf("view apiAccess %q", got)
	}
	if got := f.GetWorkspace("alice-aaaaaaaa", "agent").APIAccess; got != model.APIAccessNone {
		t.Fatalf("stored apiAccess %q", got)
	}
	w = f.Do("PUT", "/api/workspaces/agent/api-access", `{"apiAccess":"ReadWrite"}`, alice)
	if msg, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.apiAccess"); w.Code != http.StatusUnprocessableEntity || msg == "" {
		t.Fatalf("above the cap: status %d: %s", w.Code, w.Body)
	}
	// Another person gets the same answer as for a missing app.
	if w := f.Do("PUT", "/api/workspaces/agent/api-access?owner=alice-aaaaaaaa", `{"apiAccess":"Read"}`, f.Session("bob-bbbbbbbb")); w.Code != http.StatusNotFound {
		t.Fatalf("bob: status %d: %s", w.Code, w.Body)
	}
}

// A token of the scope the app no longer has stops working at once and is
// gone from Settings; a token of the new scope keeps working.
func TestChangingAPIAccessEndsTheOldToken(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("agent", apitest.LocalAlice(), "Agent")
	ws.Template = "agent"
	f := apitest.New(t, apitest.Options{}, apitest.AgentTemplate(), ws)
	c := f.LoginAlice()
	app := &token.App{Namespace: ws.Namespace, Name: ws.ObjectName(), Short: "agent"}
	old, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{Name: "agent", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite, App: app})
	if err != nil {
		t.Fatal(err)
	}
	if w := f.Bearer("GET", "/api/me", "", old, nil); w.Code != http.StatusOK {
		t.Fatalf("before: %d %s", w.Code, w.Body)
	}
	if w := f.Do("PUT", "/api/workspaces/agent/api-access", `{"apiAccess":"Read"}`, c); w.Code != http.StatusOK {
		t.Fatalf("change: %d %s", w.Code, w.Body)
	}
	if w := f.Bearer("GET", "/api/me", "", old, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("old read-write token: %d", w.Code)
	}
	fresh, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{Name: "agent", Owner: apitest.LocalAlice(), Scope: token.ScopeRead, App: app})
	if err != nil {
		t.Fatal(err)
	}
	if w := f.Bearer("GET", "/api/me", "", fresh, nil); w.Code != http.StatusOK {
		t.Fatalf("new read token: %d %s", w.Code, w.Body)
	}
	list := apitest.Decode[[]apitest.TokenJSON](t, f.Do("GET", "/api/tokens", "", c))
	if len(list) != 1 || list[0].Scope != string(token.ScopeRead) {
		t.Fatalf("tokens %+v", list)
	}
}

// An app token is refused when its scope is not the app's access, however
// the app was changed (for example with kubectl).
func TestAppTokenOfAnotherScopeIsRefused(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("lab", apitest.LocalAlice(), "Lab")
	ws.Template = "lab"
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("lab", "jupyterlab"), ws)
	f.LoginAlice()
	text, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{Name: "lab", Owner: apitest.LocalAlice(), Scope: token.ScopeRead,
		App: &token.App{Namespace: ws.Namespace, Name: ws.ObjectName(), Short: "lab"}})
	if err != nil {
		t.Fatal(err)
	}
	if w := f.Bearer("GET", "/api/me", "", text, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("token of an app without API access: %d", w.Code)
	}
}

func TestTemplatesSayTheirAPIAccess(t *testing.T) {
	t.Parallel()
	capped := gatewaytest.Template("lab", "jupyterlab")
	capped.Spec.MaxAPIAccess = model.APIAccessRead
	f := apitest.New(t, apitest.Options{}, capped, apitest.AgentTemplate())
	type tplJSON struct{ Name, APIAccessDefault, MaxAPIAccess string }
	var list struct{ Items []tplJSON }
	list = apitest.Decode[struct{ Items []tplJSON }](t, f.Do("GET", "/api/templates", "", f.Session("alice-aaaaaaaa")))
	got := map[string]tplJSON{}
	for _, x := range list.Items {
		got[x.Name] = x
	}
	if a := got["agent"]; a.APIAccessDefault != "ReadWrite" || a.MaxAPIAccess != "ReadWrite" {
		t.Fatalf("agent %+v", a)
	}
	if l := got["lab"]; l.APIAccessDefault != "None" || l.MaxAPIAccess != "Read" {
		t.Fatalf("lab %+v", l)
	}
}

// The Sharing tab says that an app with API access acts as its owner.
func TestSharingSaysTheAppActsAsItsOwner(t *testing.T) {
	t.Parallel()
	agent := gatewaytest.Workspace("agent", "alice-aaaaaaaa", "Agent")
	agent.Template = "agent"
	plain := gatewaytest.Workspace("lab", "alice-aaaaaaaa", "Lab")
	plain.Template = "lab"
	f := apitest.New(t, apitest.Options{}, apitest.AgentTemplate(), gatewaytest.Template("lab", "jupyterlab"), agent, plain)
	alice := f.Session("alice-aaaaaaaa")
	type view struct{ APIAccess, ActsAsOwner string }
	a := apitest.Decode[view](t, f.Do("GET", "/api/workspaces/agent/sharing", "", alice))
	if a.APIAccess != "ReadWrite" || a.ActsAsOwner != reasons.Msg(reasons.MsgAPIAccessActsAsOwner) {
		t.Fatalf("agent %+v", a)
	}
	if l := apitest.Decode[view](t, f.Do("GET", "/api/workspaces/lab/sharing", "", alice)); l.APIAccess != "None" || l.ActsAsOwner != "" {
		t.Fatalf("lab %+v", l)
	}
}

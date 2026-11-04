package apps_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

const admin = "root-cccccccc"

func refusedAsHeld(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusForbidden {
		t.Fatalf("%s: %d %s", what, w.Code, w.Body)
	}
	want := reasons.Msg(reasons.MsgSecretsHeldOwnerOnly)
	if got := apitest.Decode[apitest.Problem](t, w).Title; got != want {
		t.Fatalf("%s: title %q", what, got)
	}
}

// refusedRewrite checks w is the refusal of a change of what an app runs by
// anyone but its owner.
func refusedRewrite(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusForbidden {
		t.Fatalf("%s: %d %s", what, w.Code, w.Body)
	}
	if got := apitest.Decode[apitest.Problem](t, w).Title; got != reasons.Msg(reasons.MsgRewriteOwnerOnly) {
		t.Fatalf("%s: title %q", what, got)
	}
}

// A platform admin, or the app's own token, cannot change the command of an
// app that holds its owner's secrets: the new command would see them. Nor
// can an admin change it once it holds none (TestOnlyTheOwnerChangesWhatAnAppRuns).
func TestOnlyTheOwnerChangesWhatRunsNextToSecrets(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("agent", apitest.LocalAlice(), "Agent")
	ws.Template = "agent"
	f := apitest.New(t, apitest.Options{}, apitest.CustomTemplate(), apitest.AgentTemplate(), ws)
	f.WithSecretKeys()
	alice := f.LoginAlice()
	root := f.Session(admin, "javo-admins")
	f.CreateSecret(alice, "API_KEY", apitest.SecretValue)
	f.Do("POST", "/api/workspaces", `{"displayName":"Plain","template":"custom","command":"python a.py"}`, alice)
	if w := f.Do("POST", "/api/workspaces", `{"displayName":"Bot","template":"custom","command":"python a.py","env":[{"name":"K","secret":"API_KEY"}]}`, alice); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	owner := "?owner=" + apitest.LocalAlice()
	w := f.Do("PUT", "/api/workspaces/bot/launch"+owner, `{"command":"sh -c 'echo $K'"}`, root)
	refusedRewrite(t, "admin", w)

	text, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{Name: "agent", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: ws.Namespace, Name: ws.ObjectName(), Short: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	w = f.Bearer("PUT", "/api/workspaces/bot/launch", `{"command":"sh -c 'echo $K'"}`, text, nil)
	if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != apitest.WorkloadRefusal("PUT") {
		t.Fatalf("app token: %d %s", w.Code, w.Body)
	}
	if got := f.GetWorkspace(apitest.LocalAlice(), "bot").Command; got != "python a.py" {
		t.Fatalf("stored %q", got)
	}

	refusedRewrite(t, "admin on an app without secrets", f.Do("PUT", "/api/workspaces/plain/launch"+owner, `{"command":"python b.py"}`, root))
	if w := f.Do("PUT", "/api/workspaces/bot/launch", `{"command":"python b.py"}`, alice); w.Code != http.StatusOK {
		t.Fatalf("owner: %d %s", w.Code, w.Body)
	}
}

// An app's API token acts as its owner, so it counts like a secret: a
// platform admin can neither change the command of an app with API access
// nor give API access to an app whose command they set. Lowering it is fine.
func TestAPIAccessCountsAsASecret(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("agent", apitest.AliceID, "Agent")
	ws.Template = "agent"
	tpl := apitest.AgentTemplate()
	tpl.Spec.UserCommand = model.UserCommandOptional
	f := apitest.New(t, apitest.Options{}, tpl, ws)
	root := f.Session(admin, "javo-admins")
	owner := "?owner=" + apitest.AliceID

	w := f.Do("PUT", "/api/workspaces/agent/launch"+owner, `{"command":"sh -c 'echo $JAVO_TOKEN'"}`, root)
	refusedRewrite(t, "launch with API access", w)
	if w := f.Do("PUT", "/api/workspaces/agent/api-access"+owner, `{"apiAccess":"None"}`, root); w.Code != http.StatusOK {
		t.Fatalf("admin lowers API access: %d %s", w.Code, w.Body)
	}
	refusedRewrite(t, "launch without API access", f.Do("PUT", "/api/workspaces/agent/launch"+owner, `{"command":"sh -c 'echo $JAVO_TOKEN'"}`, root))
	w = f.Do("PUT", "/api/workspaces/agent/api-access"+owner, `{"apiAccess":"ReadWrite"}`, root)
	refusedAsHeld(t, "admin raises API access", w)
	if got := f.GetWorkspace(apitest.AliceID, "agent").APIAccess; got != model.APIAccessNone {
		t.Fatalf("stored %q", got)
	}
	if w := f.Do("PUT", "/api/workspaces/agent/api-access", `{"apiAccess":"ReadWrite"}`, f.Session(apitest.AliceID)); w.Code != http.StatusOK {
		t.Fatalf("owner raises API access: %d %s", w.Code, w.Body)
	}
}

// An app whose template gives it its owner's Nebi token holds a credential
// too, so a platform admin cannot change its command.
func TestANebiTokenCountsAsASecret(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("nebi", apitest.AliceID, "Nebi")
	ws.Template = "custom"
	tpl := apitest.CustomTemplate()
	tpl.Spec.NebiToken = true
	f := apitest.New(t, apitest.Options{}, tpl, ws)
	root := f.Session(admin, "javo-admins")

	w := f.Do("PUT", "/api/workspaces/nebi/launch?owner="+apitest.AliceID, `{"command":"sh -c 'cat $NEBI_TOKEN_FILE'"}`, root)
	refusedRewrite(t, "launch with a Nebi token", w)
	if w := f.Do("PUT", "/api/workspaces/nebi/launch", `{"command":"python b.py"}`, f.Session(apitest.AliceID)); w.Code != http.StatusOK {
		t.Fatalf("owner: %d %s", w.Code, w.Body)
	}
}

// Previews of the repository's own branches clone with the owner's
// repository token, so only the owner turns them on for an app that has one.
func TestOnlyTheOwnerTurnsOnPreviewsThatGetTheRepoToken(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Previews: true}, gatewaytest.Template("streamlit", "streamlit"), apitest.RepoApp("site"), apitest.RepoApp("open"))
	f.WithSecretKeys()
	alice := f.Session(apitest.AliceID)
	root := f.Session(admin, "javo-admins")
	f.CreateSecret(alice, "GH_TOKEN", apitest.TestRepoToken)
	if w := f.Do("PUT", "/api/workspaces/site/source/token", `{"secret":"GH_TOKEN"}`, alice); w.Code != http.StatusNoContent {
		t.Fatalf("token: %d %s", w.Code, w.Body)
	}
	owner := "?owner=" + apitest.AliceID
	w := f.Do("PUT", "/api/workspaces/site/previews"+owner, `{"enabled":true,"max":2}`, root)
	refusedAsHeld(t, "admin turns on previews", w)
	if w := f.Do("PUT", "/api/workspaces/open/previews"+owner, `{"enabled":true,"max":2}`, root); w.Code != http.StatusOK {
		t.Fatalf("admin on an app without a token: %d %s", w.Code, w.Body)
	}
	if w := f.Do("PUT", "/api/workspaces/site/previews", `{"enabled":true,"max":2}`, alice); w.Code != http.StatusOK {
		t.Fatalf("owner: %d %s", w.Code, w.Body)
	}
	if w := f.Do("PUT", "/api/workspaces/site/previews"+owner, `{"enabled":false,"max":2}`, root); w.Code != http.StatusOK {
		t.Fatalf("admin turns previews off: %d %s", w.Code, w.Body)
	}
}

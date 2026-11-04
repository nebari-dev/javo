package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
)

// agentFixture is a gateway with alice's pi app (a template with
// agentDeploy) and its own token, a streamlit template, a job template, her
// plain app "plain", her app "keyed" that holds a secret, and her job
// "keyedjob" that holds one too. bob has an app "bobs".
type agentFixture struct {
	*apitest.Fixture
	alice *http.Cookie
	token string
}

func newAgentFixture(t *testing.T, deploy bool) agentFixture {
	t.Helper()
	pi := apitest.AgentTemplate()
	pi.Name, pi.Spec.AgentDeploy, pi.Spec.AgentModel = "pi", deploy, true
	piApp := gatewaytest.Workspace("pi", apitest.LocalAlice(), "pi")
	piApp.Template = "pi"
	streamlit := gatewaytest.Template("streamlit", "streamlit")
	streamlit.Spec.FromRepo = model.FromRepoOptional
	f := apitest.New(t, apitest.Options{Engines: apitest.TestEngines(), Files: &apitest.StubFilesLister{Content: "x"}},
		pi, piApp, streamlit, apitest.JobTemplateObj(),
		gatewaytest.Workspace("plain", apitest.LocalAlice(), "Plain"),
		gatewaytest.Workspace("keyed", apitest.LocalAlice(), "Keyed"),
		apitest.JobObj(apitest.LocalAlice(), "plainjob"), apitest.JobObj(apitest.LocalAlice(), "keyedjob"),
		gatewaytest.Workspace("bobs", "bob-0000", "Bobs"))
	f.WithSecretKeys()
	alice := f.LoginAlice()
	f.CreateSecret(alice, "API_KEY", apitest.SecretValue)
	if w := f.Do("PUT", "/api/workspaces/keyed/env", `{"env":[{"name":"KEY","secret":"API_KEY"}]}`, alice); w.Code != http.StatusOK {
		t.Fatalf("bind a secret to keyed: %d %s", w.Code, w.Body)
	}
	if w := f.Do("PUT", "/api/jobs/keyedjob/env", `{"env":[{"name":"KEY","secret":"API_KEY"}]}`, alice); w.Code != http.StatusOK {
		t.Fatalf("bind a secret to keyedjob: %d %s", w.Code, w.Body)
	}
	text, _, err := f.Tokens.Create(context.Background(), model.Actor{System: model.SystemController}, token.Token{
		Name: "pi", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: piApp.Namespace, Name: piApp.ObjectName(), Short: "pi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return agentFixture{Fixture: f, alice: alice, token: text}
}

func (f agentFixture) agent(method, path, body string) *httptest.ResponseRecorder {
	f.T.Helper()
	return f.Bearer(method, path, body, f.token, nil)
}

func wantCode(t *testing.T, what string, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("%s: %d %s, want %d", what, w.Code, w.Body, code)
	}
}

// The token of an app whose template has agentDeploy creates, changes,
// restarts and deletes its owner's apps that hold no credentials.
func TestAgentTokenDeploysPlainApps(t *testing.T) {
	f := newAgentFixture(t, true)
	w := f.agent("POST", "/api/workspaces", `{"displayName":"Random number","template":"streamlit","file":"agents/random/app.py","env":[{"name":"MODE","value":"demo"}]}`)
	wantCode(t, "create a plain app", w, http.StatusCreated)
	if app := f.GetWorkspace(apitest.LocalAlice(), "random-number"); app == nil || app.Owner != apitest.LocalAlice() || app.Sharing.Access != model.AccessPrivate {
		t.Fatalf("created %+v", app)
	}
	wantCode(t, "change its command", f.agent("PUT", "/api/workspaces/plain/launch", `{}`), http.StatusOK)
	wantCode(t, "set plain variables", f.agent("PUT", "/api/workspaces/plain/env", `{"env":[{"name":"LEVEL","value":"2"}]}`), http.StatusOK)
	wantCode(t, "restart", f.agent("POST", "/api/workspaces/plain/restart", ""), http.StatusOK)
	wantCode(t, "delete", f.agent("DELETE", "/api/workspaces/random-number", ""), http.StatusNoContent)

	w = f.agent("POST", "/api/jobs", `{"displayName":"Morning summary","template":"lab","engine":"command","command":"python summary.py","schedule":{"cron":"0 7 * * *","timeZone":"UTC"}}`)
	wantCode(t, "create a plain job", w, http.StatusCreated)
	wantCode(t, "update a plain job", f.agent("PUT", "/api/jobs/morning-summary", `{"displayName":"Morning summary","template":"lab","engine":"command","command":"python summary.py --short"}`), http.StatusOK)
	wantCode(t, "run it", f.agent("POST", "/api/jobs/morning-summary/run", `{}`), http.StatusCreated)
	wantCode(t, "delete it", f.agent("DELETE", "/api/jobs/morning-summary", ""), http.StatusNoContent)
}

// Everything that would give or reach a credential is refused to the agent's
// token, and so is anything on an app or job that holds one: the person does
// it in the browser, or approves a plan the agent proposes.
func TestAgentTokenNeverReachesCredentials(t *testing.T) {
	f := newAgentFixture(t, true)
	apps, jobs := f.WorkspaceCount(), len(f.Jobs())
	for _, c := range []struct{ what, method, path, body string }{
		{"create an app with a secret", "POST", "/api/workspaces", `{"displayName":"x","template":"streamlit","file":"a.py","env":[{"name":"K","secret":"API_KEY"}]}`},
		{"create an app with API access", "POST", "/api/workspaces", `{"displayName":"x","template":"streamlit","file":"a.py","apiAccess":"Read"}`},
		{"create an app whose template gives an API token", "POST", "/api/workspaces", `{"displayName":"x","template":"pi"}`},
		{"create an app from a repository with a token", "POST", "/api/workspaces", `{"displayName":"x","template":"streamlit","file":"a.py","source":{"url":"https://git.example.com/a/b.git","tokenSecret":"API_KEY"}}`},
		{"create a job with a secret", "POST", "/api/jobs", `{"displayName":"x","template":"lab","engine":"command","command":"true","env":[{"name":"K","secret":"API_KEY"}]}`},
		{"give an app a secret", "PUT", "/api/workspaces/plain/env", `{"env":[{"name":"K","secret":"API_KEY"}]}`},
		{"give an app API access", "PUT", "/api/workspaces/plain/api-access", `{"apiAccess":"ReadWrite"}`},
		{"share an app", "PUT", "/api/workspaces/plain/sharing", `{"access":"SignedIn"}`},
		{"change the code of a secret-holding app", "PUT", "/api/workspaces/keyed/launch", `{}`},
		{"restart a secret-holding app", "POST", "/api/workspaces/keyed/restart", ""},
		{"delete a secret-holding app", "DELETE", "/api/workspaces/keyed", ""},
		{"change the code of a secret-holding job", "PUT", "/api/jobs/keyedjob", `{"displayName":"keyedjob","template":"lab","engine":"command","command":"curl evil.example.com"}`},
		{"run a secret-holding job", "POST", "/api/jobs/keyedjob/run", `{}`},
		{"delete a secret-holding job", "DELETE", "/api/jobs/keyedjob", ""},
		{"stop its own app, which holds an API token", "POST", "/api/workspaces/pi/stop", ""},
		{"read the secrets", "GET", "/api/secrets", ""},
		{"store a secret", "POST", "/api/secrets", `{"name":"MINE","value":"v"}`},
	} {
		if w := f.agent(c.method, c.path, c.body); w.Code != http.StatusForbidden {
			t.Errorf("%s with the agent's token: %d %s, want 403", c.what, w.Code, w.Body)
		}
	}
	if f.WorkspaceCount() != apps || len(f.Jobs()) != jobs {
		t.Fatal("an app or a job was created")
	}
	if app := f.GetWorkspace(apitest.LocalAlice(), "plain"); app.Sharing.Access != model.AccessPrivate || app.APIAccess != "" {
		t.Fatalf("plain changed: %+v", app)
	}
	// Another person's app stays out of sight.
	wantCode(t, "delete bob's app", f.agent("DELETE", "/api/workspaces/bobs?owner=bob-0000", ""), http.StatusNotFound)
	// The person, in the browser, still may.
	wantCode(t, "alice restarts keyed", f.Do("POST", "/api/workspaces/keyed/restart", "", f.alice), http.StatusOK)
}

// Without agentDeploy on its template, an app's token stays read-only.
func TestAgentDeployIsTheTemplatesCapability(t *testing.T) {
	f := newAgentFixture(t, false)
	wantCode(t, "create with a plain workload token", f.agent("POST", "/api/workspaces", `{"displayName":"x","template":"streamlit","file":"a.py"}`), http.StatusForbidden)
	wantCode(t, "delete with a plain workload token", f.agent("DELETE", "/api/workspaces/plain", ""), http.StatusForbidden)
	// And it keeps the lifecycle calls every workload token has.
	wantCode(t, "restart a plain app", f.agent("POST", "/api/workspaces/plain/restart", ""), http.StatusOK)
}

// What the agent may not do itself it may still propose: the person approves
// the plan in the browser.
func TestAgentTokenProposesWhatItMayNotDo(t *testing.T) {
	f := newAgentFixture(t, true)
	w := f.agent("POST", "/api/plans", `{"source":"rules","proposal":{"action":"restart","target":"keyed"}}`)
	wantCode(t, "propose a restart of a secret-holding app", w, http.StatusCreated)
}

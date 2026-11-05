package apps_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestPlanThenApproveRestartsTheAppAndIsAudited(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.RunningSales(), gatewaytest.Template("streamlit", "streamlit"))
	p := apitest.NewPlan(t, f, apitest.PlanOwner, "restart")
	if p.State != "pending" || p.Planned.Summary != reasons.Msg(reasons.MsgActionRestart, "Sales") ||
		len(p.Planned.Changes) != 1 || p.Planned.Changes[0].Field != "state" {
		t.Fatalf("plan %+v", p)
	}
	if !f.GetWorkspace(apitest.PlanOwner, "sales").RestartRequestedAt.IsZero() {
		t.Fatal("proposing changed the app")
	}
	w := f.Do("POST", "/api/plans/"+p.ID+"/approve", "", f.Session(apitest.PlanOwner))
	if w.Code != http.StatusOK || apitest.Decode[apitest.PlanJSON](t, w).State != "applied" {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	if f.GetWorkspace(apitest.PlanOwner, "sales").RestartRequestedAt.IsZero() {
		t.Fatal("approving did not restart the app")
	}
	evs, err := f.Db.Audit().List(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, e := range evs {
		if e.Action != "ActionApplied" {
			continue
		}
		found++
		var d map[string]string
		_ = json.Unmarshal(e.Detail, &d)
		if e.Actor != apitest.PlanOwner || e.ObjectName != "sales" || d["source"] != "rules" || d["summary"] == "" {
			t.Errorf("audit row %+v %v", e, d)
		}
	}
	if found != 1 {
		t.Fatalf("%d ActionApplied rows, want 1", found)
	}
}

func TestTwoApprovalsApplyOnce(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.RunningSales(), gatewaytest.Template("streamlit", "streamlit"))
	p := apitest.NewPlan(t, f, apitest.PlanOwner, "stop")
	c := f.Session(apitest.PlanOwner)
	codes := make([]int, 6)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() { defer wg.Done(); codes[i] = f.Do("POST", "/api/plans/"+p.ID+"/approve", "", c).Code }()
	}
	wg.Wait()
	ok := 0
	for _, code := range codes {
		switch code {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
		default:
			t.Errorf("approve answered %d", code)
		}
	}
	if ok != 1 {
		t.Fatalf("%d approvals succeeded, want 1 (%v)", ok, codes)
	}
	w := f.Do("POST", "/api/plans/"+p.ID+"/approve", "", c)
	if w.Code != http.StatusConflict || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgPlanDone, reasons.Msg(reasons.MsgPlanStateApplied)) {
		t.Fatalf("approving again: %d %s", w.Code, w.Body)
	}
}

func TestExpiredPlanAppliesNothing(t *testing.T) {
	t.Parallel()
	now := time.Now()
	f := apitest.New(t, apitest.Options{Now: func() time.Time { return now }}, apitest.RunningSales(), gatewaytest.Template("streamlit", "streamlit"))
	p := apitest.NewPlan(t, f, apitest.PlanOwner, "stop")
	now = now.Add(11 * time.Minute)
	if got := apitest.Decode[struct{ State, Message string }](t, f.Do("GET", "/api/plans/"+p.ID, "", f.Session(apitest.PlanOwner))); got.State != "expired" || got.Message != reasons.Msg(reasons.MsgPlanExpired) {
		t.Fatalf("expired plan reads %+v", got)
	}
	w := f.Do("POST", "/api/plans/"+p.ID+"/approve", "", f.Session(apitest.PlanOwner))
	if w.Code != http.StatusGone || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgPlanExpired) {
		t.Fatalf("approve after expiry: %d %s", w.Code, w.Body)
	}
	if f.GetWorkspace(apitest.PlanOwner, "sales").Paused {
		t.Fatal("an expired plan stopped the app")
	}
}

func TestAnotherPersonsPlanIsNotFound(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.RunningSales(), gatewaytest.Template("streamlit", "streamlit"))
	p := apitest.NewPlan(t, f, apitest.PlanOwner, "stop")
	bob := f.Session("bob-bbbbbbbb")
	for _, req := range []struct{ method, path string }{{"GET", "/api/plans/" + p.ID}, {"POST", "/api/plans/" + p.ID + "/approve"}, {"POST", "/api/plans/" + p.ID + "/reject"}} {
		w := f.Do(req.method, req.path, "", bob)
		if w.Code != http.StatusNotFound || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgPlanNotFound) {
			t.Errorf("%s %s as bob: %d %s", req.method, req.path, w.Code, w.Body)
		}
	}
	if f.GetWorkspace(apitest.PlanOwner, "sales").Paused {
		t.Fatal("bob stopped alice's app")
	}
	// Bob cannot propose a change to alice's app either.
	w := f.Do("POST", "/api/plans", `{"source":"rules","proposal":{"action":"stop","target":"sales"}}`, bob)
	if w.Code != http.StatusNotFound || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgAppNotFound) {
		t.Fatalf("bob proposing for alice's app: %d %s", w.Code, w.Body)
	}
}

func TestApproveFromAnAppPageIsRefused(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.RunningSales(), gatewaytest.Template("streamlit", "streamlit"))
	p := apitest.NewPlan(t, f, apitest.PlanOwner, "stop")
	c := f.Session(apitest.PlanOwner)
	for _, h := range []map[string]string{{"Sec-Fetch-Site": "cross-site"}, {"Referer": "http://javo.test/u/alice/other/"}} {
		w := f.DoWith("POST", "/api/plans/"+p.ID+"/approve", "", c, h)
		if w.Code != http.StatusForbidden {
			t.Errorf("approve with %v: %d %s", h, w.Code, w.Body)
		}
	}
	if f.GetWorkspace(apitest.PlanOwner, "sales").Paused {
		t.Fatal("a cross-site request stopped the app")
	}
}

func TestApproveChecksTheChangeAgain(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.RunningSales(), gatewaytest.Template("streamlit", "streamlit"))
	p := apitest.NewPlan(t, f, apitest.PlanOwner, "stop")
	c := f.Session(apitest.PlanOwner)
	if w := f.Do("POST", "/api/workspaces/sales/stop", "", c); w.Code != http.StatusOK {
		t.Fatal(w.Body)
	}
	before := f.GetWorkspace(apitest.PlanOwner, "sales").Version
	w := f.Do("POST", "/api/plans/"+p.ID+"/approve", "", c)
	if w.Code != http.StatusConflict || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgActionNotNeeded, "Sales") {
		t.Fatalf("approve of a change already made: %d %s", w.Code, w.Body)
	}
	if got := apitest.Decode[apitest.PlanJSON](t, f.Do("GET", "/api/plans/"+p.ID, "", c)); got.State != "failed" || got.Error == "" {
		t.Fatalf("plan after a failed apply: %+v", got)
	}
	if f.GetWorkspace(apitest.PlanOwner, "sales").Version != before {
		t.Fatal("a failed apply changed the app")
	}
}

func TestRejectChangesNothing(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.RunningSales(), gatewaytest.Template("streamlit", "streamlit"))
	p := apitest.NewPlan(t, f, apitest.PlanOwner, "delete")
	if !apitest.Decode[struct{ Planned struct{ Destructive bool } }](t, f.Do("GET", "/api/plans/"+p.ID, "", f.Session(apitest.PlanOwner))).Planned.Destructive {
		t.Error("deleting is not marked destructive")
	}
	w := f.Do("POST", "/api/plans/"+p.ID+"/reject", "", f.Session(apitest.PlanOwner))
	if w.Code != http.StatusOK || apitest.Decode[apitest.PlanJSON](t, w).State != "rejected" {
		t.Fatalf("reject: %d %s", w.Code, w.Body)
	}
	if f.WorkspaceCount() != 1 {
		t.Fatal("rejecting deleted the app")
	}
}

func TestUnknownActionIsRefused(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.RunningSales(), gatewaytest.Template("streamlit", "streamlit"))
	w := f.Do("POST", "/api/plans", `{"source":"rules","proposal":{"action":"share","target":"sales"}}`, f.Session(apitest.PlanOwner))
	if w.Code != http.StatusUnprocessableEntity || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgActionNotAllowed) {
		t.Fatalf("unknown action: %d %s", w.Code, w.Body)
	}
}

func TestATokenCannotApprove(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), apitest.RunningSalesOf(apitest.LocalAlice()))
	c := f.LoginAlice()
	tok := f.NewToken(c, `{"name":"agent","scope":"read-write"}`).Token
	w := f.Do("POST", "/api/plans", `{"source":"rules","proposal":{"action":"stop","target":"sales"}}`, c)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	p := apitest.Decode[apitest.PlanJSON](t, w)
	for _, verb := range []string{"approve", "reject"} {
		w := f.Bearer("POST", "/api/plans/"+p.ID+"/"+verb, "", tok, nil)
		if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgPlanSessionOnly) {
			t.Fatalf("%s with a token: %d %s", verb, w.Code, w.Body)
		}
	}
	if f.GetWorkspace(apitest.LocalAlice(), "sales").Paused {
		t.Fatal("a token approved a change")
	}
}

// An agent names itself in source, so the plan also says which credential
// asked, as the gateway knows it: the API token's name. The person's own
// browser session names none.
func TestAPlanSaysWhichTokenAskedForIt(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), apitest.RunningSalesOf(apitest.LocalAlice()))
	c := f.LoginAlice()
	tok := f.NewToken(c, `{"name":"laptop-cli","scope":"read-write"}`).Token
	w := f.Bearer("POST", "/api/plans", `{"source":"mcp:Your Admin","proposal":{"action":"stop","target":"sales"}}`, tok, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	id := apitest.Decode[apitest.PlanJSON](t, w).ID
	got := apitest.Decode[apitest.PlanJSON](t, f.Do("GET", "/api/plans/"+id, "", c))
	if got.Source != "mcp:Your Admin" || got.RequestedBy.Token != "laptop-cli" || got.RequestedBy.App != "" {
		t.Fatalf("plan %+v", got)
	}
	mine := apitest.Decode[apitest.PlanJSON](t, f.Do("POST", "/api/plans", `{"source":"rules","proposal":{"action":"stop","target":"sales"}}`, c))
	if mine.RequestedBy.Token != "" || mine.RequestedBy.App != "" {
		t.Fatalf("session plan %+v", mine)
	}
}

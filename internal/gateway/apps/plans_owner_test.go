package apps_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nebari-dev/javo/internal/ai/aitest"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
)

const rootAdmin = "root-rrrrrrrr"

// proposalView is a proposal as the diagnosis and explain answers show it.
type proposalView struct {
	Action string            `json:"action"`
	Target string            `json:"target"`
	Owner  string            `json:"owner,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

// approveProposal turns p into a plan for c, as the UI does, and approves it.
func approveProposal(t *testing.T, f *apitest.Fixture, c *http.Cookie, source string, p proposalView) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"source": source, "proposal": p})
	w := f.Do("POST", "/api/plans", string(body), c)
	if w.Code != http.StatusCreated {
		t.Fatalf("create plan: %d %s", w.Code, w.Body)
	}
	pl := apitest.Decode[apitest.PlanJSON](t, w)
	if w := f.Do("POST", "/api/plans/"+pl.ID+"/approve", "", c); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
}

// A platform admin looking at alice's failed app gets fixes for alice's app,
// not for the admin's own app of the same name.
func TestAdminFixOfAnotherPersonsAppTargetsThatApp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, method, path, body, source string }{
		{"rules", "GET", "/api/workspaces/sales/diagnosis?owner=" + apitest.PlanOwner, "", "rules"},
		{"explain", "POST", "/api/workspaces/sales/diagnosis/explain?owner=" + apitest.PlanOwner, `{}`, "ai-diagnosis"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := aitest.New(aitest.Reply{JSON: apitest.RestartAnswer})
			f := apitest.New(t, apitest.Options{Ai: apitest.AiService(p)}, apitest.CrashedApp(), apitest.RunningSalesOf(rootAdmin),
				gatewaytest.Template("streamlit", "streamlit"))
			admin := f.Session(rootAdmin, "javo-admins")
			w := f.Do(tc.method, tc.path, tc.body, admin)
			if w.Code != http.StatusOK {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			out := apitest.Decode[struct{ Proposals []proposalView }](t, w)
			if len(out.Proposals) == 0 {
				t.Fatalf("no proposals: %s", w.Body)
			}
			prop := out.Proposals[0]
			if prop.Owner != apitest.PlanOwner || prop.Action != "restart" {
				t.Fatalf("proposal %+v", prop)
			}
			approveProposal(t, f, admin, tc.source, prop)
			if !f.GetWorkspace(rootAdmin, "sales").RestartRequestedAt.IsZero() {
				t.Fatal("the admin's own app was restarted")
			}
			if f.GetWorkspace(apitest.PlanOwner, "sales").RestartRequestedAt.IsZero() {
				t.Fatal("alice's app was not restarted")
			}
		})
	}
}

// The owner named in a proposal is checked when the plan is made and again
// when it is approved: a member cannot aim one at another person's app.
func TestProposalForAnotherOwnerIsNotFoundForAMember(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.RunningSales(), apitest.RunningSalesOf("bob-bbbbbbbb"), gatewaytest.Template("streamlit", "streamlit"))
	bob := f.Session("bob-bbbbbbbb")
	w := f.Do("POST", "/api/plans", `{"source":"rules","proposal":{"action":"stop","target":"sales","owner":"`+apitest.PlanOwner+`"}}`, bob)
	if w.Code != http.StatusNotFound {
		t.Fatalf("bob proposing for alice's app: %d %s", w.Code, w.Body)
	}
	if f.GetWorkspace(apitest.PlanOwner, "sales").Paused {
		t.Fatal("bob stopped alice's app")
	}
}

// A plan made by an admin for alice's app is applied to alice's app only
// while the approver may still manage it.
func TestPlanForAnotherOwnerIsCheckedAgainAtApprove(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.RunningSales(), apitest.RunningSalesOf(rootAdmin), gatewaytest.Template("streamlit", "streamlit"))
	admin := f.Session(rootAdmin, "javo-admins")
	w := f.Do("POST", "/api/plans", `{"source":"rules","proposal":{"action":"stop","target":"sales","owner":"`+apitest.PlanOwner+`"}}`, admin)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	pl := apitest.Decode[apitest.PlanJSON](t, w)
	// The same person without the admin group: the plan is theirs, the app is not.
	demoted := f.Session(rootAdmin)
	if w := f.Do("POST", "/api/plans/"+pl.ID+"/approve", "", demoted); w.Code != http.StatusNotFound {
		t.Fatalf("approve without admin rights: %d %s", w.Code, w.Body)
	}
	if f.GetWorkspace(apitest.PlanOwner, "sales").Paused || f.GetWorkspace(rootAdmin, "sales").Paused {
		t.Fatal("a refused approval stopped an app")
	}
}

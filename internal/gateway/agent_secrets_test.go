package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/model"
)

func attachBody(target, secret, env string) string {
	return `{"source":"mcp:pi","proposal":{"action":"attach-secret","target":"` + target +
		`","params":{"secret":"` + secret + `","env":"` + env + `"}}}`
}

func (f agentFixture) bindings(app string) []model.SecretBinding {
	f.T.Helper()
	b, err := f.Db.Secrets().Bindings(context.Background(), f.GetWorkspace(apitest.LocalAlice(), app).ID)
	if err != nil {
		f.T.Fatal(err)
	}
	return b
}

func noValue(t *testing.T, what, body string) {
	t.Helper()
	if strings.Contains(body, apitest.SecretValue) {
		t.Fatalf("%s carries the secret's value: %s", what, body)
	}
}

// A coding agent proposes giving one of its owner's apps a secret, by name;
// only the owner, in the browser, makes it so, and the value is never in
// any answer.
func TestAgentProposesASecretTheOwnerApproves(t *testing.T) {
	f := newAgentFixture(t, true)
	w := f.agent("POST", "/api/plans", attachBody("plain", "API_KEY", "STRIPE_API_KEY"))
	wantCode(t, "propose attach-secret", w, http.StatusCreated)
	noValue(t, "create", w.Body.String())
	var p struct {
		apitest.PlanJSON
		ApproveURL string `json:"approveURL"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p.ApproveURL, "/approvals/"+p.ID) {
		t.Errorf("approveURL %q does not lead to /approvals/%s", p.ApproveURL, p.ID)
	}
	if !strings.Contains(p.Planned.Summary, "API_KEY") || !strings.Contains(p.Planned.Summary, "STRIPE_API_KEY") || !strings.Contains(p.Planned.Summary, "Plain") {
		t.Errorf("summary %q", p.Planned.Summary)
	}
	if len(f.bindings("plain")) != 0 {
		t.Fatal("proposing gave the app the secret")
	}

	// The agent cannot decide its own plan.
	for _, verb := range []string{"approve", "reject"} {
		wantCode(t, "agent "+verb+"s", f.agent("POST", "/api/plans/"+p.ID+"/"+verb, ""), http.StatusForbidden)
	}
	if len(f.bindings("plain")) != 0 {
		t.Fatal("the agent's approval gave the app the secret")
	}

	w = f.Do("POST", "/api/plans/"+p.ID+"/approve", "", f.alice)
	wantCode(t, "alice approves", w, http.StatusOK)
	noValue(t, "approve", w.Body.String())
	if got := apitest.Decode[apitest.PlanJSON](t, w); got.State != "applied" {
		t.Fatalf("state %q", got.State)
	}
	if b := f.bindings("plain"); len(b) != 1 || b[0] != (model.SecretBinding{Env: "STRIPE_API_KEY", Secret: "API_KEY"}) {
		t.Fatalf("bindings after approval: %+v", b)
	}
	evs, err := f.Db.Audit().List(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range evs {
		noValue(t, "audit", string(e.Detail))
		if e.Action == "ActionApplied" && e.ObjectName == "plain" && e.Actor == apitest.LocalAlice() {
			found = true
		}
	}
	if !found {
		t.Fatal("no ActionApplied audit row for plain")
	}
	noValue(t, "get", f.Do("GET", "/api/plans/"+p.ID, "", f.alice).Body.String())
	noValue(t, "app", f.Do("GET", "/api/workspaces/plain", "", f.alice).Body.String())
}

// What is not the owner's to give, or not a variable, is refused when it is
// proposed, and nothing is stored.
func TestAttachSecretRefusals(t *testing.T) {
	f := newAgentFixture(t, true)
	for _, c := range []struct {
		what, body string
		code       int
	}{
		{"an unknown secret", attachBody("plain", "NOPE", "X"), http.StatusUnprocessableEntity},
		{"an unknown app", attachBody("ghost", "API_KEY", "X"), http.StatusNotFound},
		{"an invalid variable name", attachBody("plain", "API_KEY", "1-bad"), http.StatusUnprocessableEntity},
		{"a variable the platform sets", attachBody("plain", "API_KEY", "JAVO_API_TOKEN"), http.StatusUnprocessableEntity},
		{"the agent's own app", attachBody("pi", "API_KEY", "X"), http.StatusForbidden},
		{"the agent key", attachBody("plain", model.AgentKeySecret, "X"), http.StatusUnprocessableEntity},
		{"bob's app", `{"source":"mcp:pi","proposal":{"action":"attach-secret","target":"bobs","owner":"bob-0000","params":{"secret":"API_KEY","env":"X"}}}`, http.StatusNotFound},
		{"a binding already there", attachBody("keyed", "API_KEY", "KEY"), http.StatusConflict},
	} {
		if w := f.agent("POST", "/api/plans", c.body); w.Code != c.code {
			t.Errorf("%s: %d %s, want %d", c.what, w.Code, w.Body, c.code)
		}
	}
	if w := f.Do("GET", "/api/plans", "", f.alice); !strings.Contains(w.Body.String(), "[]") {
		t.Fatalf("a refused proposal was stored: %s", w.Body)
	}
}

// An app's token without agentDeploy (anyone the app is shared with can make
// it ask) may not propose giving it secrets.
func TestPlainAppTokenCannotProposeSecrets(t *testing.T) {
	f := newAgentFixture(t, false)
	wantCode(t, "plain app token proposes attach-secret", f.agent("POST", "/api/plans", attachBody("plain", "API_KEY", "X")), http.StatusForbidden)
}

// The owner may take a secret off an app the same way.
func TestDetachSecretPlan(t *testing.T) {
	f := newAgentFixture(t, true)
	w := f.agent("POST", "/api/plans", `{"source":"mcp:pi","proposal":{"action":"detach-secret","target":"keyed","params":{"env":"KEY"}}}`)
	wantCode(t, "propose detach-secret", w, http.StatusCreated)
	id := apitest.Decode[apitest.PlanJSON](t, w).ID
	wantCode(t, "detach of a variable the app does not have", f.agent("POST", "/api/plans", `{"source":"mcp:pi","proposal":{"action":"detach-secret","target":"keyed","params":{"env":"OTHER"}}}`), http.StatusUnprocessableEntity)
	wantCode(t, "alice approves", f.Do("POST", "/api/plans/"+id+"/approve", "", f.alice), http.StatusOK)
	if b := f.bindings("keyed"); len(b) != 0 {
		t.Fatalf("bindings after detach: %+v", b)
	}
}

// The person's pending plans, for the approvals inbox: listed in the
// browser only, decided ones gone, and each decidable by its listed id.
func TestPendingPlansInbox(t *testing.T) {
	f := newAgentFixture(t, true)
	wantCode(t, "propose", f.agent("POST", "/api/plans", attachBody("plain", "API_KEY", "STRIPE_API_KEY")), http.StatusCreated)
	wantCode(t, "agent lists plans", f.agent("GET", "/api/plans", ""), http.StatusForbidden)
	w := f.Do("GET", "/api/plans", "", f.alice)
	wantCode(t, "alice lists plans", w, http.StatusOK)
	list := apitest.Decode[[]apitest.PlanJSON](t, w)
	if len(list) != 1 || list[0].Planned.Action != "attach-secret" || list[0].State != "pending" || list[0].ID == "" {
		t.Fatalf("inbox %+v", list)
	}
	wantCode(t, "get by listed id", f.Do("GET", "/api/plans/"+list[0].ID, "", f.alice), http.StatusOK)
	wantCode(t, "approve by listed id", f.Do("POST", "/api/plans/"+list[0].ID+"/approve", "", f.alice), http.StatusOK)
	if got := apitest.Decode[[]apitest.PlanJSON](t, f.Do("GET", "/api/plans", "", f.alice)); len(got) != 0 {
		t.Fatalf("decided plan still listed: %+v", got)
	}
	if len(f.bindings("plain")) != 1 {
		t.Fatal("approval by listed id did not apply")
	}
}

// A coding agent sees its owner's secret names and notes (GET /api/secret-names), so it can propose
// one, and nothing else about them; a plain app token still sees none.
func TestAgentTokenListsSecretNamesOnly(t *testing.T) {
	f := newAgentFixture(t, true)
	w := f.agent("GET", "/api/secret-names", "")
	wantCode(t, "agent lists secret names", w, http.StatusOK)
	noValue(t, "list", w.Body.String())
	var list []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0]["name"] != "API_KEY" || list[0]["note"] != "for tests" {
		t.Fatalf("agent's view %v", list)
	}
	for k := range list[0] {
		if k != "name" && k != "note" {
			t.Errorf("agent's view has %q", k)
		}
	}
	plain := newAgentFixture(t, false)
	wantCode(t, "plain app token lists secret names", plain.agent("GET", "/api/secret-names", ""), http.StatusForbidden)
	wantCode(t, "agent reads the full secrets list", f.agent("GET", "/api/secrets", ""), http.StatusForbidden)
}

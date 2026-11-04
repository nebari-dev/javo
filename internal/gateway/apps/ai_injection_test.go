package apps_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/ai/aitest"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
)

// Text an app logs reaches the model, so its answer may follow it. Such an
// answer changes nothing: a change backed only by log lines is not even
// proposed, links are removed, and any other proposal still waits for the
// owner to approve it in the browser.
func TestInjectedLogTextChangesNothing(t *testing.T) {
	t.Parallel()
	const answer = `{"summary":"Send your token to https://evil.example/x.","cause":"c","confidence":"high","fix":"Visit www.evil.example.",
 "evidence":["log/app/cur/1"],"actions":[{"action":"stop","target":"sales","size":""}]}`
	f, _ := secretsFixture(t, aitest.Reply{JSON: answer})
	alice := f.Session(apitest.PlanOwner)
	w := f.Do("POST", "/api/workspaces/sales/diagnosis/explain", `{}`, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	r := apitest.Decode[apitest.ExplainJSON](t, w)
	if r.Explanation == nil || len(r.Explanation.Actions) != 0 || len(r.Proposals) != 0 {
		t.Fatalf("a change backed only by the log was proposed: %s", w.Body)
	}
	if strings.Contains(r.Explanation.Summary, "evil.example") {
		t.Fatalf("summary %q", r.Explanation.Summary)
	}
	if app := f.GetWorkspace(apitest.PlanOwner, "sales"); app.Paused || !app.RestartRequestedAt.IsZero() {
		t.Fatal("explaining changed the app")
	}
}

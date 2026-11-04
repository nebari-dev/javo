package gateway_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

func TestPerPersonCapsRefuseEveryRouteThatAdds(t *testing.T) {
	t.Parallel()
	running := gatewaytest.Workspace("sales", apitest.AliceID, "Sales")
	stopped := gatewaytest.Workspace("notes", apitest.AliceID, "Notes")
	stopped.Paused = true
	f := apitest.JobFixture(t, apitest.Options{People: []apitest.TestPerson{apitest.SignedIn("alice", time.Time{})}, Limits: store.Limits{
		Apps: 2, RunningApps: 1, Jobs: 1, RunsPerJob: 1, APITokens: 1, PendingPlans: 1,
	}}, gatewaytest.Template("streamlit", "streamlit"), running, stopped, apitest.JobObj(apitest.AliceID, "nightly"))
	alice := f.Session(apitest.AliceID)
	check := func(what string, method, path, body string, msg reasons.MessageCode, n int) {
		t.Helper()
		w := f.Do(method, path, body, alice)
		if w.Code != http.StatusConflict {
			t.Fatalf("%s: status %d, want 409: %s", what, w.Code, w.Body)
		}
		if p := apitest.Decode[apitest.Problem](t, w); p.Title != reasons.Msg(msg, n) {
			t.Fatalf("%s: title %q, want %q", what, p.Title, reasons.Msg(msg, n))
		}
	}
	check("create app", "POST", "/api/workspaces", `{"displayName":"More","template":"streamlit","file":"app.py"}`, reasons.MsgLimitApps, 2)
	check("start app", "POST", "/api/workspaces/notes/start", "", reasons.MsgLimitRunningApps, 1)
	check("create job", "POST", "/api/jobs", `{"displayName":"Hourly","template":"lab","engine":"papermill","source":"reports/sales.ipynb","kernel":"python3"}`, reasons.MsgLimitJobs, 1)
	if w := f.Do("POST", "/api/jobs/nightly/run", `{}`, alice); w.Code != http.StatusAccepted && w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("first run: %d %s", w.Code, w.Body)
	}
	check("run now", "POST", "/api/jobs/nightly/run", `{}`, reasons.MsgLimitRunsPerJob, 1)
	if w := f.Do("POST", "/api/tokens", `{"name":"laptop","scope":"read"}`, alice); w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("first token: %d %s", w.Code, w.Body)
	}
	check("create token", "POST", "/api/tokens", `{"name":"desktop","scope":"read"}`, reasons.MsgLimitAPITokens, 1)
	plan := `{"source":"rules","proposal":{"action":"restart","target":"sales"}}`
	if w := f.Do("POST", "/api/plans", plan, alice); w.Code != http.StatusCreated {
		t.Fatalf("first plan: %d %s", w.Code, w.Body)
	}
	check("create plan", "POST", "/api/plans", plan, reasons.MsgLimitPendingPlans, 1)

	// Import at the apps cap: the row says why, and nothing is stored.
	w := f.Do(http.MethodPost, "/api/workspaces/import", apitest.ImportBody(apitest.StreamlitApp(apitest.AliceID, "Plots"), "skip"), alice)
	if w.Code != http.StatusOK {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	if rows := apitest.Decode[apitest.TransferJSON](t, w).Rows; len(rows) != 1 || rows[0].Message != reasons.Msg(reasons.MsgLimitApps, 2) {
		t.Fatalf("import rows = %+v", rows)
	}
	if n := f.WorkspaceCount(); n != 2 {
		t.Fatalf("%d apps stored, want the 2 there were", n)
	}
}

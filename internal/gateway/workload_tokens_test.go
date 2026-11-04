package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// workloadFixture is a gateway with alice's agent app, a job of hers, and
// the read-write token of that app, as its JAVO_TOKEN.
func workloadFixture(t *testing.T, files *apitest.StubFilesLister) (*apitest.Fixture, string) {
	t.Helper()
	ws := gatewaytest.Workspace("agent", apitest.LocalAlice(), "Agent")
	ws.Template = "agent"
	f := apitest.New(t, apitest.Options{Files: files}, apitest.AgentTemplate(), ws,
		apitest.JobTemplateObj(), apitest.JobObj(apitest.LocalAlice(), "nightly"))
	_ = f.LoginAlice()
	text, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{
		Name: "agent", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: ws.Namespace, Name: ws.ObjectName(), Short: "agent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, text
}

// An app's or a job's own token never writes its owner's home and never
// creates or changes what an app or job runs: the code a secret-holding job
// runs next sits in that home, so a write there is a way to its secrets.
func TestWorkloadTokensCannotWriteTheHomeOrCreateWhatRuns(t *testing.T) {
	l := &apitest.StubFilesLister{Content: "x"}
	f, text := workloadFixture(t, l)
	refused := reasons.Msg(reasons.MsgWorkloadTokenReadOnly)

	w := f.Bearer("PUT", "/api/files/raw?path=reports/sales.ipynb", "print(1)", text, nil, "Content-Type", "text/plain", "If-None-Match", "*")
	if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != refused {
		t.Fatalf("save a home file with an app token: %d %s", w.Code, w.Body)
	}
	if len(l.Saved) != 0 {
		t.Fatalf("saved %v", l.Saved)
	}
	jobs := len(f.Jobs())
	w = f.Bearer("POST", "/api/jobs", `{"displayName":"x","template":"jobs","engine":"command","command":"true"}`, text, nil)
	if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != refused {
		t.Fatalf("create a job with an app token: %d %s", w.Code, w.Body)
	}
	if len(f.Jobs()) != jobs {
		t.Fatal("the job was created")
	}
	apps := f.WorkspaceCount()
	w = f.Bearer("POST", "/api/workspaces", `{"displayName":"x","template":"agent"}`, text, nil)
	if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != refused {
		t.Fatalf("create an app with an app token: %d %s", w.Code, w.Body)
	}
	if f.WorkspaceCount() != apps {
		t.Fatal("the app was created")
	}
	// Reads stay open, and so do the lifecycle calls an agent makes.
	if w := f.Bearer("GET", "/api/jobs", "", text, nil); w.Code != http.StatusOK {
		t.Fatalf("list jobs with an app token: %d %s", w.Code, w.Body)
	}
	if w := f.Bearer("POST", "/api/workspaces/agent/stop", "", text, nil); w.Code != http.StatusOK {
		t.Fatalf("stop its own app with an app token: %d %s", w.Code, w.Body)
	}
	// A personal token of the same person still writes.
	personal, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{
		Name: "laptop", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := f.Bearer("PUT", "/api/files/raw?path=notes.txt", "hi", personal, nil, "Content-Type", "text/plain", "If-None-Match", "*"); w.Code != http.StatusCreated {
		t.Fatalf("save with a personal token: %d %s", w.Code, w.Body)
	}
}

// Every operation that is not a read refuses a workload token unless it is
// on the short list of lifecycle calls: a new route is refused until someone
// decides it is safe, so no route can be missed.
func TestEveryWriteRouteRefusesWorkloadTokensUnlessListed(t *testing.T) {
	f, text := workloadFixture(t, &apitest.StubFilesLister{Content: "x"})
	w := f.Do("GET", "/api/openapi.json", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("openapi: %d", w.Code)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string            `json:"operationId"`
			Security    *[]map[string]any `json:"security"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	seen := map[string]bool{}
	checked := 0
	for p, ops := range doc.Paths {
		for method, op := range ops {
			seen[op.OperationID] = true
			method = strings.ToUpper(method)
			public := op.Security != nil && len(*op.Security) == 0
			if public || method == http.MethodGet || method == http.MethodHead || slices.Contains(authn.WorkloadTokenWrites, op.OperationID) {
				continue
			}
			path := param.ReplaceAllString(p, "x")
			w := f.Bearer(method, path, "{}", text, nil)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s (%s) with an app token: %d %s", method, p, op.OperationID, w.Code, w.Body)
			}
			checked++
		}
	}
	if checked < 40 {
		t.Fatalf("only %d write operations checked", checked)
	}
	for _, id := range authn.WorkloadTokenWrites {
		if !seen[id] {
			t.Errorf("WorkloadTokenWrites names %q, which is not an operation", id)
		}
	}
}

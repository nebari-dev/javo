package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestMCPNeedsABearerToken(t *testing.T) {
	t.Parallel()
	f := newMCPFixture(t)
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	r, _ := http.NewRequest("POST", f.url+"/mcp", body)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(f.alice) // a browser session alone is not enough
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `resource_metadata="/.well-known/oauth-protected-resource"`) {
		t.Fatalf("no token: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	meta, err := http.Get(f.url + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(meta.Body)
	meta.Body.Close()
	if !strings.Contains(string(b), "workloads:write") || !strings.Contains(string(b), `"resource":"/mcp"`) {
		t.Fatalf("metadata %s", b)
	}
}

func TestMCPRefusesOtherOrigins(t *testing.T) {
	t.Parallel()
	f := newMCPFixture(t)
	tok := f.NewToken(f.alice, `{"name":"agent","scope":"read-write"}`).Token
	r, _ := http.NewRequest("POST", f.url+"/mcp", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin: %d", resp.StatusCode)
	}
}

func TestToolsComeFromTheAPIDocument(t *testing.T) {
	t.Parallel()
	f := newMCPFixture(t)
	cs := f.connect(t, f.NewToken(f.alice, `{"name":"agent","scope":"read-write"}`).Token)
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]*sdk.Tool{}
	for _, tl := range list.Tools {
		by[tl.Name] = tl
	}
	for _, name := range []string{"list_workspaces", "get_workspace", "get_diagnosis", "get_plan", "stop_workspace", "restart_workspace"} {
		if by[name] == nil {
			t.Errorf("tool %s missing", name)
		}
	}
	doc := f.Srv.API.OpenAPI()
	if by["list_workspaces"].Description != doc.Paths["/api/workspaces"].Get.Summary {
		t.Errorf("description %q is not the operation's summary", by["list_workspaces"].Description)
	}
	for _, name := range []string{"apply_plan", "approve_plan", "list_tokens", "stream_workspace_logs"} {
		if by[name] != nil {
			t.Errorf("tool %s must not exist", name)
		}
	}
}

func TestMutatingToolNeverApplies(t *testing.T) {
	t.Parallel()
	f := newMCPFixture(t)
	cs := f.connect(t, f.NewToken(f.alice, `{"name":"agent","scope":"read-write"}`).Token)
	res, text := callTool(t, cs, "stop_workspace", map[string]any{"name": "sales"})
	var plan struct {
		PlanID, State, ApproveURL string
	}
	if err := json.Unmarshal([]byte(text), &plan); err != nil || res.IsError || plan.State != "awaiting_approval" || !strings.HasSuffix(plan.ApproveURL, "/plans/"+plan.PlanID) {
		t.Fatalf("stop_workspace: %s", text)
	}
	if f.GetWorkspace(apitest.LocalAlice(), "sales").Paused {
		t.Fatal("the agent stopped the app")
	}
	// Every tool, with the plan id wherever it fits, changes nothing.
	list, _ := cs.ListTools(context.Background(), nil)
	for _, tl := range list.Tools {
		_, _ = cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tl.Name, Arguments: map[string]any{"name": "sales", "id": plan.PlanID}})
	}
	if f.GetWorkspace(apitest.LocalAlice(), "sales").Paused {
		t.Fatal("a tool applied the plan")
	}
	// The person approves it in the browser.
	if w := f.Do("POST", "/api/plans/"+plan.PlanID+"/approve", "", f.alice); w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	if !f.GetWorkspace(apitest.LocalAlice(), "sales").Paused {
		t.Fatal("approving did not stop the app")
	}
	evs, _ := f.Db.Audit().List(context.Background(), 50)
	found := false
	for _, e := range evs {
		found = found || e.Action == "ActionApplied" && strings.Contains(string(e.Detail), `"source":"mcp:claude-code"`)
	}
	if !found {
		t.Fatal("the applied change is not audited with the agent as its source")
	}
}

func TestAnotherPersonsTokenSeesNothingOfAlices(t *testing.T) {
	t.Parallel()
	f := newMCPFixture(t)
	aliceCS := f.connect(t, f.NewToken(f.alice, `{"name":"agent","scope":"read-write"}`).Token)
	_, text := callTool(t, aliceCS, "stop_workspace", map[string]any{"name": "sales"})
	var plan struct{ PlanID string }
	_ = json.Unmarshal([]byte(text), &plan)

	bob := f.Session(apitest.PersonID("bob"))
	bobCS := f.connect(t, f.NewToken(bob, `{"name":"agent","scope":"read-write"}`).Token)
	if res, text := callTool(t, bobCS, "get_workspace", map[string]any{"name": "sales"}); !res.IsError || text != reasons.Msg(reasons.MsgAppNotFound) {
		t.Fatalf("bob get_workspace: %s", text)
	}
	if res, text := callTool(t, bobCS, "get_plan", map[string]any{"id": plan.PlanID}); !res.IsError || text != reasons.Msg(reasons.MsgPlanNotFound) {
		t.Fatalf("bob get_plan: %s", text)
	}
}

func TestReadTokenCannotPropose(t *testing.T) {
	t.Parallel()
	f := newMCPFixture(t)
	cs := f.connect(t, f.NewToken(f.alice, `{"name":"agent","scope":"read"}`).Token)
	if res, text := callTool(t, cs, "stop_workspace", map[string]any{"name": "sales"}); !res.IsError || text != reasons.Msg(reasons.MsgTokenReadOnly) {
		t.Fatalf("read token: %s", text)
	}
}

func TestDiagnosisToolMatchesTheAPI(t *testing.T) {
	t.Parallel()
	f := newMCPFixture(t)
	tok := f.NewToken(f.alice, `{"name":"agent","scope":"read"}`).Token
	_, text := callTool(t, f.connect(t, tok), "get_diagnosis", map[string]any{"name": "sales"})
	w := f.Bearer("GET", "/api/workspaces/sales/diagnosis", "", tok, nil)
	var a, b any
	_ = json.Unmarshal([]byte(text), &a)
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	if w.Code != http.StatusOK || !jsonEqual(a, b) {
		t.Fatalf("tool %s, API %s", text, w.Body)
	}
}

// mcpFixture is a gateway with MCP on, served over HTTP, alice's running
// app "sales", and bob signed in once.
type mcpFixture struct {
	*apitest.Fixture
	url   string
	alice *http.Cookie
}

func newMCPFixture(t *testing.T) *mcpFixture {
	t.Helper()
	f := apitest.New(t, apitest.Options{Mcp: true, People: []apitest.TestPerson{apitest.SignedIn("bob", time.Time{})}},
		gatewaytest.Template("streamlit", "streamlit"), apitest.RunningSalesOf(apitest.LocalAlice()))
	s := httptest.NewServer(f.Srv)
	t.Cleanup(s.Close)
	return &mcpFixture{Fixture: f, url: s.URL, alice: f.LoginAlice()}
}

// bearerTransport adds a bearer token to every request.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func (f *mcpFixture) connect(t *testing.T, tok string) *sdk.ClientSession {
	t.Helper()
	c := sdk.NewClient(&sdk.Implementation{Name: "claude-code", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint: f.url + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{tok}}, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (*sdk.CallToolResult, string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res, res.Content[0].(*sdk.TextContent).Text
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

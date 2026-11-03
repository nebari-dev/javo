//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const brokenFile = "e2e/broken.py"

// tokenTransport sends an API token on every request.
type tokenTransport struct {
	token string
	next  http.RoundTripper
}

func (t tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(r)
}

// crashingApp runs file with python as a typed command, so an import error
// ends the process (a Streamlit app shows the error in its page and keeps
// running, so it never fails).
func crashingApp(s *session, display, file string) app {
	return s.createApp(map[string]any{
		"displayName": fmt.Sprintf("%s %d", display, time.Now().Unix()%100000),
		"template":    "custom", "file": file, "command": "python {{.File}}",
	})
}

// With AI off (the default): an app that imports a missing module gets
// the rules' cause and a restart proposal; the proposal becomes a plan that
// applies only when alice approves it; an agent over MCP can only propose.
func TestDiagnosisPlansAndMCP(t *testing.T) {
	s := newSession(t)
	s.login(localLogin, "alice")
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	c := kube(t)

	var status struct {
		Enabled bool
		Message string
	}
	s.json(http.MethodGet, "/api/ai/status", nil, http.StatusOK, &status)
	if status.Enabled {
		t.Skip("needs AI off: this install has a model configured")
	}

	ensureHomeClaim(t, c, me.ID)
	runHomeJob(t, s, c, me.ID, "sh", "-c", `mkdir -p "$(dirname "$1")" && printf '%s' "$2" > "$1"`, "seed", "/home/user/"+brokenFile, "import plotly_missing_e2e\n")
	broken := crashingApp(s, "e2e broken", brokenFile)
	s.waitFor(broken.Name, 4*time.Minute, func(a app) bool { return a.Phase == "Failed" })

	var d struct {
		Code      string
		Known     bool
		Bundle    []struct{ Ref, Excerpt string }
		Proposals []struct{ Action, Target, Summary string }
	}
	s.json(http.MethodGet, "/api/workspaces/"+broken.Name+"/diagnosis", nil, http.StatusOK, &d)
	found := false
	for _, e := range d.Bundle {
		found = found || strings.HasPrefix(e.Ref, "log/") && strings.Contains(e.Excerpt, "plotly_missing_e2e")
	}
	if d.Code != "MissingModule" || !found || len(d.Proposals) != 1 || d.Proposals[0].Action != "restart" {
		t.Fatalf("diagnosis %+v", d)
	}

	var plan struct{ ID, State string }
	s.json(http.MethodPost, "/api/plans", map[string]any{"source": "rules", "proposal": map[string]any{"action": "restart", "target": broken.Name}}, http.StatusCreated, &plan)
	s.json(http.MethodPost, "/api/plans/"+plan.ID+"/approve", nil, http.StatusOK, &plan)
	if plan.State != "applied" {
		t.Fatalf("plan %+v", plan)
	}

	t.Run("MCP", func(t *testing.T) {
		tok := s.newAPIToken("e2e agent", "read-write")
		running := s.create(fmt.Sprintf("e2e mcp %d", time.Now().Unix()%100000), "jupyterlab", "")
		t.Cleanup(func() { s.do(http.MethodDelete, "/api/workspaces/"+running.Name, nil) })
		s.waitFor(running.Name, 4*time.Minute, func(a app) bool { return a.Phase == "Running" })

		hc := newClient(t)
		hc.Jar = nil
		hc.Transport = tokenTransport{token: tok.Token, next: hc.Transport}
		cs, err := sdk.NewClient(&sdk.Implementation{Name: "e2e-agent", Version: "1"}, nil).
			Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: uiBase() + "/mcp", HTTPClient: hc, MaxRetries: -1}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer cs.Close()
		res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "stop_workspace", Arguments: map[string]any{"name": running.Name}})
		if err != nil || res.IsError {
			t.Fatalf("stop_workspace: %v %+v", err, res)
		}
		var proposed struct{ PlanID, State string }
		_ = json.Unmarshal([]byte(res.Content[0].(*sdk.TextContent).Text), &proposed)
		if proposed.State != "awaiting_approval" {
			t.Fatalf("proposed %+v", proposed)
		}
		time.Sleep(10 * time.Second)
		var a app
		s.json(http.MethodGet, "/api/workspaces/"+running.Name, nil, http.StatusOK, &a)
		if a.Phase != "Running" {
			t.Fatalf("the agent's proposal stopped the app: %s", a.Phase)
		}
		s.json(http.MethodPost, "/api/plans/"+proposed.PlanID+"/approve", nil, http.StatusOK, &plan)
		s.waitFor(running.Name, 2*time.Minute, func(a app) bool { return a.Phase == "Stopped" })

		resp, _ := newClient(t).Post(uiBase()+"/mcp", "application/json", strings.NewReader(`{}`))
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata") {
			t.Fatalf("no token: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}
		resp.Body.Close()
	})
}

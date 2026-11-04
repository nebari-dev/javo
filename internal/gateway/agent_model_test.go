package gateway_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/ai/agentproxy"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/model"
)

// Only the own token of an app whose template has agentModel reaches the
// model endpoint: not a person's token, not another app's, not anonymous.
func TestOnlyAgentAppsReachTheModelEndpoint(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	p := &agentproxy.Proxy{BaseURL: up.URL, APIKey: "sk-admin", Model: "m", Allows: func(m string) bool { return m == "m" },
		Budget: apitest.Unlimited{}, MaxRequestBytes: 1 << 20}
	f := newAgentFixtureWith(t, true, apitest.Options{AgentModel: p})
	path := agentproxy.Route + "/chat/completions"
	body := `{"model":"m","messages":[]}`
	if w := f.agent("POST", path, body); w.Code != http.StatusOK {
		t.Fatalf("the agent's token: %d %s", w.Code, w.Body)
	}
	personal, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{
		Name: "laptop", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := f.Tokens.Create(context.Background(), model.Actor{System: model.SystemController}, token.Token{
		Name: "plain", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: f.GetWorkspace(apitest.LocalAlice(), "plain").Namespace, Name: f.GetWorkspace(apitest.LocalAlice(), "plain").ObjectName(), Short: "plain"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for what, w := range map[string]*httptest.ResponseRecorder{
		"a personal token":   f.Bearer("POST", path, body, personal, nil),
		"an app's own token": f.Bearer("POST", path, body, plain, nil),
		"a browser session":  f.Do("POST", path, body, f.alice),
		"nobody":             f.Do("POST", path, body, nil),
	} {
		if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d %s", what, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "sk-admin") {
			t.Errorf("%s: the key is in the answer", what)
		}
	}
	if calls != 1 {
		t.Fatalf("the provider was called %d times, want once", calls)
	}
}

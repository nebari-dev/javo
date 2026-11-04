package agentproxy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nebari-dev/javo/internal/ai/agentproxy"
)

const adminKey = "sk-admin-PLACEHOLDER-0451"

// upstream is a fake OpenAI-compatible server; it keeps the last request.
type upstream struct {
	*httptest.Server
	mu     sync.Mutex
	calls  int
	auth   string
	body   map[string]any
	header http.Header
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.calls++
		u.auth = r.Header.Get("Authorization")
		u.header = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&u.body)
		stream, _ := u.body["stream"].(bool)
		u.mu.Unlock()
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, line := range []string{
				`data: {"choices":[{"delta":{"content":"Hel"}}]}`,
				`data: {"choices":[{"delta":{"content":"lo"}}]}`,
				`data: {"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150}}`,
				`data: [DONE]`,
			} {
				_, _ = io.WriteString(w, line+"\n\n")
				w.(http.Flusher).Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"Hello"}}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`)
	}))
	t.Cleanup(u.Close)
	return u
}

func proxyFor(u *upstream, caller bool) http.Handler {
	p := &agentproxy.Proxy{
		BaseURL: u.URL + "/v1", APIKey: adminKey, Model: "admin/model",
		Allows:          func(m string) bool { return m == "admin/model" || m == "admin/other" },
		Models:          []string{"admin/model", "admin/other"},
		MaxRequestBytes: 4096,
		Caller:          func(context.Context) (string, bool) { return "alice", caller },
	}
	mux := http.NewServeMux()
	p.Register(mux)
	return mux
}

func post(h http.Handler, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", agentproxy.Route+"/chat/completions", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer javo_callers-own-token")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func captureLogs(t *testing.T) *bytes.Buffer {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// A request goes to the admin's provider with the admin's key, never the
// caller's token, on the admin's model when it names another, and its usage
// is logged with the person and the model, never the prompt.
func TestForwardsWithTheAdminsKeyAndModel(t *testing.T) {
	logs := captureLogs(t)
	u := newUpstream(t)
	w := post(proxyFor(u, true), `{"model":"someone/else","messages":[{"role":"user","content":"SECRET-PROMPT-TEXT"}],"max_tokens":500}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Hello") {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	if u.auth != "Bearer "+adminKey {
		t.Fatalf("upstream got Authorization %q", u.auth)
	}
	if u.body["model"] != "admin/model" {
		t.Fatalf("upstream model %v, want the admin's", u.body["model"])
	}
	if !strings.Contains(logs.String(), "user=alice") || !strings.Contains(logs.String(), "tokens_in=100") || !strings.Contains(logs.String(), "tokens_out=20") {
		t.Fatalf("no audit line with the usage: %s", logs)
	}
	if strings.Contains(logs.String(), "SECRET-PROMPT-TEXT") || strings.Contains(logs.String(), adminKey) {
		t.Fatalf("a prompt or the key reached the log: %s", logs)
	}
	if strings.Contains(w.Body.String(), adminKey) {
		t.Fatal("the key is in the answer")
	}
	// A model on the admin's list is kept.
	post(proxyFor(u, true), `{"model":"admin/other","messages":[]}`)
	if u.body["model"] != "admin/other" {
		t.Fatalf("upstream model %v, want admin/other", u.body["model"])
	}
}

// A streamed answer comes through as server-sent events, and the usage the
// provider reports at the end is logged.
func TestStreamsAndLogsTheReportedUsage(t *testing.T) {
	logs := captureLogs(t)
	u := newUpstream(t)
	w := post(proxyFor(u, true), `{"model":"admin/model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("answer: %d %v", w.Code, w.Header())
	}
	if !strings.Contains(w.Body.String(), `"content":"Hel"`) || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("stream: %s", w.Body)
	}
	opts, _ := u.body["stream_options"].(map[string]any)
	if opts["include_usage"] != true {
		t.Fatalf("stream_options %v: usage is not asked for", u.body["stream_options"])
	}
	if !strings.Contains(logs.String(), "tokens_in=120") || !strings.Contains(logs.String(), "tokens_out=30") {
		t.Fatalf("usage not logged: %s", logs)
	}
}

// A request above the cap, or from anyone but a coding agent's app, is
// refused before anything is sent.
func TestRefusesLargeRequestsAndOtherCallers(t *testing.T) {
	u := newUpstream(t)
	big := `{"model":"admin/model","messages":[{"role":"user","content":"` + strings.Repeat("a", 5000) + `"}]}`
	if w := post(proxyFor(u, true), big); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large request: %d %s", w.Code, w.Body)
	}
	if w := post(proxyFor(u, false), `{"model":"admin/model","messages":[]}`); w.Code != http.StatusForbidden {
		t.Fatalf("other caller: %d %s", w.Code, w.Body)
	}
	if w := post(proxyFor(u, true), `not json`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d %s", w.Code, w.Body)
	}
	if u.calls != 0 {
		t.Fatalf("calls %d", u.calls)
	}
}

// The model list names only the admin's models.
func TestListsTheAdminsModels(t *testing.T) {
	u := newUpstream(t)
	r := httptest.NewRequest("GET", agentproxy.Route+"/models", nil)
	w := httptest.NewRecorder()
	proxyFor(u, true).ServeHTTP(w, r)
	var got struct {
		Data []struct{ ID string } `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Data) != 2 || got.Data[0].ID != "admin/model" {
		t.Fatalf("models: %d %s", w.Code, w.Body)
	}
	if u.calls != 0 {
		t.Fatal("the provider was asked")
	}
}

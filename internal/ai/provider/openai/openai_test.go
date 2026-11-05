package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/ai/provider"
)

var schema = json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"],"additionalProperties":false}`)

func request() provider.Request {
	return provider.Request{
		Model: "qwen3", System: "You explain app failures.", Task: "Explain.",
		Untrusted: `<untrusted ref="log/app/prev/41">x</untrusted>`, Schema: schema, SchemaName: "explanation", MaxOutputTokens: 2000,
	}
}

const chatOK = `{"id":"c1","object":"chat.completion","created":1,"model":"qwen3","choices":[{"index":0,"finish_reason":"stop",
 "message":{"role":"assistant","content":"{\"summary\":\"plotly is missing\"}"}}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`

const toolOK = `{"id":"c2","object":"chat.completion","created":1,"model":"qwen3","choices":[{"index":0,"finish_reason":"tool_calls",
 "message":{"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"explanation","arguments":"{\"summary\":\"from a tool call\"}"}}]}}],
 "usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`

type recorded struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (r *recorded) add(b map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, b)
}

func newProvider(t *testing.T, url string) provider.Provider {
	t.Helper()
	p, err := New(provider.Config{Provider: "openai", Model: "qwen3", BaseURL: url, APIKey: "k", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func serve(t *testing.T, rec *recorded, handle func(body map[string]any, w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		var b map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &b)
		rec.add(b)
		w.Header().Set("Content-Type", "application/json")
		handle(b, w)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestJSONSchemaStrictOnAV1BaseURL(t *testing.T) {
	rec := &recorded{}
	s := serve(t, rec, func(_ map[string]any, w http.ResponseWriter) { io.WriteString(w, chatOK) })
	r, err := newProvider(t, s.URL+"/v1").Generate(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if string(r.JSON) != `{"summary":"plotly is missing"}` || r.Usage != (provider.Usage{InputTokens: 100, OutputTokens: 20}) {
		t.Fatalf("response %+v", r)
	}
	rf := rec.bodies[0]["response_format"].(map[string]any)
	js := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["strict"] != true || js["name"] != "explanation" {
		t.Errorf("response_format %v", rf)
	}
	msgs, _ := json.Marshal(rec.bodies[0]["messages"])
	if !strings.Contains(string(msgs), `"role":"system"`) || strings.Count(string(msgs), "untrusted ref") != 1 {
		t.Errorf("messages %s", msgs)
	}
}

func TestFallsBackToAFunctionCallWhenJSONSchemaIsRejected(t *testing.T) {
	rec := &recorded{}
	s := serve(t, rec, func(b map[string]any, w http.ResponseWriter) {
		if _, ok := b["response_format"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"response_format json_schema is not supported","type":"invalid_request_error"}}`)
			return
		}
		io.WriteString(w, toolOK)
	})
	p := newProvider(t, s.URL+"/v1")
	for i := 0; i < 2; i++ {
		r, err := p.Generate(context.Background(), request())
		if err != nil || string(r.JSON) != `{"summary":"from a tool call"}` {
			t.Fatalf("call %d: %s %v", i, r.JSON, err)
		}
	}
	// The second call goes straight to the function call.
	if len(rec.bodies) != 3 {
		t.Fatalf("%d requests, want 3 (one rejected, then two function calls)", len(rec.bodies))
	}
	tc := rec.bodies[2]["tool_choice"].(map[string]any)
	if tc["type"] != "function" || tc["function"].(map[string]any)["name"] != "explanation" {
		t.Errorf("tool_choice %v", tc)
	}
}

func TestRateLimitAndOutages(t *testing.T) {
	rec := &recorded{}
	s := serve(t, rec, func(_ map[string]any, w http.ResponseWriter) {
		w.Header().Set("Retry-After", "20")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"slow down","type":"rate_limit"}}`)
	})
	_, err := newProvider(t, s.URL+"/v1").Generate(context.Background(), request())
	var rl *provider.RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter != 20*time.Second {
		t.Fatalf("429: %v", err)
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	if _, err := newProvider(t, closed.URL+"/v1").Generate(context.Background(), request()); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatalf("refused: %v", err)
	}
}

// countingTransport counts the requests that go through it.
type countingTransport struct{ n atomic.Int32 }

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return http.DefaultTransport.RoundTrip(r)
}

// A config's HTTP client sends the requests, so the caller decides where
// they may connect.
func TestRequestsGoThroughTheConfigsClient(t *testing.T) {
	s := serve(t, &recorded{}, func(_ map[string]any, w http.ResponseWriter) { io.WriteString(w, chatOK) })
	tr := &countingTransport{}
	p, err := New(provider.Config{Provider: "openai", Model: "qwen3", BaseURL: s.URL + "/v1", APIKey: "k", HTTPClient: &http.Client{Transport: tr}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(context.Background(), request()); err != nil || tr.n.Load() != 1 {
		t.Fatalf("err %v, requests through the client %d", err, tr.n.Load())
	}
}

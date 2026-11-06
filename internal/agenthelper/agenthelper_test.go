package agenthelper

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seen is what an upstream test server got.
type seen struct {
	method, path, auth, cookie string
	body                       string
}

func upstream(t *testing.T, got *seen) *url.URL {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = seen{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Cookie"), string(b)}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return u
}

func do(t *testing.T, h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://helper"+target, strings.NewReader(`{"a":1}`))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// An API call reaches the gateway with the app's token, whatever the client
// sent as its own credentials.
func TestAPICallsGetTheTokenInsteadOfTheClients(t *testing.T) {
	var got seen
	h := Handler(Config{API: upstream(t, &got), Token: "app-token", Log: quiet()})
	rec := do(t, h, "POST", "/api/workspaces", map[string]string{"Authorization": "Bearer stolen", "Cookie": "s=1"})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got.method != "POST" || got.path != "/api/workspaces" || got.auth != "Bearer app-token" || got.cookie != "" || got.body != `{"a":1}` {
		t.Fatalf("upstream got %+v", got)
	}
}

// Model calls go to the model's base URL with the model key; the base's
// own path is kept.
func TestModelCallsGetTheModelKey(t *testing.T) {
	var api, model seen
	m := upstream(t, &model)
	m.Path = "/api/ai/openai/v1"
	h := Handler(Config{API: upstream(t, &api), Token: "app-token", Model: m, ModelKey: "model-key", Log: quiet()})
	rec := do(t, h, "POST", ModelPrefix+"/chat/completions", map[string]string{"Authorization": "Bearer javo-agent-helper"})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if model.path != "/api/ai/openai/v1/chat/completions" || model.auth != "Bearer model-key" {
		t.Fatalf("model upstream got %+v", model)
	}
	if api.path != "" {
		t.Fatalf("the API got %+v", api)
	}
}

// Nothing but the API and the model goes out: no other path, no path that
// climbs out of /api/, no other host, and no model route without a model.
func TestOtherRequestsAreRefused(t *testing.T) {
	var got seen
	h := Handler(Config{API: upstream(t, &got), Token: "app-token", Log: quiet()})
	for _, target := range []string{
		"/", "/metrics", "/apix", "/api/../admin", "/api/%2e%2e/admin", "/api/x/../../admin",
		ModelPrefix + "/chat/completions",
	} {
		rec := do(t, h, "GET", target, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", target, rec.Code)
		}
	}
	if got.path != "" {
		t.Fatalf("upstream got %+v", got)
	}
}

// A request naming another host reaches only the gateway.
func TestTheHostIsAlwaysTheGateways(t *testing.T) {
	var got seen
	api := upstream(t, &got)
	h := Handler(Config{API: api, Token: "app-token", Log: quiet()})
	req := httptest.NewRequest("GET", "http://helper/api/me", nil)
	req.Host = "evil.example.net"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || got.path != "/api/me" {
		t.Fatalf("status %d, upstream %+v", rec.Code, got)
	}
}

// The log names the method and the path, never a credential or a body.
func TestTheLogHoldsNoSecret(t *testing.T) {
	var got seen
	var buf bytes.Buffer
	h := Handler(Config{API: upstream(t, &got), Token: "app-token-text", Log: slog.New(slog.NewTextHandler(&buf, nil))})
	do(t, h, "POST", "/api/me", map[string]string{"Authorization": "Bearer client-secret"})
	out := buf.String()
	if !strings.Contains(out, "POST") || !strings.Contains(out, "/api/me") {
		t.Fatalf("log %q", out)
	}
	for _, s := range []string{"app-token-text", "client-secret", `"a":1`} {
		if strings.Contains(out, s) {
			t.Fatalf("log holds %q: %q", s, out)
		}
	}
}

// Run serves on a unix socket and on a local TCP address until ctx ends.
func TestRunServesOnTheSocketAndLocally(t *testing.T) {
	var got seen
	cfg := Config{API: upstream(t, &got), Token: "app-token", Log: quiet()}
	dir, err := os.MkdirTemp("", "ah") // short: a socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "api.sock")
	// A socket left by an earlier run does not stop a new one.
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, sock, addr) }()

	unix := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	for _, c := range []struct {
		client *http.Client
		url    string
	}{{unix, "http://socket/api/me"}, {http.DefaultClient, "http://" + addr + "/api/me"}} {
		var res *http.Response
		deadline := time.Now().Add(5 * time.Second)
		for {
			res, err = c.client.Get(c.url)
			if err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 || got.auth != "Bearer app-token" {
			t.Fatalf("%s: status %d, upstream %+v", c.url, res.StatusCode, got)
		}
	}
	if err := Ready(sock); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := Ready(sock); err == nil {
		t.Fatal("Ready after Run ended")
	}
}

// Without a token (an app with no API access) the API route is closed.
func TestNoTokenNoAPI(t *testing.T) {
	var got seen
	h := Handler(Config{API: upstream(t, &got), Log: quiet()})
	if rec := do(t, h, "GET", "/api/me", nil); rec.Code != http.StatusNotFound || got.path != "" {
		t.Fatalf("status %d, upstream %+v", rec.Code, got)
	}
}

// The helper's container gives it the gateway (its address inside the
// cluster first), the token and the model by variables.
func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{
		"JAVO_URL": "https://javo.example.com", "JAVO_API_URL": "http://gateway.javo:8080", "JAVO_TOKEN": "tok",
		"JAVO_AGENT_BASE_URL": "https://llm.example.com/v1", "JAVO_AGENT_KEY": "key",
	}
	c, err := ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.API.String() != "http://gateway.javo:8080" || c.Token != "tok" || c.Model.String() != "https://llm.example.com/v1" || c.ModelKey != "key" {
		t.Fatalf("%+v", c)
	}
	delete(env, "JAVO_API_URL")
	delete(env, "JAVO_AGENT_BASE_URL")
	if c, err = ConfigFromEnv(func(k string) string { return env[k] }); err != nil || c.API.String() != "https://javo.example.com" || c.Model != nil {
		t.Fatalf("%+v, %v", c, err)
	}
	env["JAVO_URL"] = "not a url"
	if _, err := ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("a bad URL is taken")
	}
}

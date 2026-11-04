package gateway_test

import (
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
)

// The gateway's own answers (the UI, the API, health checks) carry the
// browser hardening headers, and the API's are never cached; over https the
// host is pinned to https.
func TestGatewayAnswersCarrySecurityHeaders(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>index</html>"), 0o644)
	f := apitest.New(t, apitest.Options{UiDir: dir})
	c := f.LoginAlice()
	for _, path := range []string{"/", "/api/me", "/api/branding", "/api/nope"} {
		w := f.Do("GET", path, "", c)
		for k, want := range map[string]string{
			"Strict-Transport-Security":  "max-age=31536000",
			"X-Content-Type-Options":     "nosniff",
			"Referrer-Policy":            "same-origin",
			"Cross-Origin-Opener-Policy": "same-origin",
			"X-Frame-Options":            "SAMEORIGIN",
			"Permissions-Policy":         "camera=(), microphone=(), geolocation=(), payment=(), usb=()",
		} {
			if got := w.Header().Get(k); got != want {
				t.Errorf("%s: %s = %q, want %q", path, k, got, want)
			}
		}
	}
	for _, path := range []string{"/api/me", "/api/branding"} {
		if got := f.Do("GET", path, "", c).Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control %q", path, got)
		}
	}
}

// Over plain http there is no HSTS: it would pin a host that has no TLS.
func TestNoHSTSOverPlainHTTP(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Urls: config.URLs{Scheme: "http"}})
	if got := f.Do("GET", "/api/branding", "", nil).Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("HSTS over http: %q", got)
	}
}

// App pages get none of the gateway's own headers: the app decides them.
func TestAppAnswersKeepTheirOwnHeaders(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	f.MountProxy("http://127.0.0.1:1")
	w := f.Do("GET", "/u/someone/app/", "", f.LoginAlice())
	if got := w.Header().Get("Cross-Origin-Opener-Policy"); got != "" {
		t.Fatalf("an app answer got COOP %q", got)
	}
}

// A client that sends a request body slowly is cut off. The API's own
// routes already give a body 5 s (huma); this covers the rest, such as /mcp.
func TestASlowRequestBodyIsCutOff(t *testing.T) {
	old := *gateway.BodyReadTimeout
	*gateway.BodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { *gateway.BodyReadTimeout = old })
	f := apitest.New(t, apitest.Options{Mcp: true})
	tok := f.NewToken(f.LoginAlice(), `{"name":"agent","scope":"read","expiresInDays":1}`).Token
	s := httptest.NewServer(f.Srv)
	t.Cleanup(s.Close)
	conn, err := net.Dial("tcp", s.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "POST /mcp HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nAccept: application/json, text/event-stream\r\n"+
		"Authorization: Bearer "+tok+"\r\nContent-Length: 100\r\n\r\n{\"json")
	_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
	start := time.Now()
	_, err = io.ReadAll(conn)
	if took := time.Since(start); err != nil || took > 2*time.Second {
		t.Fatalf("connection held %s (%v)", took, err)
	}
}

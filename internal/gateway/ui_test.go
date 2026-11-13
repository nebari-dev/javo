package gateway_test

import (
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/branding"
)

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

// The UI may be files that are not a folder: javo local serves the ones
// its binary carries, by the same rules.
func TestUIServesFilesThatAreNotAFolder(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{UI: fstest.MapFS{
		"index.html":    {Data: []byte("<html>index</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}})
	for path, want := range map[string]string{"/": "index", "/apps/new": "index", "/assets/": "index", "/assets/app.js": "console.log"} {
		w := f.Do("GET", path, "", nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: status %d body %q", path, w.Code, w.Body)
		}
		if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("%s: csp %q", path, csp)
		}
	}
	if w := f.Do("GET", "/api/nope", "", nil); strings.Contains(w.Body.String(), "index") {
		t.Errorf("/api/nope fell back to the UI: %d", w.Code)
	}
}

func TestUIServesFilesWithSPAFallbackAndCSP(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>index</html>"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "assets"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log(1)"), 0o644)
	f := apitest.New(t, apitest.Options{UiDir: dir})

	for path, want := range map[string]string{
		"/":              "index",
		"/apps/new":      "index",
		"/assets/":       "index",
		"/assets/app.js": "console.log",
	} {
		w := f.Do("GET", path, "", nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Fatalf("%s: status %d body %q", path, w.Code, w.Body)
		}
		csp := w.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") ||
			!strings.Contains(csp, "base-uri 'none'") || !strings.Contains(csp, "form-action 'self'") {
			t.Fatalf("%s: csp %q", path, csp)
		}
		// Fonts ship with the UI; nothing is loaded from a third-party host.
		if !strings.Contains(csp, "font-src 'self'") || strings.Contains(csp, "https:") {
			t.Fatalf("%s: csp allows a third-party host: %q", path, csp)
		}
	}
	for _, path := range []string{"/api/nope", "/u/alice-1a2b3c4d/x/"} {
		if w := f.Do("GET", path, "", nil); w.Code < 400 || strings.Contains(w.Body.String(), "index") {
			t.Fatalf("%s: status %d body %q", path, w.Code, w.Body)
		}
	}
	if w := f.Do("GET", "/healthz", "", nil); w.Code != http.StatusOK {
		t.Fatalf("healthz %d", w.Code)
	}
}

// The UI frames only raw files of the home (the file viewer), so a page shown
// there cannot navigate its frame elsewhere (to another site, or to an app
// under /u/) with what the viewer typed. A Host that is not a plain host
// name frames nothing.
func TestUIFramesOnlyRawFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>index</html>"), 0o644)
	f := apitest.New(t, apitest.Options{UiDir: dir})
	for host, want := range map[string]string{
		"javo.example.com":      "frame-src javo.example.com/api/files/raw;",
		"javo.example.com:8443": "frame-src javo.example.com:8443/api/files/raw;",
		"x;script-src":          "frame-src 'none';",
	} {
		r := httptest.NewRequest("GET", "/files/view", nil)
		r.Host = host
		w := httptest.NewRecorder()
		f.Srv.ServeHTTP(w, r)
		if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, want) || strings.Contains(csp, "script-src") {
			t.Errorf("host %q: csp %q, want %q", host, csp, want)
		}
	}
}

func TestMountedHandlerTakesPrecedenceOverUI(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{UiDir: t.TempDir()})
	f.Srv.Handle("/u/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	if w := f.Do("GET", "/u/alice-1a2b3c4d/x/", "", nil); w.Code != http.StatusTeapot {
		t.Fatalf("status %d", w.Code)
	}
}

// The shipped icons (ui/public, copied into the build as-is) must be served
// as images; a missing file would fall back to index.html as text/html.
func TestUIServesShippedFaviconsAsImages(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>index</html>"), 0o644)
	for _, name := range []string{"favicon.svg", "favicon.ico", "favicon.png"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "ui", "public", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := apitest.New(t, apitest.Options{UiDir: dir})
	for path, ctype := range map[string]string{"/favicon.svg": "image/svg+xml", "/favicon.ico": "image/", "/favicon.png": "image/png"} {
		w := f.Do("GET", path, "", nil)
		if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), ctype) || strings.Contains(w.Body.String(), "index") {
			t.Fatalf("%s: status %d type %q", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
}

// /readyz is what Kubernetes waits on: it fails while the database is not
// ready (for example the one-time import has not finished), and /healthz
// stays up so the pod is not restarted meanwhile. It is public, so it says
// nothing of why, and it asks the database at most once a second whoever
// floods it.
func TestReadyzFollowsTheDatabase(t *testing.T) {
	t.Parallel()
	var notReady atomic.Bool
	var asked atomic.Int32
	notReady.Store(true)
	now := time.Now()
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	f := apitest.New(t, apitest.Options{Now: clock, Ready: func(context.Context) error {
		asked.Add(1)
		if notReady.Load() {
			return errors.New("pq: relation schema_version at 10.0.0.7 does not exist")
		}
		return nil
	}})
	for range 20 {
		w := f.Do("GET", "/readyz", "", nil)
		if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "10.0.0.7") || strings.Contains(w.Body.String(), "schema_version") {
			t.Fatalf("not ready: status %d body %q", w.Code, w.Body)
		}
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("20 probes asked the database %d times", n)
	}
	if w := f.Do("GET", "/healthz", "", nil); w.Code != http.StatusOK {
		t.Fatalf("healthz while not ready: %d", w.Code)
	}
	notReady.Store(false)
	mu.Lock()
	now = now.Add(2 * time.Second)
	mu.Unlock()
	if w := f.Do("GET", "/readyz", "", nil); w.Code != http.StatusOK {
		t.Fatalf("ready: status %d body %q", w.Code, w.Body)
	}
}

func TestUICSPAllowsOnlyTheBrandingImageHosts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>index</html>"), 0o644)
	var c config.BrandingCfg
	c.Logo.Light.URL = "https://cdn.example.com/logo.svg"
	b, err := branding.Load(c)
	if err != nil {
		t.Fatal(err)
	}
	csp := apitest.New(t, apitest.Options{UiDir: dir, Branding: b}).Do("GET", "/", "", nil).Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' https://cdn.example.com;") || !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("csp %q", csp)
	}
	if strings.Contains(strings.Replace(csp, "img-src 'self' https://cdn.example.com;", "", 1), "https:") {
		t.Fatalf("csp allows another host: %q", csp)
	}
}

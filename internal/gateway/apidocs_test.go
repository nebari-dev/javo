package gateway_test

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/authn"
)

func TestDocsOffByDefault(t *testing.T) {
	f := apitest.New(t, apitest.Options{UiDir: docsUI(t)})
	for _, p := range []string{"/api/docs", "/api/docs/", "/api/docs/swagger-ui-bundle.js", "/api/docs/init.js"} {
		w := f.Do("GET", p, "", f.Session(apitest.LocalAlice()))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s with docs off: %d", p, w.Code)
		}
	}
}

func TestDocsServedWithCSP(t *testing.T) {
	f := apitest.New(t, apitest.Options{UiDir: docsUI(t), ApiDocs: true})
	if w := f.Do("GET", "/api/docs", "", nil); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/api/docs/" {
		t.Fatalf("/api/docs: %d %v", w.Code, w.Header())
	}
	// No sign-in needed: the document it shows is public too.
	w := f.Do("GET", "/api/docs/", "", nil)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("page: %d %v", w.Code, w.Header())
	}
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self'", "frame-ancestors 'none'", "connect-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe") {
		t.Fatalf("CSP %q allows unsafe sources", csp)
	}
	html := w.Body.String()
	if regexp.MustCompile(`<script(\s[^>]*)?>[^<]`).MatchString(html) || strings.Contains(html, "<script>") {
		t.Fatalf("inline script in %s", html)
	}
	if regexp.MustCompile(`(src|href)="(https?:)?//`).MatchString(html) {
		t.Fatalf("external asset in %s", html)
	}
	for _, asset := range []string{"swagger-ui-bundle.js", "swagger-ui.css", "init.js"} {
		if !strings.Contains(html, `"`+asset+`"`) {
			t.Fatalf("page does not load %s: %s", asset, html)
		}
		w := f.Do("GET", "/api/docs/"+asset, "", nil)
		if w.Code != http.StatusOK || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: %d %v", asset, w.Code, w.Header())
		}
	}
	init := f.Do("GET", "/api/docs/init.js", "", nil)
	if !strings.Contains(init.Header().Get("Content-Type"), "javascript") || !strings.Contains(init.Body.String(), "/api/openapi.json") ||
		!strings.Contains(init.Body.String(), "validatorUrl: null") {
		t.Fatalf("init.js: %v %s", init.Header(), init.Body)
	}
	if w := f.Do("GET", "/api/docs/../me", "", nil); w.Code == http.StatusOK {
		t.Fatalf("path escape answered %d", w.Code)
	}
	if w := f.Do("GET", "/api/docs/nope.js", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown asset: %d", w.Code)
	}
}

func TestDocumentOffersBearerAuthorization(t *testing.T) {
	f := apitest.New(t, apitest.Options{})
	doc := apitest.Decode[struct {
		Components struct {
			SecuritySchemes map[string]struct{ Type, Scheme string }
		}
	}](t, f.Do("GET", "/api/openapi.json", "", nil))
	s, ok := doc.Components.SecuritySchemes[authn.BearerScheme]
	if !ok || s.Type != "http" || s.Scheme != "bearer" {
		t.Fatalf("security schemes %+v", doc.Components.SecuritySchemes)
	}
}

// docsUI is a built UI folder with stand-ins for the Swagger UI assets.
func docsUI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>ui</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, gateway.APIDocsAssets), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"swagger-ui-bundle.js": "window.SwaggerUIBundle=function(){}", "swagger-ui.css": "body{}"} {
		if err := os.WriteFile(filepath.Join(dir, gateway.APIDocsAssets, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

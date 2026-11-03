package branding_test

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/branding"
)

const svgLogo = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10"/></svg>`

// A 1x1 PNG.
var pngIcon = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

func file(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

type view struct {
	Name      string `json:"name"`
	LogoLight string `json:"logoLight"`
	LogoDark  string `json:"logoDark"`
	Favicon   string `json:"favicon"`
	Accent    string `json:"accent"`
}

func TestBrandingWithoutConfigIsJavoAndPublic(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	w := f.Do("GET", "/api/branding", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if got := apitest.Decode[view](t, w); got != (view{Name: "Javo"}) {
		t.Fatalf("branding = %+v", got)
	}
	if w := f.Do("GET", "/branding/logo-light", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("unset logo: status %d", w.Code)
	}
}

func TestConfiguredBrandingIsServedWithoutSignIn(t *testing.T) {
	t.Parallel()
	var c config.BrandingCfg
	c.Name, c.Accent = "Acme Lab", "#0a7d55"
	c.Logo.Light.File = file(t, "logo.svg", []byte(svgLogo))
	c.Logo.Dark.URL = "https://cdn.example.com/logo-dark.svg"
	c.Favicon.File = file(t, "favicon.png", pngIcon)
	b, err := branding.Load(c)
	if err != nil {
		t.Fatal(err)
	}
	f := apitest.New(t, apitest.Options{Branding: b})

	got := apitest.Decode[view](t, f.Do("GET", "/api/branding", "", nil))
	if got.Name != "Acme Lab" || got.Accent != "#0a7d55" || got.LogoDark != "https://cdn.example.com/logo-dark.svg" ||
		!strings.HasPrefix(got.LogoLight, "/branding/logo-light?v=") || !strings.HasPrefix(got.Favicon, "/branding/favicon?v=") {
		t.Fatalf("branding = %+v", got)
	}

	w := f.Do("GET", got.LogoLight, "", nil)
	if w.Code != http.StatusOK || w.Body.String() != svgLogo {
		t.Fatalf("logo: status %d body %q", w.Code, w.Body)
	}
	h := w.Header()
	// An SVG opened on its own must not run script or load anything.
	if h.Get("Content-Type") != "image/svg+xml" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Content-Security-Policy") != "default-src 'none'; style-src 'unsafe-inline'" {
		t.Fatalf("logo headers %v", h)
	}
	if !strings.Contains(h.Get("Cache-Control"), "immutable") {
		t.Fatalf("a versioned logo should be cached: %q", h.Get("Cache-Control"))
	}
	if cc := f.Do("GET", "/branding/logo-light", "", nil).Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("an unversioned logo must be revalidated: %q", cc)
	}

	w = f.Do("GET", got.Favicon, "", nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), pngIcon) {
		t.Fatalf("favicon: status %d type %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w := f.Do("GET", "/branding/logo-dark", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("a URL logo is not served by the gateway: status %d", w.Code)
	}
}

func TestLoadRefusesFilesThatAreNotSmallImages(t *testing.T) {
	t.Parallel()
	for name, data := range map[string][]byte{
		"oversized": append([]byte(svgLogo), bytes.Repeat([]byte(" "), branding.MaxImageBytes)...),
		"html":      []byte("<!doctype html><html><script>alert(1)</script></html>"),
		"text":      []byte("just words"),
		"empty":     {},
	} {
		t.Run(name, func(t *testing.T) {
			var c config.BrandingCfg
			c.Logo.Light.File = file(t, "logo", data)
			if _, err := branding.Load(c); err == nil || !strings.Contains(err.Error(), "branding.logo.light") {
				t.Fatalf("err = %v, want one naming branding.logo.light", err)
			}
		})
	}
	var c config.BrandingCfg
	c.Favicon.File = filepath.Join(t.TempDir(), "missing.ico")
	if _, err := branding.Load(c); err == nil || !strings.Contains(err.Error(), "branding.favicon") {
		t.Fatalf("missing file: err = %v", err)
	}
}

func TestImageOriginsAreTheURLImagesHosts(t *testing.T) {
	t.Parallel()
	var c config.BrandingCfg
	c.Logo.Light.URL = "https://cdn.example.com/a/logo.svg"
	c.Logo.Dark.URL = "https://cdn.example.com/b/logo.svg"
	c.Favicon.URL = "https://icons.example.org:8443/f.ico"
	b, err := branding.Load(c)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(b.ImageOrigins(), " ")
	if got != "https://cdn.example.com https://icons.example.org:8443" {
		t.Fatalf("origins = %q", got)
	}
}

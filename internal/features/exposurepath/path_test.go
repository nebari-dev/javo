package exposurepath

import (
	"net/http/httptest"
	"testing"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/naming"
)

func TestURLForOnAppsHostIsAbsolute(t *testing.T) {
	b := New(config.URLs{AppsHost: "apps.example.com", Scheme: "https"})
	u := b.URLFor("alice-1a2b3c4d", "sales-app", naming.ObjectName("alice-1a2b3c4d", "sales-app"))
	if got, want := u.String(), "https://apps.example.com/u/alice-1a2b3c4d/sales-app/"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if got := b.BasePath("alice-1a2b3c4d", "sales-app"); got != "/u/alice-1a2b3c4d/sales-app/" {
		t.Fatalf("base path %s", got)
	}
}

func TestURLForOnTheUIHostIsRelative(t *testing.T) {
	u := New(config.URLs{}).URLFor("alice-1a2b3c4d", "sales-app", "x")
	if got := u.String(); got != "/u/alice-1a2b3c4d/sales-app/" {
		t.Fatalf("got %s", got)
	}
}

func TestMatch(t *testing.T) {
	on := New(config.URLs{AppsHost: "apps.example.com"})
	cases := []struct {
		name, host, path string
		backend          *Backend
		ok               bool
		rest             string
	}{
		{"app root", "apps.example.com", "/u/alice-1/sales/", on, true, ""},
		{"nested", "apps.example.com", "/u/alice-1/sales/lab/tree", on, true, "lab/tree"},
		{"host case", "Apps.Example.com", "/u/alice-1/sales/", on, true, ""},
		{"other host", "javo.example.com", "/u/alice-1/sales/", on, false, ""},
		{"no app name", "apps.example.com", "/u/alice-1", on, false, ""},
		{"not under /u/", "apps.example.com", "/api/me", on, false, ""},
		{"one host any host", "javo.example.com", "/u/alice-1/sales/x", New(config.URLs{}), true, "x"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", c.path, nil)
		r.Host = c.host
		got, ok := c.backend.Match(r)
		if ok != c.ok || ok && (got.Owner != "alice-1" || got.Name != "sales" || got.Rest != c.rest ||
			got.ObjectName != naming.ObjectName("alice-1", "sales")) {
			t.Errorf("%s: got %+v %v", c.name, got, ok)
		}
	}
}

func TestSeparateHostAndCookieScope(t *testing.T) {
	if New(config.URLs{}).SeparateHost() {
		t.Fatal("one host reported as separate")
	}
	if New(config.URLs{AppsHost: "javo.example.com", UIHost: "javo.example.com"}).SeparateHost() {
		t.Fatal("apps host equal to the UI host reported as separate")
	}
	b := New(config.URLs{AppsHost: "apps.example.com", UIHost: "javo.example.com"})
	if !b.SeparateHost() {
		t.Fatal("separate apps host not reported")
	}
	d, p := b.CookieScope(exposure.Target{ObjectName: naming.ObjectName("alice-1", "sales"), Owner: "alice-1", Name: "sales"})
	// No trailing slash: a browser sends a cookie whose path ends in "/" only
	// below that slash, so the bare app address would arrive with no session.
	if d != "" || p != "/u/alice-1/sales" {
		t.Fatalf("scope %q %q", d, p)
	}
}

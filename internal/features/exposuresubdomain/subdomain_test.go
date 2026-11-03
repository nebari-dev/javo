package exposuresubdomain

import (
	"net/http/httptest"
	"testing"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/naming"
)

func TestURLForIsTheObjectNameUnderTheAppsDomain(t *testing.T) {
	b := New(config.URLs{AppsDomain: "apps.example.com", Scheme: "https"})
	obj := naming.ObjectName("alice-1a2b3c4d", "sales-app")
	u := b.URLFor("alice-1a2b3c4d", "sales-app", obj)
	if got, want := u.String(), "https://alice-1a2b3c4d-sales-app.apps.example.com/"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if got := b.BasePath("alice-1a2b3c4d", "sales-app"); got != "/" {
		t.Fatalf("base path %q", got)
	}
}

func TestMatchReadsTheObjectNameFromTheHost(t *testing.T) {
	b := New(config.URLs{AppsDomain: "apps.example.com"})
	cases := []struct {
		host, path string
		ok         bool
		obj, rest  string
	}{
		{"alice-1-sales.apps.example.com", "/", true, "alice-1-sales", ""},
		{"Alice-1-Sales.Apps.Example.com", "/lab/tree", true, "alice-1-sales", "lab/tree"},
		{"apps.example.com", "/", false, "", ""},
		{"a.b.apps.example.com", "/", false, "", ""},
		{"alice-1-sales.apps.example.com.evil.test", "/", false, "", ""},
		{"javo.example.com", "/", false, "", ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", c.path, nil)
		r.Host = c.host
		got, ok := b.Match(r)
		if ok != c.ok || got.ObjectName != c.obj || got.Rest != c.rest {
			t.Errorf("%s%s: got %+v %v", c.host, c.path, got, ok)
		}
	}
}

func TestAlwaysSeparateAndCookieIsHostOnly(t *testing.T) {
	b := New(config.URLs{AppsDomain: "apps.example.com"})
	if !b.SeparateHost() {
		t.Fatal("subdomain mode is not separate")
	}
	if d, p := b.CookieScope(exposure.Target{ObjectName: "x"}); d != "" || p != "/" {
		t.Fatalf("scope %q %q", d, p)
	}
}

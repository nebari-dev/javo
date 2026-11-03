package servercmd

import (
	"net/netip"
	"slices"
	"testing"
)

func TestParseTrustedProxies(t *testing.T) {
	got, err := parseTrustedProxies([]string{"10.42.0.1/32", " 2001:db8::/32 "})
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.42.0.1/32"), netip.MustParsePrefix("2001:db8::/32")}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{"10.42.0.1", "nonsense", "10.42.0.0/99"} {
		if _, err := parseTrustedProxies([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

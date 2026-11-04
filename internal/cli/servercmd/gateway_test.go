package servercmd

import (
	"net/netip"
	"os"
	"slices"
	"strings"
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

// A short proxy secret could be guessed by the traffic it keeps out, so the
// gateway refuses to start with one.
func TestProxySecretMustBeLong(t *testing.T) {
	dir := t.TempDir()
	short, long := dir+"/short", dir+"/long"
	if err := os.WriteFile(short, []byte("hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(long, []byte(strings.Repeat("k", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProxySecret(short); err == nil {
		t.Error("a 7-character proxy secret was accepted")
	}
	got, err := readProxySecret(long)
	if err != nil || string(got) != strings.Repeat("k", 40) {
		t.Errorf("long secret: %q, %v", got, err)
	}
}

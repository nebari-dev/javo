package ratelimit

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.42.0.1/32"), netip.MustParsePrefix("10.43.0.0/16")}
	cases := []struct {
		name    string
		remote  string
		xff     []string
		trusted []netip.Prefix
		want    string
	}{
		{"no proxies trusted ignores the header", "192.0.2.1:1234", []string{"198.51.100.9"}, nil, "192.0.2.1"},
		{"untrusted peer ignores the header", "192.0.2.1:1234", []string{"198.51.100.9"}, trusted, "192.0.2.1"},
		{"trusted peer without header", "10.42.0.1:1234", nil, trusted, "10.42.0.1"},
		{"trusted peer gives the client", "10.42.0.1:1234", []string{"198.51.100.9"}, trusted, "198.51.100.9"},
		{"spoofed left entries are skipped", "10.42.0.1:1234", []string{"203.0.113.66, 198.51.100.9"}, trusted, "198.51.100.9"},
		{"trusted hops are skipped", "10.42.0.1:1234", []string{"198.51.100.9, 10.43.2.3"}, trusted, "198.51.100.9"},
		{"several header lines are one list", "10.42.0.1:1234", []string{"203.0.113.66", "198.51.100.9"}, trusted, "198.51.100.9"},
		{"garbage from the proxy falls back to the peer", "10.42.0.1:1234", []string{"198.51.100.9, nonsense"}, trusted, "10.42.0.1"},
		{"all hops trusted gives the leftmost", "10.42.0.1:1234", []string{"10.43.0.7, 10.43.0.8"}, trusted, "10.43.0.7"},
		{"ipv6 client", "10.42.0.1:1234", []string{"2001:db8::1"}, trusted, "2001:db8::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remote
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := ClientIP(r, tc.trusted); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

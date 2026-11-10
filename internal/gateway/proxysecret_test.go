package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/ratelimit"
)

// A trusted proxy's address can be shared by traffic that does not come
// through it (on k3s, a pod calling the node's NodePort arrives from the
// bridge address the proxy also uses). With a proxy secret, X-Forwarded-For
// and X-Forwarded-Proto are believed only on requests that carry it; the
// secret never reaches anything behind the gateway (apps would learn it).
func TestForwardedForNeedsTheProxySecret(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.42.0.1/32")}
	var client, seen, proto string
	h := RequireProxySecret([]byte("s3cret-from-the-proxy"), http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		client, seen, proto = ratelimit.ClientIP(r, trusted), r.Header.Get(ProxySecretHeader), r.Header.Get("X-Forwarded-Proto")
	}))
	for _, c := range []struct {
		name, secret, want, wantProto string
	}{
		{"from the proxy", "s3cret-from-the-proxy", "203.0.113.5", "https"},
		{"no secret", "", "10.42.0.1", ""},
		{"wrong secret", "guess", "10.42.0.1", ""},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/login", nil)
		r.RemoteAddr = "10.42.0.1:51234"
		r.Header.Set("X-Forwarded-For", "203.0.113.5")
		r.Header.Set("X-Forwarded-Proto", "https")
		if c.secret != "" {
			r.Header.Set(ProxySecretHeader, c.secret)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		if client != c.want {
			t.Errorf("%s: client %s, want %s", c.name, client, c.want)
		}
		if proto != c.wantProto {
			t.Errorf("%s: X-Forwarded-Proto %q, want %q", c.name, proto, c.wantProto)
		}
		if seen != "" {
			t.Errorf("%s: the secret header reached the handler", c.name)
		}
	}
}

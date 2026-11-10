package gateway

import (
	"crypto/subtle"
	"net/http"
)

// ProxySecretHeader carries the secret a trusted reverse proxy adds to every
// request it forwards (gateway.trustedProxySecret).
const ProxySecretHeader = "X-Javo-Proxy-Secret"

// RequireProxySecret believes X-Forwarded-For and X-Forwarded-Proto only on
// requests that carry secret in ProxySecretHeader: a trusted proxy's address
// alone can be shared by traffic that does not pass through it (on k3s, a
// pod calling a NodePort arrives from the same bridge address as a proxy
// that forwards to it). The header is removed from every request, so nothing behind the gateway (an
// app) ever sees the secret.
func RequireProxySecret(secret []byte, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(ProxySecretHeader)
		r.Header.Del(ProxySecretHeader)
		if subtle.ConstantTimeCompare([]byte(got), secret) != 1 {
			r.Header.Del("X-Forwarded-For")
			r.Header.Del("X-Forwarded-Proto")
		}
		next.ServeHTTP(w, r)
	})
}

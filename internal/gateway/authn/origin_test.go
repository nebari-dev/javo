package authn_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/authn"
)

func TestSameOrigin(t *testing.T) {
	t.Parallel()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := authn.SameOrigin(ok)
	ui := map[string]string{authn.RequestHeader: "1"}
	with := func(h map[string]string) map[string]string {
		out := map[string]string{authn.RequestHeader: "1"}
		for k, v := range h {
			out[k] = v
		}
		return out
	}
	cases := []struct {
		name, method string
		headers      map[string]string
		want         int
	}{
		{"referer inside an app", "POST", with(map[string]string{"Referer": "https://host/u/alice-1a2b3c4d/x/"}), http.StatusForbidden},
		{"encoded app referer", "POST", with(map[string]string{"Referer": "https://host/%75/alice-1a2b3c4d/x/"}), http.StatusForbidden},
		{"cross-site fetch", "POST", with(map[string]string{"Sec-Fetch-Site": "cross-site"}), http.StatusForbidden},
		{"same-site fetch", "DELETE", with(map[string]string{"Sec-Fetch-Site": "same-site"}), http.StatusForbidden},
		{"normal UI request", "POST", with(map[string]string{"Referer": "https://host/apps/new", "Sec-Fetch-Site": "same-origin"}), http.StatusNoContent},
		{"UI request with no browser headers", "POST", ui, http.StatusNoContent},
		{"foreign Origin without Sec-Fetch-Site", "POST", with(map[string]string{"Origin": "https://evil.example.org"}), http.StatusForbidden},
		{"null Origin", "POST", with(map[string]string{"Origin": "null"}), http.StatusForbidden},
		{"own Origin", "POST", with(map[string]string{"Origin": "http://example.com"}), http.StatusNoContent},
		{"read from inside an app", "GET", map[string]string{"Referer": "https://host/u/alice-1a2b3c4d/x/"}, http.StatusNoContent},
		// A form or a plain fetch from another site cannot add the header
		// without a CORS preflight, which the gateway never answers.
		{"no UI header", "POST", nil, http.StatusForbidden},
		{"no UI header, same origin, no referer", "DELETE", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://example.com"}, http.StatusForbidden},
		{"UI header with another value", "POST", map[string]string{authn.RequestHeader: "yes"}, http.StatusForbidden},
		{"bearer token without the UI header", "POST", map[string]string{"Authorization": "Bearer javo_x"}, http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/api/workspaces", nil)
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestServerAppliesSameOriginToAPI(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d", w.Code)
	}
}

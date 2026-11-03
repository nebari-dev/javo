package authn

import (
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/reasons"
)

// SameOrigin guards state-changing API requests on a single host where
// apps share the gateway's origin. Safe methods pass. It
// rejects a request whose Sec-Fetch-Site header is present and not
// "same-origin"; when that header is absent, one whose Origin header is
// present and names another host; and one whose Referer path is under /u/
// (a page served by an app). These checks protect the session cookie, which
// a browser attaches by itself; a request with a bearer token is not checked
// here, since the API then ignores its cookies and a browser never adds that
// header on its own.
func SameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := BearerToken(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		if !sameOrigin(r) {
			problem.Write(w, http.StatusForbidden, reasons.Msg(reasons.MsgNotAllowedFromApp))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" {
		if s != "same-origin" {
			return false
		}
	} else if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		if err != nil || strings.HasPrefix(path.Clean("/"+u.Path)+"/", "/u/") {
			return false
		}
	}
	return true
}

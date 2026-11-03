package authn

import (
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/reasons"
)

// RequestHeader is the header the web UI sends, with the value "1", on every
// API request. Another site cannot add a custom header to a request without
// a CORS preflight, which the gateway never allows, and an HTML form cannot
// add one at all.
const RequestHeader = "X-Javo-Request"

// SameOrigin guards state-changing API requests that the browser may have
// sent by itself with the session cookie. Safe methods pass. It rejects a
// request without RequestHeader; one whose Sec-Fetch-Site header is present
// and not "same-origin"; when that header is absent, one whose Origin header
// is present and names another host; and one whose Referer path is under /u/
// (a page served by an app). A request with a bearer token is not checked
// here, since the API then ignores its cookies and a browser never adds that
// header on its own.
//
// These checks stop other sites. They cannot stop a page served under /u/
// on the UI host itself: it shares the UI's origin, so its script can send
// any header and leave out its Referer. That is why the proxy keeps such
// pages away from platform admins and fork previews (see proxy.New), and why
// one-host mode is only for apps everyone trusts.
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
		if r.Header.Get(RequestHeader) != "1" || !sameOrigin(r) {
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

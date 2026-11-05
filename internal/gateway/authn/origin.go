package authn

import (
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

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
		if r.Header.Get(RequestHeader) != "1" || !sameOrigin(r.Header.Get, r.Host) {
			problem.Write(w, http.StatusForbidden, reasons.Msg(reasons.MsgNotAllowedFromApp))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin is true unless a request's headers (header) say a page of
// another origin or one under /u/ sent it to host.
func sameOrigin(header func(string) string, host string) bool {
	if s := header("Sec-Fetch-Site"); s != "" {
		if s != "same-origin" {
			return false
		}
	} else if o := header("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, host) {
			return false
		}
	}
	if ref := header("Referer"); ref != "" {
		u, err := url.Parse(ref)
		if err != nil || strings.HasPrefix(path.Clean("/"+u.Path)+"/", "/u/") {
			return false
		}
	}
	return true
}

// HomeReads are the GET operations that can make the caller's home or start
// its file pod, with the test that says whether a request of that operation
// does (a nil test: always). HomeReadsGuard keeps pages of apps and of other
// sites from sending them.
var HomeReads = map[string]func(huma.Context) bool{
	"listFiles":  nil,
	"getRawFile": nil,
	"getStorage": func(hctx huma.Context) bool {
		usage, _ := strconv.ParseBool(hctx.Query("usage"))
		return usage
	},
}

// PlainHomeReads are the HomeReads the web UI also loads without its header,
// as an image, a frame, a download or a link: they need only a request from
// the UI's own pages (sameOrigin).
var PlainHomeReads = []string{"getRawFile"}

// HomeReadsGuard is API middleware that refuses a HomeReads request sent
// with the session cookie unless it carries RequestHeader (except
// PlainHomeReads) and passes the SameOrigin checks. Browsers send the
// session cookie with an image or a script that a page of an app on another
// host of the same site loads, so without it such a page could make a
// person's home and keep a file pod running. A request with an API token is
// not checked, as for SameOrigin.
func HomeReadsGuard(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(hctx huma.Context, next func(huma.Context)) {
		op := hctx.Operation().OperationID
		test, guarded := HomeReads[op]
		if !guarded || test != nil && !test(hctx) || viaToken(hctx.Context()) ||
			(hctx.Header(RequestHeader) == "1" || slices.Contains(PlainHomeReads, op)) && sameOrigin(hctx.Header, hctx.Host()) {
			next(hctx)
			return
		}
		hctx.SetHeader("Content-Type", "application/problem+json")
		hctx.SetStatus(http.StatusForbidden)
		_ = api.Marshal(hctx.BodyWriter(), "application/problem+json", problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgNotAllowedFromApp)))
	}
}

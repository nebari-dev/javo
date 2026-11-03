package proxy

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/nebari-dev/javo/internal/reasons"
)

// fromAnotherApp is true when a browser request for the app at base (its
// base path, "/u/<owner>/<app>/") was started by a page that is not that
// app's own, while apps share one origin. A page script cannot change the
// Sec-Fetch headers. It can drop the Referer, and it can make the Referer
// any path of its own origin by moving its address with history.pushState,
// so the Referer checks below stop pages that do not try; only an origin per
// app (subdomain mode) stops pages that do.
//
// It refuses:
//   - embedding the app (a frame, iframe, embed or object) from any page but
//     its own, on any site;
//   - a same-origin request that is not a top-level navigation, unless its
//     Referer is a page of the app itself. A missing Referer, or one cut to
//     the origin, cannot be told apart from a page that asked for that, and
//     nothing else a same-origin request carries says which page sent it
//     (the app's cookie goes with every request to the app's path, whoever
//     starts it). Browsers send the full Referer to the same origin by
//     default. The web UI on one host makes no such requests to apps.
//
// It lets through:
//   - requests without Sec-Fetch headers: clients other than browsers, which
//     have no ambient cookie of the visitor to abuse;
//   - top-level navigations (a link, a new tab, a typed address);
//   - requests from another site or the same site, which the browser's
//     SameSite cookie rules already keep from carrying the visitor's session;
//   - websocket handshakes, which by standard carry no Referer at all.
func fromAnotherApp(r *http.Request, base string) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	if site == "" {
		return false
	}
	mode, dest := r.Header.Get("Sec-Fetch-Mode"), r.Header.Get("Sec-Fetch-Dest")
	ref, hasRef := referer(r)
	ownPage := hasRef && strings.EqualFold(ref.Host, r.Host) && inApp(ref.Path, base)
	switch dest {
	case "iframe", "frame", "embed", "object":
		return !ownPage
	}
	if mode == "navigate" || mode == "websocket" || site != "same-origin" {
		return false
	}
	return !ownPage
}

// referer is the request's Referer, when it has one that parses as an
// absolute URL.
func referer(r *http.Request) (*url.URL, bool) {
	u, err := url.Parse(r.Header.Get("Referer"))
	if err != nil || u.Host == "" {
		return nil, false
	}
	return u, true
}

// inApp is true when path is the app's base path or below it; base ends in
// "/", so a sibling whose name only starts the same is not in the app.
func inApp(path, base string) bool {
	return strings.HasPrefix(path+"/", base)
}

// refuseFromAnotherApp answers a request fromAnotherApp refused: the branded
// page for a navigation (it shows in the frame), plain text otherwise.
func refuseFromAnotherApp(w http.ResponseWriter, r *http.Request) {
	msg := reasons.Msg(reasons.MsgOpenedFromAnotherApp)
	if r.Header.Get("Sec-Fetch-Mode") == "navigate" {
		writePage(w, http.StatusForbidden, page{Title: msg, Link: "/apps", LinkText: "See your apps"})
		return
	}
	http.Error(w, msg, http.StatusForbidden)
}

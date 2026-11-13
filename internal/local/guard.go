//go:build linux || darwin

package local

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Guard answers only what a browser sends to this Javo from this Javo's
// own pages, on the loopback address addr (127.0.0.1 and a port). Other
// users of the machine, and every page open in the person's browser, can
// reach a loopback port, so two kinds of request are refused before
// anything else sees them:
//
//   - one whose Host is not addr, or localhost and the same port. A page
//     of another site that makes its name resolve to 127.0.0.1 (DNS
//     rebinding) is, to the browser, the same origin as itself, so only
//     the name it asked for tells it apart;
//   - one a page of another origin started: any Sec-Fetch-Site but
//     same-origin and none (a typed address, a bookmark) unless it is a
//     top-level GET (a link), and, from a browser that sends no such
//     header, any Origin but this Javo's. Another port of 127.0.0.1 is
//     another origin and the same site: the browser sends it the session
//     cookie, so SameSite does not stop a page served there, such as an
//     app reached at its own port.
//
// The gateway already refuses a state-changing API request without the
// UI's header or from another origin (authn.SameOrigin), and keeps apps
// from each other (proxy). It checks no Host, and lets another origin's
// GET and websocket through: on a server those come from other machines'
// pages without the cookie. A program that is not a browser sends neither
// header and is not refused here; it has no cookie either.
func Guard(addr string, next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(addr)
	hosts := map[string]bool{addr: true, net.JoinHostPort("localhost", port): true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case !hosts[strings.ToLower(r.Host)]:
			http.Error(w, "This Javo answers at http://"+addr+" only.", http.StatusForbidden)
		case fromAnotherOrigin(r, hosts):
			http.Error(w, "This Javo answers its own pages only, not requests another site's page sends.", http.StatusForbidden)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func fromAnotherOrigin(r *http.Request, hosts map[string]bool) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return false
	case "":
		origin := r.Header.Get("Origin")
		if origin == "" {
			return false
		}
		u, err := url.Parse(origin)
		return err != nil || u.Scheme != "http" || !hosts[strings.ToLower(u.Host)]
	}
	link := r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document" &&
		(r.Method == http.MethodGet || r.Method == http.MethodHead)
	return !link
}

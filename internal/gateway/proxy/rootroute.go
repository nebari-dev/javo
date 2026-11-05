package proxy

import (
	"context"
	"encoding/base64"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/model"
)

// Root requests. In path mode an app is served under /u/<owner>/<app>/, and a
// template with basePath Strip has the prefix removed, so the app believes it
// sits at "/". Its pages then often name absolute paths ("/assets/x.js",
// fetch("/api/..."), a websocket to "/ws"), which the browser asks for
// outside the prefix. The proxy takes such a request (a root request) as the
// app's own when:
//
//   - its Referer is a page of the app on the same host (the Referer decides
//     whenever there is one), or
//   - it has no Referer (websockets never carry one) and the browser holds
//     the route cookie, which the gateway sealed when it served the app's
//     last page this browser opened.
//
// It is then decided and proxied exactly as a request for the same path
// under the app's prefix: the same authz, cross-app guard and response
// rules. Requests from another site never count, and an app whose template
// keeps the prefix gets none (its links carry the prefix already).
//
// On an apps host the app's session cookie lives on the app's path, which a
// root request does not match, so the gateway also gives each app a session
// cookie for "/" (the root session cookie) holding the same app session.

// routeCookieName names the app a browser opened last; rootSessionPrefix
// starts the name of each app's root session cookie.
const (
	routeCookieName   = model.ProductName + "_app_route"
	rootSessionPrefix = model.ProductName + "_app_root_"
)

// routeTTL is how long a route cookie names its app.
const routeTTL = time.Hour

type rootKey struct{}

// isRoot is true for a root request taken as an app's (see atApp).
func isRoot(r *http.Request) bool {
	v, _ := r.Context().Value(rootKey{}).(bool)
	return v
}

// ClaimsRoot is true when r, a request outside /u/, is a root request of an
// app (by its Referer or the route cookie). The gateway asks it on the UI
// host, for paths it does not serve itself, before it sends r to the proxy.
func (p *proxy) ClaimsRoot(r *http.Request) bool {
	_, ok := p.rootTarget(r)
	return ok
}

// rootTarget is the app a root request is for, in path mode only.
func (p *proxy) rootTarget(r *http.Request) (exposure.Target, bool) {
	if p.cfg.Exposure.BasePath("o", "n") == "/" || r.URL.Path == "/u" || strings.HasPrefix(r.URL.Path, "/u/") {
		return exposure.Target{}, false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return exposure.Target{}, false
	}
	if r.Header.Get("Referer") != "" {
		ref, ok := referer(r)
		if !ok || !strings.EqualFold(ref.Host, r.Host) {
			return exposure.Target{}, false
		}
		return p.appAt(r.Host, ref.Path)
	}
	owner, name, ok := p.readRoute(r)
	if !ok {
		return exposure.Target{}, false
	}
	return p.appAt(r.Host, p.cfg.Exposure.BasePath(owner, name))
}

// appAt is the app whose address is path on host.
func (p *proxy) appAt(host, path string) (exposure.Target, bool) {
	return p.cfg.Exposure.Match(&http.Request{Host: host, URL: &url.URL{Path: path}})
}

// atApp turns a root request into the request for the same path under its
// app's prefix, marked as a root request. It is false when r is no app's.
func (p *proxy) atApp(r *http.Request) (*http.Request, exposure.Target, bool) {
	app, ok := p.rootTarget(r)
	if !ok {
		return r, exposure.Target{}, false
	}
	base := strings.TrimSuffix(p.cfg.Exposure.BasePath(app.Owner, app.Name), "/")
	in := r.Clone(context.WithValue(r.Context(), rootKey{}, true))
	in.URL.Path = base + r.URL.Path
	if r.URL.RawPath != "" {
		in.URL.RawPath = base + r.URL.RawPath
	}
	t, ok := p.cfg.Exposure.Match(in)
	if !ok || t.Rest == ticketPath {
		return r, exposure.Target{}, false
	}
	return in, t, true
}

// rootSessionCookie is the root session cookie of the app t, without its value.
func (p *proxy) rootSessionCookie(t exposure.Target) http.Cookie {
	secure := p.cfg.URLs.URLScheme() == "https"
	return http.Cookie{Name: model.CookieName(rootSessionPrefix+t.ObjectName, secure), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure}
}

// routeCookie is the route cookie without its value.
func (p *proxy) routeCookie() http.Cookie {
	secure := p.cfg.URLs.URLScheme() == "https"
	return http.Cookie{Name: model.CookieName(routeCookieName, secure), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure}
}

// routeAD binds a sealed route to its cookie and host, so no other value
// sealed with the same key passes for one.
func routeAD(host string) []byte { return []byte(routeCookieName + "|" + strings.ToLower(host)) }

// readRoute is the app the route cookie of r names, while it is valid.
func (p *proxy) readRoute(r *http.Request) (owner, name string, ok bool) {
	if p.cfg.Seal == nil {
		return "", "", false
	}
	c, err := r.Cookie(p.routeCookie().Name)
	if err != nil {
		return "", "", false
	}
	sealed, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return "", "", false
	}
	plain, err := p.cfg.Seal.Decrypt(sealed, routeAD(r.Host))
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(string(plain), "/")
	if len(parts) != 3 {
		return "", "", false
	}
	until, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > until {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// setRootCookies adds to h the route cookie naming t and, when the request
// came with an app session (tok), the app's root session cookie.
func (p *proxy) setRootCookies(h http.Header, host string, t exposure.Target, tok string) {
	if p.cfg.Seal != nil {
		until := time.Now().Add(routeTTL).Unix()
		sealed, err := p.cfg.Seal.Encrypt([]byte(t.Owner+"/"+t.Name+"/"+strconv.FormatInt(until, 10)), routeAD(host))
		if err == nil {
			c := p.routeCookie()
			c.Value, c.MaxAge = base64.RawURLEncoding.EncodeToString(sealed), int(routeTTL/time.Second)
			h.Add("Set-Cookie", c.String())
		}
	}
	if tok != "" {
		c := p.rootSessionCookie(t)
		c.Value = tok
		h.Add("Set-Cookie", c.String())
	}
}

// pageView is true for a browser opening a page: a GET navigation of the
// top-level document, or a GET from a client that sends no Fetch Metadata.
func pageView(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	mode, dest := r.Header.Get("Sec-Fetch-Mode"), r.Header.Get("Sec-Fetch-Dest")
	return mode == "navigate" && dest == "document" || mode == "" && dest == ""
}

// isHTML is true when h says the body is an HTML page.
func isHTML(h http.Header) bool {
	t, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && t == "text/html"
}

// prefixLocation adds prefix to a redirect of a stripped app to one of its
// own absolute paths ("/login", or an absolute URL on host), unless it is
// under the prefix already. Relative redirects and other hosts are left
// alone.
func prefixLocation(h http.Header, host, prefix string) {
	loc := h.Get("Location")
	u, err := url.Parse(loc)
	if loc == "" || err != nil {
		return
	}
	var head, rest string
	switch {
	case u.Scheme == "" && u.Host == "" && strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//"):
		rest = loc
	case (u.Scheme == "http" || u.Scheme == "https") && u.User == nil && strings.EqualFold(u.Host, host):
		head = loc[:len(u.Scheme)+len("://")+len(u.Host)]
		if rest = loc[len(head):]; !strings.HasPrefix(rest, "/") {
			rest = "/" + rest
		}
	default:
		return
	}
	if inApp(u.Path, prefix+"/") {
		return
	}
	h.Set("Location", head+prefix+rest)
}

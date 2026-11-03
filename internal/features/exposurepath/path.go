// Package exposurepath serves apps under /u/<owner>/<name>/, on the apps
// host when one is configured and on the UI host otherwise.
package exposurepath

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/naming"
)

// ID is the backend's registry key and the value of urls.mode.
const ID = "path"

// Backend is the path exposure.
type Backend struct{ urls config.URLs }

var _ exposure.Backend = (*Backend)(nil)

// New returns the path backend for urls.
func New(urls config.URLs) *Backend { return &Backend{urls: urls} }

func (*Backend) ID() string { return ID }

func (*Backend) BasePath(owner, name string) string { return "/u/" + owner + "/" + name + "/" }

func (b *Backend) URLFor(owner, name, _ string) url.URL {
	u := url.URL{Path: b.BasePath(owner, name)}
	if b.urls.AppsHost != "" {
		u.Scheme, u.Host = b.urls.URLScheme(), b.urls.AppsHost
	}
	return u
}

func (b *Backend) Match(r *http.Request) (exposure.Target, bool) {
	if b.urls.AppsHost != "" && !strings.EqualFold(r.Host, b.urls.AppsHost) {
		return exposure.Target{}, false
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/u/")
	if !ok {
		return exposure.Target{}, false
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return exposure.Target{}, false
	}
	t := exposure.Target{ObjectName: naming.ObjectName(parts[0], parts[1]), Owner: parts[0], Name: parts[1]}
	if len(parts) == 3 {
		t.Rest = parts[2]
	}
	return t, true
}

// CookieScope keeps the cookie on the app's own path, host only. The path has
// no trailing slash: RFC 6265 sends a cookie to its path and to anything below
// "<path>/", so "/u/o/n" covers both /u/o/n and /u/o/n/ and still not /u/o/n2.
func (b *Backend) CookieScope(t exposure.Target) (string, string) {
	return "", strings.TrimSuffix(b.BasePath(t.Owner, t.Name), "/")
}

func (b *Backend) SeparateHost() bool { return b.urls.AppsOnOtherHost() }

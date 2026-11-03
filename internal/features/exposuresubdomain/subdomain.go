// Package exposuresubdomain serves each app on <objectName>.<appsDomain>.
package exposuresubdomain

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/exposure"
)

// ID is the backend's registry key and the value of urls.mode.
const ID = "subdomain"

// Backend is the subdomain exposure.
type Backend struct{ urls config.URLs }

var _ exposure.Backend = (*Backend)(nil)

// New returns the subdomain backend for urls.
func New(urls config.URLs) *Backend { return &Backend{urls: urls} }

func (*Backend) ID() string { return ID }

func (*Backend) BasePath(_, _ string) string { return "/" }

func (b *Backend) URLFor(_, _, objectName string) url.URL {
	return url.URL{Scheme: b.urls.URLScheme(), Host: objectName + "." + b.urls.AppsDomain, Path: "/"}
}

// Match reads the object name from the first label of the host. The object
// name is a single DNS label, so a host with more labels is not an app.
func (b *Backend) Match(r *http.Request) (exposure.Target, bool) {
	label, ok := strings.CutSuffix(strings.ToLower(r.Host), "."+strings.ToLower(b.urls.AppsDomain))
	if !ok || label == "" || strings.Contains(label, ".") {
		return exposure.Target{}, false
	}
	return exposure.Target{ObjectName: label, Rest: strings.TrimPrefix(r.URL.Path, "/")}, true
}

// CookieScope is host-only: one app's cookie never reaches another app.
func (*Backend) CookieScope(exposure.Target) (string, string) { return "", "/" }

func (*Backend) SeparateHost() bool { return true }

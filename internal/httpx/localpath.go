// Package httpx holds small HTTP helpers shared by the gateway and the
// feature packages.
package httpx

import (
	"net/url"
	"strings"
)

// IsLocalPath is true for an absolute path on the same host: it starts with
// one slash, not two, and holds no backslash or control character that a
// browser could read as another host. Redirect targets taken from a request
// must pass it.
func IsLocalPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n\t") {
		return false
	}
	u, err := url.Parse(p)
	return err == nil && u.Host == "" && u.Scheme == ""
}

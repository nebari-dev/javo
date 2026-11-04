// Package exposure is the extension point for where an app is served: under
// a path of one host, or on its own subdomain. Implementations live in
// internal/features and are registered in internal/cli/wiring.
package exposure

import (
	"net/http"
	"net/url"
)

// Target is the app a request is for. Owner and Name are set when the
// address carries them (path mode); ObjectName always is. Rest is the path
// below the app's base, without a leading slash.
type Target struct{ ObjectName, Owner, Name, Rest string }

// Backend decides the address of an app and which app an address is for.
type Backend interface {
	ID() string // "path", "subdomain"
	// URLFor is the app's base address, absolute when a host is configured.
	URLFor(owner, name, objectName string) url.URL
	// BasePath is the path the app serves under ("/" in subdomain mode).
	BasePath(owner, name string) string
	// Match says which app a request is for.
	Match(r *http.Request) (Target, bool)
	// CookieScope is where the app's own cookie lives ("" domain is host-only).
	CookieScope(t Target) (domain, path string)
	// SeparateHost is true when apps are not on the UI origin, so people
	// reach them with a ticket instead of the UI cookie.
	SeparateHost() bool
}

// SharesOrigin is true when an app's page shares its origin with the UI (one
// host) or with the other apps (path mode on an apps host). A page there can
// act as whoever views it, so an admin opens only their own apps.
func SharesOrigin(b Backend) bool {
	return !b.SeparateHost() || b.BasePath("o", "n") != "/"
}

// Package authprovider is the extension point for ways of signing in. A
// provider proves who a person is; the gateway (the host) turns that into an
// Identity, a session and a cookie. Implementations live in
// internal/features and are registered in internal/cli/wiring.
package authprovider

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/reasons"
)

// ErrRevoked means the provider no longer vouches for the person (for
// example the account was deleted): the session ends.
var ErrRevoked = errors.New("sign-in revoked")

// ErrUnavailable means the provider could not be asked right now: the
// session stays until the outage grace period ends.
var ErrUnavailable = errors.New("sign-in provider unavailable")

// Login option kinds.
const (
	KindPassword = "password"
	KindRedirect = "redirect"
)

// LoginOption is what the login page shows for one provider.
type LoginOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Kind is KindPassword (the page shows a form) or KindRedirect (the page
	// links to StartURL).
	Kind     string `json:"kind"`
	StartURL string `json:"startURL,omitempty"`
	// Disabled means the provider cannot sign anyone in right now (for
	// example the identity provider is unreachable); Reason is the catalog
	// reason code that says why.
	Disabled bool   `json:"disabled,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// Me is the signed-in person as the API reports it.
type Me struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	// Picture is the person's avatar image URL; absent when the provider sends none.
	Picture string   `json:"picture,omitempty" format:"uri"`
	Groups  []string `json:"groups"`
	Roles   []string `json:"roles"`
}

// Signed is the outcome of Mounter.Complete: the cookie to set and the
// person it belongs to.
type Signed struct {
	Cookie http.Cookie
	Me     Me
}

// Provider is one way of signing in.
type Provider interface {
	// ID is the registry key and the Session.Provider value: "local", "oidc".
	ID() string
	// Option is what the login page shows.
	Option() LoginOption
	// Mount registers the provider's own routes.
	Mount(m Mounter)
	// Revalidate asks the provider whether the session's person is still
	// valid. It returns the current claims and the session to store, with
	// RevalidateAt moved forward. It returns ErrRevoked or ErrUnavailable.
	Revalidate(ctx context.Context, s session.Session) (identity.Claims, session.Session, error)
}

// Mounter is what the host offers a provider while it mounts its routes.
type Mounter interface {
	// API is for JSON routes such as POST /api/auth/local/login.
	API() huma.API
	// Handle is for browser routes such as a redirect callback.
	Handle(pattern string, h http.Handler)
	// LimitLogin counts one sign-in attempt for username from the caller's
	// address. It returns nil, or the 429 error to return from the route.
	LimitLogin(ctx context.Context, username string) error
	// Reject builds the error for a failed request from a catalog message.
	Reject(status int, code reasons.MessageCode) error
	// Complete finishes a sign-in: it finds or creates the Identity for c,
	// ends the session this browser had, creates s for that Identity and
	// returns the cookie. s.Principal is filled in by the host.
	Complete(ctx context.Context, c identity.Claims, s session.Session) (Signed, error)
	// Now is the host's clock.
	Now() time.Time
}

// LogoutRedirector is implemented by providers that can sign the person out
// of the identity provider too (OIDC RP-initiated logout). LogoutURL returns
// where to send the browser after the platform session ended, or "" when
// the provider has no such endpoint.
type LogoutRedirector interface {
	LogoutURL(s session.Session) string
}

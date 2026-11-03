// Package codehost is the extension point for the service that hosts an
// app's repository and its pull requests (GitHub today). Implementations live
// in internal/features and are registered in internal/cli/wiring.
package codehost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// PullRequest is one open pull request as the preview reconciler needs it.
type PullRequest struct {
	Number  int32
	Title   string
	HeadSHA string
	// Fork is true when the head branch lives in another repository than the
	// one the pull request targets.
	Fork      bool
	UpdatedAt time.Time
}

// Host talks to one code host.
type Host interface {
	ID() string // "github"
	// Owns says whether this host serves the repository at repoURL.
	Owns(repoURL string) bool
	// OpenPulls lists the open pull requests of the repository, the most
	// recently updated first, at most MaxPulls of them. token is the owner's
	// repository token; empty means the host's own credential, if it has one.
	// A refusal for lack of request budget is a *RateLimitError; a failure
	// the app owner can fix wraps ErrNotFound or ErrAuth.
	OpenPulls(ctx context.Context, repoURL, token string) ([]PullRequest, error)
	// VerifyWebhook checks the signature of a delivery made with secret and
	// returns the event type (the host's own name for it). It reads r.Body.
	VerifyWebhook(r *http.Request, secret []byte) (event string, err error)
}

// MaxPulls is the most pull requests one OpenPulls call reads.
const MaxPulls = 100

var (
	// ErrNotFound says the repository cannot be seen with the credential at
	// hand: the address is wrong, or it is private and needs a token.
	ErrNotFound = errors.New("repository not found")
	// ErrAuth says the host refused the token, or the token may not read the
	// repository.
	ErrAuth = errors.New("token not accepted")
)

// RateLimitError says the host refused for lack of request budget until
// Reset.
type RateLimitError struct{ Reset time.Time }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("code host request limit reached until %s", e.Reset.UTC().Format(time.RFC3339))
}

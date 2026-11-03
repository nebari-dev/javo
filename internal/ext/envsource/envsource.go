// Package envsource is the extension point for an outside environment
// manager (Nebi today) that people pick a pixi environment from for an app or
// a job. The pick is copied in: the pixi.toml and pixi.lock of one version are
// stored with the app or job, so a run never calls the manager. The
// implementation lives in internal/features and is wired in
// internal/cli/wiring; with none wired the platform offers no environments.
package envsource

import (
	"context"
	"errors"
)

// Environment is one environment a person may pick.
type Environment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Owner is the manager's name for whoever owns it.
	Owner string `json:"owner,omitempty"`
}

// Pinned is one version of an environment, with its files.
type Pinned struct {
	Environment
	// Version is the manager's label of the version the files are from.
	Version  string
	Manifest string // pixi.toml
	Lock     string // pixi.lock
}

// Source lists and fetches the environments of the person whose OpenID
// Connect ID token is idToken. It acts as that person: it sees only what the
// manager lets them see.
type Source interface {
	ID() string // "nebi"
	List(ctx context.Context, idToken string) ([]Environment, error)
	// Fetch returns the newest version of the environment id.
	Fetch(ctx context.Context, idToken, id string) (Pinned, error)
}

var (
	// ErrRefused says the manager did not accept the person's sign-in.
	ErrRefused = errors.New("environment manager refused the sign-in")
	// ErrNotFound says the person cannot see the environment, or it has no
	// version yet.
	ErrNotFound = errors.New("environment not found")
)

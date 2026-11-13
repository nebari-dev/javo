// Package executor is what Javo needs from whatever runs its workloads:
// Kubernetes, Docker or local processes. The packages below it implement
// these interfaces; nothing above it knows which one is in use.
package executor

import (
	"context"
	"errors"

	"github.com/nebari-dev/javo/internal/workload"
)

// Bundle is the values a workload reads for one use (its API token, its
// secrets, its repository token), with notes about them.
type Bundle struct {
	// Data is the values by key. They are secret: never log a Bundle.
	Data map[string][]byte
	// Meta says which values these are (a token's id, a generation), so a
	// caller can tell whether they are current without reading them.
	Meta map[string]string
}

// The uses an app's bundles have.
const (
	UseAPI     = "api"     // its owner's API token and the API guide
	UseSecrets = "secrets" // the values of the secrets it is given
	UseGit     = "git"     // the token its clone reads
	UseNebi    = "nebi"    // its Nebi token
)

// ErrForeign means the place a workload's bundle is kept holds something
// that is not that workload's. It is never read, replaced or deleted.
var ErrForeign = errors.New("exists and does not belong to the app")

// Secrets keeps the bundles workloads read. A bundle outlives the workload
// and is replaced in place; for some values it is the only copy.
type Secrets interface {
	// Get returns the workload's bundle for use, or nil when it has none.
	Get(ctx context.Context, of workload.Ref, use string) (*Bundle, error)
	// Put makes the workload's bundle for use hold b.
	Put(ctx context.Context, of workload.Ref, use string, b Bundle) error
	// Delete removes the workload's bundle for use, if there is one.
	Delete(ctx context.Context, of workload.Ref, use string) error
}

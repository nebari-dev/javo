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
	// Rebuildable is for values that can be made again from the database:
	// an executor may then let the bundle go with the workload. Any other
	// bundle holds the only copy of a token and stays until deleted.
	Rebuildable bool
}

// ErrForeign means the place a workload's bundle is kept holds something
// that is not that workload's. It is never read, replaced or deleted.
var ErrForeign = errors.New("exists and does not belong to the app")

// Secrets keeps the bundles workloads read, each under a name that is
// unique where the workload runs (workload.SecretRef names one). A bundle
// outlives the workload and is replaced in place.
type Secrets interface {
	// Get returns the workload's bundle name, or nil when there is none.
	Get(ctx context.Context, of workload.Ref, name string) (*Bundle, error)
	// Put makes the workload's bundle name hold b.
	Put(ctx context.Context, of workload.Ref, name string, b Bundle) error
	// Delete removes the workload's bundle name, if there is one.
	Delete(ctx context.Context, of workload.Ref, name string) error
}

// ErrUnmanaged means the executor runs nothing where a workload points,
// for now: Observe returns it, and the caller looks again later.
var ErrUnmanaged = errors.New("is not a place the executor runs workloads in")

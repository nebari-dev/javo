package executor

import (
	"context"
	"io"
	"time"

	"github.com/nebari-dev/javo/internal/workload"
)

// Executor runs workloads. It is the only thing that knows how: no caller
// asks which platform is in use.
type Executor interface {
	// Start runs s. Starting a workload that already runs under s.Ref is
	// not an error and changes nothing.
	Start(ctx context.Context, s workload.Spec) error
	// Stop ends the workload and removes what Start made for it. Its
	// bundles (Secrets) stay. Stopping one that does not exist is not an
	// error.
	Stop(ctx context.Context, ref workload.Ref) error
	// Observe says what the workload is doing, or nil when there is none.
	Observe(ctx context.Context, ref workload.Ref) (*workload.State, error)
	// List names every workload the executor runs, for the sweep that
	// stops the ones whose row is gone.
	List(ctx context.Context) ([]workload.Ref, error)
	// Watch calls changed with the Ref of each workload whose state may
	// have changed, until ctx ends. A caller that misses a call learns
	// nothing wrong: it Observes again later anyway.
	Watch(ctx context.Context, changed func(workload.Ref)) error
	// Addr is the host:port the gateway dials to reach the workload's
	// port. It is known before the workload runs.
	Addr(ctx context.Context, ref workload.Ref, port int32) (string, error)
	// Logs streams what a unit of the workload printed.
	Logs(ctx context.Context, ref workload.Ref, o LogOptions) (io.ReadCloser, error)
}

// LogOptions picks the part of a workload's output to read.
type LogOptions struct {
	// Unit is the unit to read; empty is the main one.
	Unit string
	// Follow keeps the stream open for new lines.
	Follow bool
	// Previous reads the run before the last restart.
	Previous bool
	// Tail limits the stream to the last lines; zero is all of them.
	Tail int64
	// Since leaves out lines printed before it; zero is all of them.
	Since time.Time
	// Timestamps starts each line with when it was printed (RFC 3339).
	Timestamps bool
	// MaxBytes ends the stream after that many bytes; zero is no limit.
	MaxBytes int64
}

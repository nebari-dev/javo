package executor

import (
	"context"
	"errors"
	"io"

	"github.com/nebari-dev/javo/internal/workload"
)

// ErrNone is None's answer to a Start.
var ErrNone = errors.New("nothing runs this kind of workload here")

// None is the Executor of a kind of workload a platform cannot run yet
// (job runs, on Docker and as processes): it has no workload, sees none
// change, and refuses to start one with ErrNone. Whoever holds it can go
// on reading and tidying as if every workload had ended.
type None struct{}

var _ Executor = None{}

func (None) Start(context.Context, workload.Spec) error { return ErrNone }
func (None) Stop(context.Context, workload.Ref) error   { return nil }
func (None) Observe(context.Context, workload.Ref) (*workload.State, error) {
	return nil, nil
}
func (None) List(context.Context) ([]workload.Ref, error) { return nil, nil }

// Watch waits for ctx to end: nothing ever changes.
func (None) Watch(ctx context.Context, _ func(workload.Ref)) error {
	<-ctx.Done()
	return nil
}

func (None) Addr(context.Context, workload.Ref, int32) (string, error) { return "", ErrNone }
func (None) Logs(context.Context, workload.Ref, LogOptions) (io.ReadCloser, error) {
	return nil, nil
}

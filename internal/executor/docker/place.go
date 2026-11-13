package docker

import (
	"context"

	"github.com/nebari-dev/javo/internal/workload"
)

// The three methods below are the app reconciler's Place: what an app
// has where it runs for as long as its row exists. On Docker that is
// nothing. The address the gateway dials is the sandbox's name (Addr),
// which is known without anything being made; the bundles are rows of
// the database (internal/executor/vault) and need no owner on the
// daemon; and the one thing that could be kept, the app's network, is
// taken by Start and given back by Stop instead, because each holds a
// range of a pool that stopped apps should not use up.

// Hold makes nothing: see above.
func (x *Executor) Hold(context.Context, workload.Ref, int32) error { return nil }

// Tidy removes nothing: no earlier version kept anything on a Docker
// daemon under other names.
func (x *Executor) Tidy(context.Context, workload.Ref, func(ctx context.Context, tokenID string) error) error {
	return nil
}

// Release gives up nothing: Stop has removed all there was.
func (x *Executor) Release(context.Context, workload.Ref) error { return nil }

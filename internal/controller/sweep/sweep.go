// Package sweep removes what a database row that is gone left where
// apps run: what an app or job left behind when its row was deleted while
// the controller was not running.
package sweep

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/store"
)

// Runner runs Sweep on the elected leader: at start, on every Resync change
// (announcements may have been missed) and every Every. Add it to the manager.
type Runner struct {
	// What names the rows swept, for the log ("apps", "jobs").
	What    string
	Sweep   func(ctx context.Context) error
	Every   time.Duration // zero means dbsource.DefaultResync
	Changes <-chan store.Change
}

// NeedLeaderElection makes the manager start Runner on the leader only.
func (r *Runner) NeedLeaderElection() bool { return true }

// Start runs until ctx ends.
func (r *Runner) Start(ctx context.Context) error {
	every := r.Every
	if every <= 0 {
		every = dbsource.DefaultResync
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	sweep := func() {
		if err := r.Sweep(ctx); err != nil && ctx.Err() == nil {
			log.FromContext(ctx).Error(err, "sweeping objects of removed rows", "rows", r.What)
		}
	}
	sweep()
	for {
		select {
		case <-ctx.Done():
			return nil
		case c := <-r.Changes:
			if c.Table == store.TableResync {
				sweep()
			}
		case <-tick.C:
			sweep()
		}
	}
}

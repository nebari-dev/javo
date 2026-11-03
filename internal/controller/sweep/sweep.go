// Package sweep removes the Kubernetes objects whose database row is gone:
// what an app or job left behind when its row was deleted while the
// controller was not running.
package sweep

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/namespace"
	"github.com/nebari-dev/javo/internal/store"
)

// Orphans deletes objects that carry Label with a value no row has. It looks
// only at objects with the label, and, when ManagedOnly is set, in
// namespaces the controller manages; anything else is never touched.
type Orphans struct {
	Client client.Client
	Label  string
	// ManagedOnly limits the sweep to namespaces labelled as managed, for
	// controllers that see more than the one workspace namespace.
	ManagedOnly bool
	// Live returns the ids of the rows that exist.
	Live func(ctx context.Context) (map[string]bool, error)
	// Release, when set, runs before each delete, for kinds that hold their
	// own finalizer: with the row gone nothing else would ever remove it.
	Release func(ctx context.Context, obj client.Object) error
	// DeleteOptions apply to every delete (for example the propagation
	// policy that takes a Job's pods with it).
	DeleteOptions []client.DeleteOption
}

// Delete lists the objects first and the rows after, so an object created
// while the sweep runs belongs to a row that already existed and Live always
// names it. lists are empty lists of the kinds to sweep.
func (o Orphans) Delete(ctx context.Context, lists ...client.ObjectList) error {
	for _, l := range lists {
		if err := o.Client.List(ctx, l, client.HasLabels{o.Label}); err != nil {
			return err
		}
	}
	live, err := o.Live(ctx)
	if err != nil {
		return err
	}
	managed := map[string]bool{}
	var orphans []client.Object
	for _, l := range lists {
		items, err := meta.ExtractList(l)
		if err != nil {
			return err
		}
		for _, item := range items {
			obj := item.(client.Object)
			if live[obj.GetLabels()[o.Label]] {
				continue
			}
			if o.ManagedOnly {
				ok, seen := managed[obj.GetNamespace()]
				if !seen {
					if ok, err = namespace.IsManaged(ctx, o.Client, obj.GetNamespace()); err != nil {
						return err
					}
					managed[obj.GetNamespace()] = ok
				}
				if !ok {
					continue
				}
			}
			orphans = append(orphans, obj)
		}
	}
	var failed error
	for _, obj := range orphans {
		log.FromContext(ctx).Info("deleting an object whose row is gone", "kind", fmt.Sprintf("%T", obj), "namespace", obj.GetNamespace(), "name", obj.GetName())
		if o.Release != nil {
			if err := o.Release(ctx, obj); err != nil {
				failed = err
				continue
			}
		}
		if err := o.Client.Delete(ctx, obj, o.DeleteOptions...); client.IgnoreNotFound(err) != nil {
			failed = err
		}
	}
	return failed
}

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

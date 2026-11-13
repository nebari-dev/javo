// Package loop is what reconcilers are set up on. On Kubernetes that is
// the controller-runtime manager, with its cache and leader election.
// Without a cluster it is a Loop: the same controllers (work queue, back
// off, one request at a time per row) fed by the database's change feed
// and the executor's Watch, with no manager, no API server and no leader
// to elect, because one Javo process is then the only controller there is.
package loop

import (
	"context"

	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// Driver runs reconcilers. A reconciler that sets itself up on a Driver
// runs the same way with a cluster and without one.
type Driver interface {
	// Reconcile calls r for every request the sources send, under name
	// (its name in logs and metrics).
	Reconcile(name string, r reconcile.Reconciler, sources ...source.TypedSource[reconcile.Request]) error
	// Add runs part beside the reconcilers until the Driver stops.
	Add(part manager.Runnable) error
}

// OnManager is the Driver that sets reconcilers up on mgr.
func OnManager(mgr ctrl.Manager) Driver { return managed{mgr} }

type managed struct{ mgr ctrl.Manager }

func (m managed) Reconcile(name string, r reconcile.Reconciler, sources ...source.TypedSource[reconcile.Request]) error {
	b := ctrl.NewControllerManagedBy(m.mgr).Named(name)
	for _, s := range sources {
		b = b.WatchesRawSource(s)
	}
	return b.Complete(r)
}

func (m managed) Add(part manager.Runnable) error { return m.mgr.Add(part) }

// Loop is the Driver without a cluster. The zero Loop is ready: set
// reconcilers up on it, then Start it.
type Loop struct {
	parts []manager.Runnable
}

var _ Driver = (*Loop)(nil)

func (l *Loop) Reconcile(name string, r reconcile.Reconciler, sources ...source.TypedSource[reconcile.Request]) error {
	// The name only labels logs and metrics; a process that builds two
	// Loops (the tests) may use it twice.
	c, err := controller.NewUnmanaged(name, controller.Options{Reconciler: r, SkipNameValidation: ptr.To(true)})
	if err != nil {
		return err
	}
	for _, s := range sources {
		if err := c.Watch(s); err != nil {
			return err
		}
	}
	l.parts = append(l.parts, c)
	return nil
}

func (l *Loop) Add(part manager.Runnable) error {
	l.parts = append(l.parts, part)
	return nil
}

// Start runs every reconciler and part until ctx ends, or until one of
// them fails: the others are then stopped and its error returned.
func (l *Loop) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, len(l.parts))
	for _, p := range l.parts {
		go func() { errs <- p.Start(ctx) }()
	}
	var first error
	for range l.parts {
		if err := <-errs; err != nil && first == nil {
			first = err
			cancel()
		}
	}
	return first
}

// Package loop is what reconcilers are set up on: a Driver. On Kubernetes
// that is the controller-runtime manager, with its cache and leader
// election (loop/managed). Without a cluster it is a Loop: a work queue
// per reconciler (back off, one call at a time per row) fed by the
// database's change feed and the executor's Watch, with no manager, no API
// server and no leader to elect, because one Javo process is then the only
// controller there is. Nothing here knows of a cluster, so a reconciler
// set up on a Driver, and a binary that runs only Loops, need no client of
// one.
package loop

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Reconciler brings what a row describes to what the row says.
type Reconciler interface {
	// Reconcile does so for the row with this id. It is called again
	// after again when that is positive, and, backing off, after an
	// error.
	Reconcile(ctx context.Context, id string) (again time.Duration, err error)
}

// ReconcilerFunc is a function as a Reconciler.
type ReconcilerFunc func(ctx context.Context, id string) (time.Duration, error)

func (f ReconcilerFunc) Reconcile(ctx context.Context, id string) (time.Duration, error) {
	return f(ctx, id)
}

// Source says which rows to reconcile.
type Source interface {
	// Start begins calling enqueue with the id of each row that may need
	// work, until ctx ends. It returns once it has begun: what waits or
	// watches runs in a goroutine of its own.
	Start(ctx context.Context, enqueue func(id string)) error
}

// SourceFunc is a function as a Source.
type SourceFunc func(ctx context.Context, enqueue func(id string)) error

func (f SourceFunc) Start(ctx context.Context, enqueue func(id string)) error {
	return f(ctx, enqueue)
}

// Part runs beside the reconcilers.
type Part interface {
	// Start runs until ctx ends. An error ends the whole Driver.
	Start(ctx context.Context) error
}

// PartFunc is a function as a Part.
type PartFunc func(ctx context.Context) error

func (f PartFunc) Start(ctx context.Context) error { return f(ctx) }

// Driver runs reconcilers. A reconciler that sets itself up on a Driver
// runs the same way with a cluster and without one.
type Driver interface {
	// Reconcile calls r for every row the sources ask for, under name
	// (its name in logs).
	Reconcile(name string, r Reconciler, sources ...Source) error
	// Add runs part beside the reconcilers until the Driver stops.
	Add(part Part) error
}

// Loop is the Driver without a cluster. The zero Loop is ready: set
// reconcilers up on it, then Start it.
type Loop struct {
	parts []Part
}

var _ Driver = (*Loop)(nil)

func (l *Loop) Reconcile(name string, r Reconciler, sources ...Source) error {
	l.parts = append(l.parts, &worker{name: name, r: r, sources: sources})
	return nil
}

func (l *Loop) Add(part Part) error {
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

// worker is one reconciler of a Loop and its queue. A row asked for
// while it waits in the queue is in it once, and one asked for during its
// own call is called once more afterwards, so a row never has two calls
// at once.
type worker struct {
	name    string
	r       Reconciler
	sources []Source
}

// Start reconciles what the sources ask for, one row at a time, until
// ctx ends.
func (w *worker) Start(ctx context.Context) error {
	// What a controller of a cluster waits between tries: from 5ms,
	// doubling for a row that keeps failing, and no more than ten rows
	// a second over all.
	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	defer q.ShutDown()
	for _, s := range w.sources {
		if err := s.Start(ctx, q.Add); err != nil {
			return fmt.Errorf("%s: start its sources: %w", w.name, err)
		}
	}
	go func() {
		<-ctx.Done()
		q.ShutDown()
	}()
	logger := log.FromContext(ctx).WithValues("controller", w.name)
	for {
		id, shutdown := q.Get()
		if shutdown {
			return nil
		}
		again, err := w.reconcile(log.IntoContext(ctx, logger.WithValues("name", id)), id)
		switch {
		case err != nil:
			logger.Error(err, "Reconciler error", "name", id)
			q.AddRateLimited(id)
		case again > 0:
			q.Forget(id)
			q.AddAfter(id, again)
		default:
			q.Forget(id)
		}
		q.Done(id)
	}
}

// reconcile is one call; a panic in it is an error, so one row's bug
// does not stop the others, and where it happened is logged.
func (w *worker) reconcile(ctx context.Context, id string) (again time.Duration, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v [recovered]", p)
			log.FromContext(ctx).Error(err, "Observed a panic in reconciler", "stack", string(debug.Stack()))
		}
	}()
	return w.r.Reconcile(ctx, id)
}

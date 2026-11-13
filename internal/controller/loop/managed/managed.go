// Package managed sets reconcilers up on a controller-runtime manager: the
// loop.Driver of a controller on Kubernetes, with the manager's cache,
// metrics and leader election. Only what runs on a cluster imports it.
package managed

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/nebari-dev/javo/internal/controller/loop"
)

// Driver is the loop.Driver that sets reconcilers up on mgr.
func Driver(mgr ctrl.Manager) loop.Driver { return driver{mgr} }

type driver struct{ mgr ctrl.Manager }

func (d driver) Reconcile(name string, r loop.Reconciler, sources ...loop.Source) error {
	b := ctrl.NewControllerManagedBy(d.mgr).Named(name)
	for _, s := range sources {
		b = b.WatchesRawSource(Source(s))
	}
	return b.Complete(reconcile.Func(func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
		again, err := r.Reconcile(ctx, req.Name)
		return reconcile.Result{RequeueAfter: again}, err
	}))
}

func (d driver) Add(part loop.Part) error { return d.mgr.Add(part) }

// Request is the request for the row with this id: a controller's
// requests name rows by id, in no namespace.
func Request(id string) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Name: id}}
}

// Source is s as a source of a controller built on a manager.
func Source(s loop.Source) source.TypedSource[reconcile.Request] {
	return source.Func(func(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
		return s.Start(ctx, func(id string) { q.Add(Request(id)) })
	})
}

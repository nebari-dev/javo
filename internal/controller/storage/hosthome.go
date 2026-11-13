package storage

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/loop"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/storage/hosthome"
	"github.com/nebari-dev/javo/internal/store"
)

// HostHomeReconciler gives every Home a folder of the machine Javo runs
// on, where apps run as plain processes (the process executor). It stands
// in for HomeReconciler there: no claim, no prepare Job, no size. The
// folder's path is recorded where a claim's name is on Kubernetes
// (Status.ClaimName), which is what an app planned for this machine and
// the gateway's files.Folders read, and the home is Prepared and Ready at
// once. Its size is never enforced: a folder shares the person's disk.
//
// It reads and writes the database and the root folder only, never a
// cluster.
type HostHomeReconciler struct {
	Rows
	// Root is the folder the homes are folders of. Required.
	Root hosthome.Root
}

// Reconcile makes the home's folder and records it. A folder that cannot
// be made, or that is not a private folder of this user, leaves the home
// not Ready with HomePrepareFailed and what was found, and is looked at
// again later.
func (r *HostHomeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	h, err := r.Store.Homes().Get(ctx, req.Name)
	if errors.Is(err, store.ErrNotFound) {
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	base := h.DeepCopy()
	var b *blocked
	if dir, err := r.Root.Ensure(h.Spec.Owner); err != nil {
		b = &blocked{code: reasons.HomePrepareFailed, evidence: []string{err.Error()}, retry: homePrepareRetry}
		h.Status.ClaimName, h.Status.Prepared = "", false
	} else {
		h.Status.ClaimName, h.Status.Prepared = dir, true
	}
	h.Status.SizeEnforced = false
	meta.SetStatusCondition(&h.Status.Conditions, readyCondition("The home is ready.", b))
	if conflict, err := r.writeHome(ctx, &h, base); conflict {
		return ctrl.Result{RequeueAfter: retryConflict}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	if b == nil {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: b.retry}, nil
}

// SetupWithManager is Setup on a controller-runtime manager.
func (r *HostHomeReconciler) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	return r.Setup(loop.OnManager(mgr), feed)
}

// Setup reconciles homes on d when their rows change, as HomeReconciler
// is set up. A machine that runs apps as processes has no manager: there
// d is a loop.Loop.
func (r *HostHomeReconciler) Setup(d loop.Driver, feed *dbsource.Feed) error {
	if r.Store == nil || r.Root.Dir() == "" {
		return errors.New("storage.HostHomeReconciler needs Store and Root")
	}
	all := func(ctx context.Context) ([]reconcile.Request, error) { return r.homeRequests(ctx, nil) }
	return d.Reconcile("storage-host-home", r, dbsource.For(feed.Subscribe(), store.TableHomes, 0, all))
}

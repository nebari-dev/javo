package storage

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"

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
func (r *HostHomeReconciler) Reconcile(ctx context.Context, id string) (time.Duration, error) {
	h, err := r.Store.Homes().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	} else if err != nil {
		return 0, err
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
		return retryConflict, nil
	} else if err != nil {
		return 0, err
	}
	if b == nil {
		return 0, nil
	}
	return b.retry, nil
}

// Setup reconciles homes on d when their rows change, as HomeReconciler
// is set up. A machine that runs apps as processes has no manager: there
// d is a loop.Loop.
func (r *HostHomeReconciler) Setup(d loop.Driver, feed *dbsource.Feed) error {
	if r.Store == nil || r.Root.Dir() == "" {
		return errors.New("storage.HostHomeReconciler needs Store and Root")
	}
	all := func(ctx context.Context) ([]string, error) { return r.homeRequests(ctx, nil) }
	return d.Reconcile("storage-host-home", r, dbsource.For(feed.Subscribe(), store.TableHomes, 0, all))
}

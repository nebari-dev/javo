package storage

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/storage/dockerhome"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/store"
)

// DockerHomeReconciler gives every Home a named volume of the Docker
// daemon apps run on (the Docker executor). It stands in for
// HomeReconciler there: the volume takes the place of the claim, and
// handing its root to the person's uid the place of the prepare Job. The
// volume's name is recorded where a claim's name is on Kubernetes
// (Status.ClaimName), which is what an app planned for Docker mounts and
// what the gateway's file helper (files.Helpers) reads.
//
// A home here has no size, no backup, no restore and no move to other
// storage: a volume of Docker's local driver has no size (SizeEnforced is
// never set), and nothing here reads Spec.Size, Spec.Backup or
// Spec.Restore. It never removes a volume: a home's files outlive its row.
//
// It reads and writes the database and the daemon's volumes only, never a
// cluster.
type DockerHomeReconciler struct {
	Rows
	// Volumes are the homes on the daemon. Required.
	Volumes dockerhome.Volumes
	// UIDs gives the uid and gid a person's apps run as: their home
	// belongs to them. Required.
	UIDs ids.UserIDs
}

// Reconcile makes the home's volume, hands it to its owner once, and
// records it. A volume that is not labelled as this person's home leaves
// the home not Ready with HomeClaimForeign and is never used.
func (r *DockerHomeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	h, err := r.Store.Homes().Get(ctx, req.Name)
	if errors.Is(err, store.ErrNotFound) {
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	base := h.DeepCopy()
	b, err := r.step(ctx, &h)
	if err != nil {
		return ctrl.Result{}, err
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

// step does the work of one pass. It changes h in memory; Reconcile
// writes it. A returned *blocked is a state to report, not an error.
func (r *DockerHomeReconciler) step(ctx context.Context, h *model.Home) (*blocked, error) {
	name, fresh, err := r.Volumes.Ensure(ctx, h.Namespace, h.Spec.Owner)
	switch {
	case errors.Is(err, dockerhome.ErrForeign):
		h.Status.ClaimName, h.Status.Prepared = "", false
		return &blocked{code: reasons.HomeClaimForeign, evidence: []string{err.Error()}}, nil
	case err != nil:
		// The daemon's trouble, or a name no volume can have. What the
		// home had recorded stays: its files have not gone anywhere.
		return &blocked{code: reasons.HomePending, evidence: []string{err.Error()}, retry: homeClaimRetry}, nil
	}
	h.Status.ClaimName = name
	if fresh {
		// A volume made now belongs to root, whatever the row says of an
		// earlier one.
		h.Status.Prepared = false
	}
	if h.Status.Prepared {
		return nil, nil
	}
	uid, gid, err := r.UIDs.UID(ctx, h.Spec.Owner)
	if errors.Is(err, ids.ErrNoUID) {
		return &blocked{code: reasons.IdentityPending, evidence: []string{"the owner has no user id yet"}, retry: homeWaitRetry}, nil
	} else if err != nil {
		return nil, err
	}
	if err := r.Volumes.Own(ctx, h.Namespace, h.Spec.Owner, uid, gid); err != nil {
		return &blocked{code: reasons.HomePrepareFailed, evidence: []string{err.Error()}, retry: homePrepareRetry}, nil
	}
	h.Status.Prepared = true
	return nil, nil
}

// SetupWithManager reconciles homes when their rows change, as
// HomeReconciler is set up.
func (r *DockerHomeReconciler) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	if r.Store == nil || r.Volumes.Client == nil || r.Volumes.Image == "" || r.UIDs == nil {
		return errors.New("storage.DockerHomeReconciler needs Store, Volumes and UIDs")
	}
	all := func(ctx context.Context) ([]reconcile.Request, error) { return r.homeRequests(ctx, nil) }
	return ctrl.NewControllerManagedBy(mgr).Named("storage-docker-home").
		WatchesRawSource(dbsource.For(feed.Subscribe(), store.TableHomes, 0, all)).
		Complete(r)
}

// DockerFolderReconciler answers every SharedFolder where apps run as
// Docker containers: group folders are not offered there yet. It makes no
// volume and marks the folder not Ready with SharedFoldersUnavailable and
// the reason, so nothing mounts it and the admin who asked sees why.
type DockerFolderReconciler struct {
	Rows
}

// Reconcile records that the folder is not offered.
func (r *DockerFolderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	got, err := r.Store.SharedFolders().Get(ctx, req.Name)
	if errors.Is(err, store.ErrNotFound) {
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	f, base := &got, got.DeepCopy()
	meta.SetStatusCondition(&f.Status.Conditions, readyCondition("", &blocked{
		code:     reasons.SharedFoldersUnavailable,
		evidence: []string{"apps run as Docker containers here, where a group has no volume of its own yet"},
	}))
	if err := r.writeFolder(ctx, f, base); errors.Is(err, errRowChanged) {
		return ctrl.Result{RequeueAfter: retryConflict}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager reconciles folders when their rows change.
func (r *DockerFolderReconciler) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	if r.Store == nil {
		return errors.New("storage.DockerFolderReconciler needs Store")
	}
	all := func(ctx context.Context) ([]reconcile.Request, error) { return r.folderRequests(ctx, nil) }
	return ctrl.NewControllerManagedBy(mgr).Named("storage-docker-folder").
		WatchesRawSource(dbsource.For(feed.Subscribe(), store.TableSharedFolders, 0, all)).
		Complete(r)
}

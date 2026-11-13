package onhost

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/loop"
	"github.com/nebari-dev/javo/internal/controller/storage/rows"
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
	rows.Rows
	// Volumes are the homes on the daemon. Required.
	Volumes dockerhome.Volumes
	// UIDs gives the uid and gid a person's apps run as: their home
	// belongs to them. Required.
	UIDs ids.UserIDs
}

// Reconcile makes the home's volume, hands it to its owner once, and
// records it. A volume that is not labelled as this person's home leaves
// the home not Ready with HomeClaimForeign and is never used.
func (r *DockerHomeReconciler) Reconcile(ctx context.Context, id string) (time.Duration, error) {
	h, err := r.Store.Homes().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	base := h.DeepCopy()
	b, err := r.step(ctx, &h)
	if err != nil {
		return 0, err
	}
	h.Status.SizeEnforced = false
	meta.SetStatusCondition(&h.Status.Conditions, rows.ReadyCondition("The home is ready.", b))
	if conflict, err := r.WriteHome(ctx, &h, base); conflict {
		return rows.RetryConflict, nil
	} else if err != nil {
		return 0, err
	}
	if b == nil {
		return 0, nil
	}
	return b.Retry, nil
}

// step does the work of one pass. It changes h in memory; Reconcile
// writes it. A returned *rows.Blocked is a state to report, not an error.
func (r *DockerHomeReconciler) step(ctx context.Context, h *model.Home) (*rows.Blocked, error) {
	name, fresh, err := r.Volumes.Ensure(ctx, h.Namespace, h.Spec.Owner)
	switch {
	case errors.Is(err, dockerhome.ErrForeign):
		h.Status.ClaimName, h.Status.Prepared = "", false
		return &rows.Blocked{Code: reasons.HomeClaimForeign, Evidence: []string{err.Error()}}, nil
	case err != nil:
		// The daemon's trouble, or a name no volume can have. What the
		// home had recorded stays: its files have not gone anywhere.
		return &rows.Blocked{Code: reasons.HomePending, Evidence: []string{err.Error()}, Retry: rows.HomeClaimRetry}, nil
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
		return &rows.Blocked{Code: reasons.IdentityPending, Evidence: []string{"the owner has no user id yet"}, Retry: rows.HomeWaitRetry}, nil
	} else if err != nil {
		return nil, err
	}
	if err := r.Volumes.Own(ctx, h.Namespace, h.Spec.Owner, uid, gid); err != nil {
		return &rows.Blocked{Code: reasons.HomePrepareFailed, Evidence: []string{err.Error()}, Retry: rows.HomePrepareRetry}, nil
	}
	h.Status.Prepared = true
	return nil, nil
}

// Setup reconciles homes on d when their rows change, as HomeReconciler
// is set up. A Docker host has no manager: there d is a loop.Loop.
func (r *DockerHomeReconciler) Setup(d loop.Driver, feed *dbsource.Feed) error {
	if r.Store == nil || r.Volumes.Client == nil || r.Volumes.Image == "" || r.UIDs == nil {
		return errors.New("onhost.DockerHomeReconciler needs Store, Volumes and UIDs")
	}
	all := func(ctx context.Context) ([]string, error) { return r.HomeIDs(ctx, nil) }
	return d.Reconcile("storage-docker-home", r, dbsource.For(feed.Subscribe(), store.TableHomes, 0, all))
}

// NoFolderReconciler answers every SharedFolder where group folders are
// not offered: where apps run as Docker containers, and where they run as
// processes of one person's machine. It makes nothing and marks the
// folder not Ready with SharedFoldersUnavailable and Why, so nothing
// mounts it and the admin who asked sees why.
type NoFolderReconciler struct {
	rows.Rows
	// Why says, as the folder's evidence, why there are none here.
	// Required.
	Why string
}

// Reconcile records that the folder is not offered.
func (r *NoFolderReconciler) Reconcile(ctx context.Context, id string) (time.Duration, error) {
	got, err := r.Store.SharedFolders().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	f, base := &got, got.DeepCopy()
	meta.SetStatusCondition(&f.Status.Conditions, rows.ReadyCondition("", &rows.Blocked{
		Code: reasons.SharedFoldersUnavailable, Evidence: []string{r.Why},
	}))
	if err := r.WriteFolder(ctx, f, base); errors.Is(err, rows.ErrRowChanged) {
		return rows.RetryConflict, nil
	} else if err != nil {
		return 0, err
	}
	return 0, nil
}

// Setup reconciles folders on d when their rows change.
func (r *NoFolderReconciler) Setup(d loop.Driver, feed *dbsource.Feed) error {
	if r.Store == nil || r.Why == "" {
		return errors.New("onhost.NoFolderReconciler needs Store and Why")
	}
	all := func(ctx context.Context) ([]string, error) { return r.FolderIDs(ctx, nil) }
	return d.Reconcile("storage-no-folder", r, dbsource.For(feed.Subscribe(), store.TableSharedFolders, 0, all))
}

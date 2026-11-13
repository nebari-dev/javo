package storage

import (
	"context"
	"errors"
	"fmt"
	"slices"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/loop/managed"
	"github.com/nebari-dev/javo/internal/controller/storage/rows"
	"github.com/nebari-dev/javo/internal/ext/storage"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
)

const (
	folderMountPath = "/vol"
	// folderDir is the directory inside the claim that pods mount (subPath
	// "data"); the prepare Job makes it so the claim root stays the volume's.
	folderDir = folderMountPath + "/" + podstorage.FolderSubPath
	// folderMode is group-writable and setgid: files made in the folder take
	// the group, so every member can edit them.
	folderMode = "02775"
	// folderUID is the non-root user the prepare Job runs as. It owns the
	// directory; members write through the group.
	folderUID = 65532

	defaultFolderSize = "20Gi"
)

// SharedFolderReconciler gives every SharedFolder its storage: the group's
// gid, a claim, and a directory that every member of the group can write.
type SharedFolderReconciler struct {
	client.Client
	Rows
	// Reader reads claims, Jobs and Pods without the manager's filtered cache.
	Reader   client.Reader
	Backends map[model.HomeMode]storage.Backend
	GIDs     ids.GroupIDs
	// Image is the javo image the prepare Job runs (`javo storage prepare`).
	Image podspec.Image
	// PerUserNamespaces is true in per-user namespace mode: a claim cannot
	// span namespaces, so folders are not offered.
	PerUserNamespaces bool
}

// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

// Reconcile moves one folder towards having a prepared claim.
func (r *SharedFolderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	got, err := r.Store.SharedFolders().Get(ctx, req.Name)
	if errors.Is(err, store.ErrNotFound) {
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	f, base := &got, got.DeepCopy()
	b, err := r.step(ctx, f, base)
	if errors.Is(err, errRowChanged) {
		return ctrl.Result{RequeueAfter: retryConflict}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	meta.SetStatusCondition(&f.Status.Conditions, rows.ReadyCondition("The folder is ready.", b))
	if err := rows.WriteFolder(ctx, r.Rows, f, base); errors.Is(err, errRowChanged) {
		return ctrl.Result{RequeueAfter: retryConflict}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	if b == nil {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: b.Retry}, nil
}

// step does the work of one pass. It changes f.Status in memory; Reconcile
// writes it. A returned *blocked is a state to report, not an error. base is
// the folder as stored, for a write in the middle of the pass.
func (r *SharedFolderReconciler) step(ctx context.Context, f, base *model.SharedFolder) (*blocked, error) {
	if r.PerUserNamespaces {
		return &blocked{Code: reasons.SharedFoldersUnavailable, Evidence: []string{"each person has a namespace of their own and a claim cannot span namespaces"}}, nil
	}
	cs, err := rows.Cluster(ctx, r.Rows)
	if err != nil {
		return nil, err
	}
	if f.Status.GID == 0 {
		gid, ok, err := r.GIDs.GID(ctx, f.Spec.Group)
		if err != nil {
			return nil, err
		}
		if !ok || gid == 0 {
			return &blocked{Code: reasons.GroupHasNoId, Evidence: []string{fmt.Sprintf("group %s", f.Spec.Group)}, Retry: homeWaitRetry}, nil
		}
		f.Status.GID = gid
	}
	if b, err := r.dirTaken(ctx, f); b != nil || err != nil {
		return b, err
	}
	if f.Status.ClaimName == "" {
		if b, err := r.bind(ctx, f, cs); b != nil || err != nil {
			return b, err
		}
	}
	claim := &corev1.PersistentVolumeClaim{}
	switch err := r.Reader.Get(ctx, client.ObjectKey{Namespace: f.Namespace, Name: f.Status.ClaimName}, claim); {
	case apierrors.IsNotFound(err):
		return &blocked{Code: reasons.FolderPending, Evidence: []string{fmt.Sprintf("claim %s not found", f.Status.ClaimName)}, Retry: homeClaimRetry}, nil
	case err != nil:
		return nil, err
	}
	if !f.Status.Prepared {
		return r.prepare(ctx, f, base)
	}
	return nil, nil
}

// classFor is the class a folder's claim is made on and the backend that
// shapes it: the folder's own class, else the admin's, else the class chosen
// for shared storage (all of them shared drives when the storage can do
// that), else, on one node only, the class of the homes. A named class that
// cannot be shared keeps the folder on the per-user backend, which offers it
// on one node only.
func (r *SharedFolderReconciler) classFor(f *model.SharedFolder, cs *model.ClusterStorage) (string, storage.Backend, bool) {
	if cs == nil || cs.Status.Choice == nil || cs.Status.Choice.Mode == "" {
		return "", nil, false
	}
	choice := cs.Status.Choice
	for _, class := range []string{f.Spec.StorageClassName, cs.Spec.SharedFolders.StorageClassName} {
		if class == "" {
			continue
		}
		if capabilityOf(cs, class).RWXFile.Value {
			return class, r.Backends[model.HomeModeShared], true
		}
		return class, r.Backends[model.HomeModePerUser], true
	}
	if choice.SharedClass != "" {
		return choice.SharedClass, r.Backends[model.HomeModeShared], true
	}
	return choice.HomeClass, r.Backends[choice.Mode], true
}

// ownsClaim is true for a claim labelled with this folder's name and stamped
// with its group. Claims outlive their folder, so both must agree: a new
// folder of the same name for another group never inherits the files.
func ownsClaim(c *corev1.PersistentVolumeClaim, f *model.SharedFolder) bool {
	group, stamped := c.Annotations[model.AnnotationGroup]
	return c.Labels[model.LabelGroupFolder] == f.Name && stamped && group == f.Spec.Group
}

// dirTaken refuses a folder whose mount directory (naming.GroupDir) is also
// another folder's, because pods mount one folder per directory. The one
// podstorage.FolderPerDir picks keeps it.
func (r *SharedFolderReconciler) dirTaken(ctx context.Context, f *model.SharedFolder) (*blocked, error) {
	all, err := r.Store.SharedFolders().List(ctx)
	if err != nil {
		return nil, err
	}
	all = slices.DeleteFunc(all, func(o model.SharedFolder) bool { return o.Namespace != f.Namespace })
	dir := naming.GroupDir(f.Spec.Group)
	for _, o := range podstorage.FolderPerDir(all) {
		if naming.GroupDir(o.Spec.Group) == dir && o.Name != f.Name {
			return &blocked{Code: reasons.FolderDirTaken, Evidence: []string{fmt.Sprintf("group %s already has folder %s at /shared/%s", o.Spec.Group, o.Name, dir)}, Retry: homeClaimRetry}, nil
		}
	}
	return nil, nil
}

// bind makes the folder's claim, or adopts the one an earlier pass made.
func (r *SharedFolderReconciler) bind(ctx context.Context, f *model.SharedFolder, cs *model.ClusterStorage) (*blocked, error) {
	name := naming.FolderClaimName(f.Name)
	existing := &corev1.PersistentVolumeClaim{}
	switch err := r.Reader.Get(ctx, client.ObjectKey{Namespace: f.Namespace, Name: name}, existing); {
	case err == nil:
		if !ownsClaim(existing, f) {
			return &blocked{Code: reasons.FolderClaimForeign, Evidence: []string{fmt.Sprintf("claim %s is not this folder's or belongs to another group; not using it", name)}, Retry: homeClaimRetry}, nil
		}
		f.Status.ClaimName = name
		return nil, nil
	case !apierrors.IsNotFound(err):
		return nil, err
	}

	class, backend, ok := r.classFor(f, cs)
	switch {
	case !ok:
		return &blocked{Code: reasons.FolderPending, Evidence: []string{"the cluster's storage has not been inspected yet"}, Retry: homeWaitRetry}, nil
	case class == "":
		return &blocked{Code: reasons.NoStorageClass, Evidence: []string{"no class chosen for folders or homes"}, Retry: homeClaimRetry}, nil
	case backend == nil:
		return &blocked{Code: reasons.FolderPending, Evidence: []string{fmt.Sprintf("no storage backend for mode %q", cs.Status.Choice.Mode)}, Retry: homeClaimRetry}, nil
	}
	size := resource.MustParse(defaultFolderSize)
	switch {
	case f.Spec.Size != nil:
		size = *f.Spec.Size
	case cs.Spec.SharedFolders.DefaultSize != nil:
		size = *cs.Spec.SharedFolders.DefaultSize
	}
	claim, err := backend.FolderClaim(f, class, size, cs.Status.Choice.SingleNode)
	var unavailable *storage.UnavailableError
	if errors.As(err, &unavailable) {
		return &blocked{Code: unavailable.Code, Evidence: []string{"no storage class that several machines can share"}, Retry: homeClaimRetry}, nil
	} else if err != nil {
		return nil, err
	}
	// Created without an owner reference, like homes: deleting the object keeps the files.
	if err := r.Create(ctx, claim); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return &blocked{Code: reasons.FolderPending, Evidence: []string{"claim " + name + " was just created"}, Retry: homeWaitRetry}, nil
		}
		return &blocked{Code: reasons.FolderPending, Evidence: []string{"create claim: " + err.Error()}, Retry: homeClaimRetry}, nil
	}
	f.Status.ClaimName = name
	return nil, nil
}

func folderPrepareLabels(f *model.SharedFolder) map[string]string {
	return map[string]string{model.LabelRole: model.RoleFolderPrepare, model.LabelGroupFolder: f.Name}
}

// prepareJob is the Job that makes the folder directory and gives it to the
// group. It mounts only the folder's claim (see buildPrepareJob).
func (r *SharedFolderReconciler) prepareJob(key client.ObjectKey, f *model.SharedFolder) *batchv1.Job {
	return buildPrepareJob(prepareJobSpec{
		Key: key, Labels: folderPrepareLabels(f), Image: r.Image, Claim: f.Status.ClaimName,
		MountPath: folderMountPath, Dir: folderDir, UID: folderUID, GID: f.Status.GID, Mode: folderMode,
	})
}

// prepare runs the Job that readies the directory and records status.prepared
// when it completes. A failed Job is shown as evidence until its TTL removes
// it; the next pass then tries again, up to maxPrepareAttempts.
func (r *SharedFolderReconciler) prepare(ctx context.Context, f, base *model.SharedFolder) (*blocked, error) {
	p := preparer{Client: r.Client, Reader: r.Reader}
	owner := jobOwner{
		Namespace: f.Namespace, Label: model.LabelGroupFolder, ID: f.Name, Attempts: f.Status.PrepareAttempts,
		Count: func(ctx context.Context, n int) error {
			f.Status.PrepareAttempts = n
			return rows.WriteFolder(ctx, r.Rows, f, base)
		},
	}
	res, err := p.run(ctx, owner, "folder", model.RoleFolderPrepare, func() *batchv1.Job {
		return r.prepareJob(client.ObjectKey{Namespace: f.Namespace, Name: prepareJobNameFor(f.Status.ClaimName)}, f)
	})
	if err != nil {
		return nil, err
	}
	switch res.outcome {
	case prepareDone:
		f.Status.Prepared = true
		return nil, nil
	case prepareForeign:
		return &blocked{Code: reasons.FolderPending, Evidence: res.evidence, Retry: homeClaimRetry}, nil
	case prepareRunning:
		if len(res.unmounted) > 0 {
			// Still looked at: a volume that mounts later finishes the folder.
			return &blocked{Code: reasons.FolderPrepareFailed, Evidence: res.evidence, Retry: jobPoll}, nil
		}
	case prepareRetry, prepareGaveUp:
		// Permissions are blamed only when the prepare step itself said so; a
		// Job that never ran (the volume never mounted) is a plain failure.
		code := reasons.FolderPrepareFailed
		if res.said {
			code = reasons.FolderPermissionsUnsupported
		}
		b := &blocked{Code: code, Evidence: res.evidence}
		if res.outcome == prepareRetry {
			b.Retry = homePrepareRetry
		}
		return b, nil
	}
	return &blocked{Code: reasons.FolderPreparing, Retry: jobPoll}, nil
}

// SetupWithManager reconciles folders, and every folder that is not ready
// when the cluster's storage findings change. A folder's gid comes from its
// group, so a new folder of a group waits for nothing else.
func (r *SharedFolderReconciler) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	if r.Store == nil || r.Reader == nil || r.GIDs == nil || len(r.Backends) == 0 || r.Image.Ref == "" {
		return errors.New("storage.SharedFolderReconciler needs Store, Reader, GIDs, Backends and Image")
	}
	all := func(ctx context.Context) ([]string, error) { return rows.FolderIDs(ctx, r.Rows, nil) }
	notReady := func(ctx context.Context, _ string) ([]string, error) {
		return rows.FolderIDs(ctx, r.Rows, func(f *model.SharedFolder) bool {
			return !meta.IsStatusConditionTrue(f.Status.Conditions, model.ConditionReady)
		})
	}
	return ctrl.NewControllerManagedBy(mgr).Named("storage-shared-folder").
		WatchesRawSource(managed.Source(dbsource.For(feed.Subscribe(), store.TableSharedFolders, 0, all))).
		WatchesRawSource(managed.Source(dbsource.Fan(feed.Subscribe(), store.TableStorageStatus, notReady))).
		Complete(r)
}

package storage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
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
	prepareMountPath = "/home-root"
	prepareMode      = "0775"
	prepareJobTTL    = 300
	// prepareDeadline fails a Job whose pod never starts (the claim cannot be
	// provisioned or attached), so the home shows why instead of waiting.
	prepareDeadline = 600

	// defaultHomeSize applies when neither the Home nor ClusterStorage names one.
	defaultHomeSize = "10Gi"

	homeWaitRetry    = 5 * time.Second
	homeClaimRetry   = 30 * time.Second
	homePrepareRetry = time.Minute
	homeFirstGen     = 1
	// maxPrepareAttempts bounds the prepare Jobs made for one Home. The last
	// Job has no TTL: it stays as the evidence and nothing replaces it.
	maxPrepareAttempts = 3
	quotaExceededText  = "exceeded quota"
)

// HomeReconciler gives every Home its storage: it makes (or adopts) the
// claim, readies the volume once with a prepare Job, and keeps the claim's
// size in step with the Home. It never moves a Home to another mode and never
// shrinks or deletes a claim.
type HomeReconciler struct {
	client.Client
	Rows
	// Reader reads claims, Jobs and Pods without the manager's filtered cache.
	Reader client.Reader
	// Backends are the claim shapes by mode.
	Backends map[model.HomeMode]storage.Backend
	UIDs     ids.UserIDs
	// Image is the javo image the prepare Job runs (`javo storage prepare`).
	Image podspec.Image
}

// RBAC: see internal/controller/workspace/reconciler.go for how the chart's
// Roles are checked against these markers.
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

// Reconcile moves one home towards having a prepared claim.
func (r *HomeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	h, err := r.Store.Homes().Get(ctx, req.Name)
	if errors.Is(err, store.ErrNotFound) {
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	base := h.DeepCopy()
	b, err := r.step(ctx, &h, base)
	if errors.Is(err, errRowChanged) {
		return ctrl.Result{RequeueAfter: retryConflict}, nil // the home changed since it was read
	} else if err != nil {
		return ctrl.Result{}, err
	}
	r.setReady(&h, b)
	// The write names the version this pass read: the migration reconciler
	// writes the same row, and its Migrating=True (the hold on the owner's
	// apps) must not be written over by a pass that read the home before.
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

// errRowChanged is a write in the middle of a pass that lost the race to
// another writer; the pass is run again.
var errRowChanged = errors.New("the row changed since it was read")

// setReady records the Ready condition; the catalog supplies the text.
func (r *HomeReconciler) setReady(h *model.Home, b *blocked) {
	meta.SetStatusCondition(&h.Status.Conditions, readyCondition("The home is ready.", b))
}

// step does the work of one pass. It changes h in memory; Reconcile writes
// it. A returned *blocked is a state to report, not an error. base is the
// home as stored, for a write in the middle of the pass.
func (r *HomeReconciler) step(ctx context.Context, h, base *model.Home) (*blocked, error) {
	cs, err := r.cluster(ctx)
	if err != nil {
		return nil, err
	}
	if h.Status.ClaimName == "" {
		if b, err := r.bind(ctx, h, cs); b != nil || err != nil {
			return b, err
		}
	}
	claim := &corev1.PersistentVolumeClaim{}
	err = r.Reader.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: h.Status.ClaimName}, claim)
	switch {
	case apierrors.IsNotFound(err):
		return &blocked{code: reasons.HomePending, evidence: []string{fmt.Sprintf("claim %s not found", h.Status.ClaimName)}, retry: homeClaimRetry}, nil
	case err != nil:
		return nil, err
	}
	sizeProblem, err := r.resize(ctx, h, claim, cs)
	if err != nil {
		return nil, err
	}
	if !h.Status.Prepared {
		if b, err := r.prepare(ctx, h, base, cs); b != nil || err != nil {
			return b, err
		}
	}
	return sizeProblem, nil
}

// wantSize is the size the Home asks for: its own, else the cluster's
// default, else 10Gi.
func wantSize(h *model.Home, cs *model.ClusterStorage) resource.Quantity {
	switch {
	case h.Spec.Size != nil:
		return *h.Spec.Size
	case cs != nil && cs.Spec.Home.Size != nil:
		return *cs.Spec.Home.Size
	}
	return resource.MustParse(defaultHomeSize)
}

func capabilityOf(cs *model.ClusterStorage, class string) model.ClassCapability {
	if cs != nil {
		if i := slices.IndexFunc(cs.Status.Classes, func(c model.ClassCapability) bool { return c.Name == class }); i >= 0 {
			return cs.Status.Classes[i]
		}
	}
	return model.ClassCapability{}
}

// owns is true for a claim labelled with this owner. An owner-id annotation,
// when present, must agree: two slugs that collide never share a home.
func owns(c *corev1.PersistentVolumeClaim, owner string) bool {
	if c.Labels[model.LabelOwner] != owner {
		return false
	}
	id, annotated := c.Annotations[model.AnnotationOwnerID]
	return !annotated || id == owner
}

// liveClaimName is the claim a Home without one belongs on: the owner's claim
// a mode change marked live (the highest generation if several), else the
// first generation's name.
func (r *HomeReconciler) liveClaimName(ctx context.Context, h *model.Home) (string, error) {
	var l corev1.PersistentVolumeClaimList
	if err := r.Reader.List(ctx, &l, client.InNamespace(h.Namespace), client.MatchingLabels{
		model.LabelOwner: h.Spec.Owner, model.LabelLive: "true"}); err != nil {
		return "", err
	}
	best := homeFirstGen
	for i := range l.Items {
		c := &l.Items[i]
		if gen := naming.HomeClaimGen(h.Spec.Owner, c.Name); gen > best && owns(c, h.Spec.Owner) && c.DeletionTimestamp.IsZero() {
			best = gen
		}
	}
	return naming.HomeClaimNameGen(h.Spec.Owner, best), nil
}

// bind gives a Home without a claim one: an earlier claim of the same owner
// is adopted as it is, otherwise the backend of the chosen mode creates it.
func (r *HomeReconciler) bind(ctx context.Context, h *model.Home, cs *model.ClusterStorage) (*blocked, error) {
	name, err := r.liveClaimName(ctx, h)
	if err != nil {
		return nil, err
	}
	existing := &corev1.PersistentVolumeClaim{}
	switch err := r.Reader.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: name}, existing); {
	case err == nil:
		if !owns(existing, h.Spec.Owner) {
			return &blocked{code: reasons.HomeClaimForeign, evidence: []string{fmt.Sprintf("claim %s is not labelled as belonging to this home's owner", name)}}, nil
		}
		class := ptr.Deref(existing.Spec.StorageClassName, "")
		mode := model.HomeModePerUser
		if slices.Contains(existing.Spec.AccessModes, corev1.ReadWriteMany) {
			mode = model.HomeModeShared
		}
		// The claim is left as it is: the Home takes its size, so the cluster
		// default cannot grow or "shrink" a disk that was never asked for it.
		have := existing.Spec.Resources.Requests[corev1.ResourceStorage]
		if h.Spec.Size == nil || h.Spec.Size.Cmp(have) != 0 {
			h.Spec.Size = &have
		}
		recordClaim(h, mode, name, class, cs)
		return nil, nil
	case !apierrors.IsNotFound(err):
		return nil, err
	}

	if cs == nil || cs.Status.Choice == nil || cs.Status.Choice.Mode == "" {
		return &blocked{code: reasons.HomePending, evidence: []string{"the cluster's storage has not been inspected yet"}, retry: homeWaitRetry}, nil
	}
	choice := cs.Status.Choice
	if choice.HomeClass == "" {
		code := reasons.NoDefaultStorageClass
		if len(cs.Status.Classes) == 0 {
			code = reasons.NoStorageClass
		}
		return &blocked{code: code, evidence: []string{fmt.Sprintf("%d StorageClasses found, none chosen for homes", len(cs.Status.Classes))}, retry: homeClaimRetry}, nil
	}
	backend, ok := r.Backends[choice.Mode]
	if !ok {
		return &blocked{code: reasons.HomePending, evidence: []string{fmt.Sprintf("no storage backend for mode %q", choice.Mode)}, retry: homeClaimRetry}, nil
	}
	size := wantSize(h, cs)
	if max := cs.Spec.Home.MaxSize; max != nil && size.Cmp(*max) > 0 {
		return &blocked{code: reasons.HomeTooLarge, evidence: []string{fmt.Sprintf("asked for %s, the largest allowed is %s", size.String(), max.String())}}, nil
	}
	claim := backend.HomeClaim(h, choice.HomeClass, size)
	claim.Name = name
	if err := r.Create(ctx, claim); err != nil {
		switch {
		case apierrors.IsForbidden(err) && strings.Contains(err.Error(), quotaExceededText):
			return &blocked{code: reasons.TeamStorageFull, evidence: []string{"namespace " + h.Namespace + ": " + err.Error()}, retry: homeClaimRetry}, nil
		case apierrors.IsAlreadyExists(err):
			return &blocked{code: reasons.HomePending, evidence: []string{"claim " + name + " was just created"}, retry: homeWaitRetry}, nil
		}
		// A LimitRange, an admission webhook or a bad class: the Home says so
		// instead of looking like it is still starting.
		return &blocked{code: reasons.HomePending, evidence: []string{"create claim: " + err.Error()}, retry: homeClaimRetry}, nil
	}
	if choice.SingleNode && backend.Mode() == model.HomeModePerUser {
		h.Status.CreatedOnSingleNode = true
	}
	recordClaim(h, backend.Mode(), name, choice.HomeClass, cs)
	return nil, nil
}

// recordClaim points h at claim; the mode migration uses it on a switch too.
func recordClaim(h *model.Home, mode model.HomeMode, claim, class string, cs *model.ClusterStorage) {
	h.Status.Mode, h.Status.ClaimName, h.Status.StorageClassName = mode, claim, class
	h.Status.SizeEnforced = capabilityOf(cs, class).SizeEnforced.Value
}

// resize keeps the claim at the size the Home asks for. Growing needs a class
// that expands; shrinking is never done. Either refusal is returned as the
// reason to show, and does not stop the home from being used at its size.
func (r *HomeReconciler) resize(ctx context.Context, h *model.Home, claim *corev1.PersistentVolumeClaim, cs *model.ClusterStorage) (*blocked, error) {
	have := claim.Spec.Resources.Requests[corev1.ResourceStorage]
	want := wantSize(h, cs)
	switch cmp := want.Cmp(have); {
	case cmp == 0:
		return nil, nil
	case cmp < 0:
		return &blocked{code: reasons.HomeCannotShrink, evidence: []string{fmt.Sprintf("claim %s is %s, the home asks for %s", claim.Name, have.String(), want.String())}}, nil
	}
	if cs != nil && cs.Spec.Home.MaxSize != nil && want.Cmp(*cs.Spec.Home.MaxSize) > 0 {
		return &blocked{code: reasons.HomeTooLarge, evidence: []string{fmt.Sprintf("asked for %s, the largest allowed is %s", want.String(), cs.Spec.Home.MaxSize.String())}}, nil
	}
	if !capabilityOf(cs, h.Status.StorageClassName).Expansion.Value {
		return &blocked{code: reasons.HomeCannotGrow, evidence: []string{fmt.Sprintf("claim %s is %s, the home asks for %s, and StorageClass %q cannot expand", claim.Name, have.String(), want.String(), h.Status.StorageClassName)}}, nil
	}
	if claim.Status.Phase != corev1.ClaimBound {
		// The API refuses to resize a claim that is not bound yet. The
		// prepare Job binds it, and its completion brings the next pass.
		return nil, nil
	}
	grown := claim.DeepCopy()
	grown.Spec.Resources.Requests[corev1.ResourceStorage] = want
	if err := r.Patch(ctx, grown, client.MergeFrom(claim)); err != nil {
		return nil, fmt.Errorf("grow home claim: %w", err)
	}
	return nil, nil
}

func prepareLabels(owner string) map[string]string {
	return map[string]string{model.LabelRole: model.RoleHomePrepare, model.LabelOwner: owner}
}

// prepare runs the one-time Job that readies the volume and records
// status.prepared when it completes.
func (r *HomeReconciler) prepare(ctx context.Context, h, base *model.Home, cs *model.ClusterStorage) (*blocked, error) {
	uid, gid, err := r.UIDs.UID(ctx, h.Spec.Owner)
	if errors.Is(err, ids.ErrNoUID) {
		return &blocked{code: reasons.IdentityPending, evidence: []string{"the owner has no user id yet"}, retry: homeWaitRetry}, nil
	} else if err != nil {
		return nil, err
	}
	res, err := r.preparer().run(ctx, r.jobOwner(h, base), "home", model.RoleHomePrepare, func() *batchv1.Job {
		key := client.ObjectKey{Namespace: h.Namespace, Name: prepareJobNameFor(h.Status.ClaimName)}
		return r.prepareJob(key, h, uid, gid, r.coLocate(h, cs))
	})
	if err != nil {
		return nil, err
	}
	switch res.outcome {
	case prepareDone:
		h.Status.Prepared = true
		return nil, nil
	case prepareForeign:
		return &blocked{code: reasons.HomePending, evidence: res.evidence, retry: homeClaimRetry}, nil
	case prepareRunning:
		if len(res.unmounted) > 0 {
			// Still looked at: a volume that mounts later finishes the home.
			return &blocked{code: reasons.HomeMountFailed, evidence: res.evidence, retry: jobPoll}, nil
		}
	case prepareRetry, prepareGaveUp:
		b := &blocked{code: reasons.HomePrepareFailed, evidence: res.evidence}
		if len(res.unmounted) > 0 {
			b.code = reasons.HomeMountFailed
		}
		if res.outcome == prepareRetry {
			// The Job is kept until its TTL removes it; the next pass then runs a new one.
			b.retry = homePrepareRetry
		}
		return b, nil
	}
	// Helper Jobs are not in the manager's cache, so a running one is looked
	// at again rather than watched.
	return &blocked{code: reasons.HomePreparing, retry: jobPoll}, nil
}

// jobPoll is how often a running helper Job is looked at.
const jobPoll = 3 * time.Second

func (r *HomeReconciler) preparer() preparer {
	return preparer{Client: r.Client, Reader: r.Reader}
}

// jobOwner is h as the owner of its helper Jobs. Counting an attempt writes
// the row at once, against base.
func (r *HomeReconciler) jobOwner(h, base *model.Home) jobOwner {
	return homeJobOwner(r.Rows, h, base)
}

// homeJobOwner is h as the owner of the Jobs made for it (prepare, copy).
func homeJobOwner(rw Rows, h, base *model.Home) jobOwner {
	return jobOwner{
		Namespace: h.Namespace, Label: model.LabelHome, ID: h.ID, Attempts: h.Status.PrepareAttempts,
		Count: func(ctx context.Context, n int) error {
			h.Status.PrepareAttempts = n
			if conflict, err := rw.writeHome(ctx, h, base); conflict {
				return errRowChanged
			} else if err != nil {
				return err
			}
			return nil
		},
	}
}

// coLocate is true when the home's disk attaches to one node at a time and the
// cluster has more than one.
func (r *HomeReconciler) coLocate(h *model.Home, cs *model.ClusterStorage) bool {
	return podstorage.CoLocate(r.Backends, h, cs)
}

// prepareJob is the Job that readies the volume root of a Home's claim.
func (r *HomeReconciler) prepareJob(key client.ObjectKey, h *model.Home, uid, gid int64, coLocate bool) *batchv1.Job {
	return homePrepareJob(r.Image, key, h.Spec.Owner, h.Status.ClaimName, uid, gid, coLocate)
}

// homePrepareJob readies claim for owner; a home's first claim and the claim
// a mode change copies to are readied the same way.
func homePrepareJob(image podspec.Image, key client.ObjectKey, owner, claim string, uid, gid int64, coLocate bool) *batchv1.Job {
	var affinity *corev1.Affinity
	if coLocate {
		// A disk that attaches to one node: run where the owner's pods already are.
		affinity = &corev1.Affinity{PodAffinity: &corev1.PodAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{model.LabelOwner: owner}},
			TopologyKey:   corev1.LabelHostname,
		}}}}
	}
	return buildPrepareJob(prepareJobSpec{
		Key: key, Labels: prepareLabels(owner), Image: image, Claim: claim,
		MountPath: prepareMountPath, Dir: prepareMountPath, UID: uid, GID: gid, Mode: prepareMode, NoMkdir: true,
		Affinity: affinity,
	})
}

// SetupWithManager reconciles homes, and every home that is not ready when
// the cluster's storage findings change (a new class, a raised limit).
func (r *HomeReconciler) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	if r.Store == nil || r.Reader == nil || r.UIDs == nil || len(r.Backends) == 0 || r.Image.Ref == "" {
		return errors.New("storage.HomeReconciler needs Store, Reader, UIDs, Backends and Image")
	}
	all := func(ctx context.Context) ([]reconcile.Request, error) { return r.homeRequests(ctx, nil) }
	notReady := func(ctx context.Context, _ string) ([]reconcile.Request, error) {
		return r.homeRequests(ctx, notReadyHome)
	}
	return ctrl.NewControllerManagedBy(mgr).Named("storage-home").
		WatchesRawSource(dbsource.For(feed.Subscribe(), store.TableHomes, 0, all)).
		WatchesRawSource(dbsource.Fan(feed.Subscribe(), store.TableStorageStatus, notReady)).
		Complete(r)
}

package storage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
	// CopyOldMount and CopyNewMount are where the copy Job sees the two
	// claims; `javo storage copy` (internal/storage/homecopy) copies and
	// checks between them.
	CopyOldMount = "/old"
	CopyNewMount = "/new"

	// copyJobTTL keeps a finished copy Job (and its pod's logs) for a day.
	copyJobTTL = 24 * 60 * 60
	// copyDeadline fails a copy that has not finished after six hours, so a
	// stuck mount shows as a failure instead of a copy that never ends.
	copyDeadline = 6 * 60 * 60

	// evidenceLines caps what a failed move keeps of the copy's output.
	evidenceLines = 25
	// messageLines is how much of it goes into the condition message.
	messageLines = 4

	reasonSwitched = "Switched"
	reasonCopying  = "Copying"
	reasonWaiting  = "Waiting"
)

// MigrationReconciler moves a Home to the chosen mode when an admin has
// scheduled it, and only then. It waits until the owner's pods are gone,
// copies the files to a new claim with a watched copy Job, switches the Home
// to the new claim after the copy verified clean, and deletes the old claim
// when its retention is over. A copy that fails in any way leaves the Home
// on its old claim.
type MigrationReconciler struct {
	client.Client
	Rows
	// Reader reads claims, Jobs and Pods without the manager's filtered cache.
	Reader client.Reader
	// Backends are the claim shapes by mode.
	Backends map[model.HomeMode]storage.Backend
	UIDs     ids.UserIDs
	// Storage resolves the owner's group folders; their gids become the copy
	// Job's supplemental groups.
	Storage podstorage.Resolver
	// Image, the javo image, readies the new claim (`javo storage prepare`)
	// and copies the files (`javo storage copy`).
	Image podspec.Image
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

func (r *MigrationReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile moves one home one step along its move, if one is scheduled.
func (r *MigrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	got, err := r.Store.Homes().Get(ctx, req.Name)
	if errors.Is(err, store.ErrNotFound) {
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	cs, err := r.cluster(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	h, base := &got, got.DeepCopy()
	res, err := r.step(ctx, h, cs, base)
	if errors.Is(err, errRowChanged) {
		return ctrl.Result{RequeueAfter: retryConflict}, nil // the home changed since it was read
	} else if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.writeStatus(ctx, h, base); errors.Is(err, errRowChanged) {
		// Another writer (the home reconciler) got in first; the next pass
		// derives the same status.
		return ctrl.Result{RequeueAfter: retryConflict}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	return res, nil
}

// writeStatus stores h against base and makes base the written state, so a
// second write in the same pass names the version the first one made. The
// write names the version it read: the home reconciler writes the same row.
func (r *MigrationReconciler) writeStatus(ctx context.Context, h, base *model.Home) error {
	if conflict, err := r.writeHome(ctx, h, base); conflict {
		return errRowChanged
	} else if err != nil {
		return err
	}
	return nil
}

func (r *MigrationReconciler) step(ctx context.Context, h *model.Home, cs *model.ClusterStorage, base *model.Home) (ctrl.Result, error) {
	m := h.Status.Migration
	if m != nil && m.Phase == model.MigrationSwitched {
		if !r.movesAgain(h, cs) {
			return r.retain(ctx, h)
		}
		// A schedule for another mode comes before the old claim's grace
		// period. The old claim keeps its label, and the retention sweep of
		// the round that ends it deletes it once its date is over.
		h.Status.Migration, m = nil, nil
	}
	if cs == nil || cs.Status.Choice == nil || cs.Status.Choice.Mode == "" || h.Status.ClaimName == "" || !h.Status.Prepared {
		return ctrl.Result{}, nil
	}
	choice := *cs.Status.Choice
	startAt := cs.Spec.Migration.StartAt
	if h.Status.Mode == choice.Mode || startAt == nil || cs.Spec.Migration.To != choice.Mode {
		// Nothing to move, no move scheduled (or it was taken back), or the
		// schedule is for another mode than the one now chosen: a mode change
		// alone never touches a home, and a finished round's start time, which
		// stays in the past, is not a schedule for the next one.
		return ctrl.Result{}, r.cancel(ctx, h)
	}
	attempt := cs.Spec.Migration.Attempt
	if m != nil && m.Phase == model.MigrationFailed && attempt <= m.Attempt {
		return ctrl.Result{}, nil // stays failed until an admin bumps the attempt
	}
	if m == nil || m.Phase == model.MigrationFailed || m.Phase == model.MigrationScheduled {
		now := r.now()
		if now.Before(startAt.Time) {
			h.Status.Migration = &model.HomeMigration{Phase: model.MigrationScheduled, Attempt: attempt}
			return ctrl.Result{RequeueAfter: startAt.Sub(now)}, nil
		}
		h.Status.Migration = &model.HomeMigration{Phase: model.MigrationWaiting, Attempt: attempt}
	}
	switch h.Status.Migration.Phase {
	case model.MigrationWaiting:
		return r.wait(ctx, h, cs, base)
	default:
		return r.copy(ctx, h, cs, base, choice)
	}
}

// movesAgain is true for a home that is on another mode than the chosen one
// while a move to the chosen mode is scheduled.
func (r *MigrationReconciler) movesAgain(h *model.Home, cs *model.ClusterStorage) bool {
	return cs != nil && cs.Status.Choice != nil && cs.Status.Choice.Mode != "" &&
		h.Status.Mode != cs.Status.Choice.Mode &&
		cs.Spec.Migration.StartAt != nil && cs.Spec.Migration.To == cs.Status.Choice.Mode
}

// wait holds the home until the owner has no pod left. Migrating=True is the
// hold: the workspace reconciler stops running apps and keeps new ones off
// (HomeMigrating), so a user who ignored the banner sees why.
func (r *MigrationReconciler) wait(ctx context.Context, h *model.Home, cs *model.ClusterStorage, base *model.Home) (ctrl.Result, error) {
	pods, err := r.ownerPods(ctx, h)
	if err != nil {
		return ctrl.Result{}, err
	}
	setMigrating(h, metav1.ConditionTrue, reasonWaiting, "Waiting for the owner's apps to stop.")
	if len(pods) > 0 {
		return ctrl.Result{RequeueAfter: homeWaitRetry}, nil
	}
	name, err := r.nextClaim(ctx, h)
	if err != nil {
		return ctrl.Result{}, err
	}
	m := h.Status.Migration
	m.Phase, m.NewClaim = model.MigrationCopying, name
	setMigrating(h, metav1.ConditionTrue, reasonCopying, "Copying the home's files to "+string(r.targetMode(h))+" storage.")
	// Recorded before the claim exists, so a crash cannot leave a claim no
	// status points at.
	if err := r.writeStatus(ctx, h, base); err != nil {
		return ctrl.Result{}, err
	}
	return r.copy(ctx, h, cs, base, *cs.Status.Choice)
}

func (r *MigrationReconciler) targetMode(h *model.Home) model.HomeMode {
	if h.Status.Mode == model.HomeModeShared {
		return model.HomeModePerUser
	}
	return model.HomeModeShared
}

// ownerPods names the pods of the owner that are alive: apps and file pods.
// The storage Jobs of the owner do not count. It is checked again right before
// the copy starts and before the switch, because a pod can be made by a pass
// that read the Home before the hold was written.
func (r *MigrationReconciler) ownerPods(ctx context.Context, h *model.Home) ([]string, error) {
	var pods corev1.PodList
	if err := r.Reader.List(ctx, &pods, client.InNamespace(h.Namespace), client.MatchingLabels{model.LabelOwner: h.Spec.Owner}); err != nil {
		return nil, fmt.Errorf("list the owner's pods: %w", err)
	}
	var names []string
	for _, p := range pods.Items {
		role := p.Labels[model.LabelRole]
		finished := p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
		if role != model.RoleHomePrepare && role != model.RoleHomeMigrate && !finished {
			names = append(names, p.Name)
		}
	}
	slices.Sort(names)
	return names, nil
}

// nextClaim is the first generation above the home's claim whose name is
// free, so a claim a failed attempt left terminating is never reused.
func (r *MigrationReconciler) nextClaim(ctx context.Context, h *model.Home) (string, error) {
	gen := max(naming.HomeClaimGen(h.Spec.Owner, h.Status.ClaimName), 1)
	for {
		gen++
		name := naming.HomeClaimNameGen(h.Spec.Owner, gen)
		err := r.Reader.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: name}, &corev1.PersistentVolumeClaim{})
		switch {
		case apierrors.IsNotFound(err):
			return name, nil
		case err != nil:
			return "", err
		}
	}
}

// copy makes and readies the new claim, runs the copy Job and, when it
// succeeded, switches the home.
func (r *MigrationReconciler) copy(ctx context.Context, h *model.Home, cs *model.ClusterStorage, base *model.Home, choice model.StorageChoice) (ctrl.Result, error) {
	m := h.Status.Migration
	setMigrating(h, metav1.ConditionTrue, reasonCopying, "Copying the home's files to "+string(choice.Mode)+" storage.")
	backend, ok := r.Backends[choice.Mode]
	if !ok {
		return r.fail(ctx, h, []string{fmt.Sprintf("no storage backend for mode %q", choice.Mode)})
	}
	uid, gid, err := r.UIDs.UID(ctx, h.Spec.Owner)
	if errors.Is(err, ids.ErrNoUID) {
		return ctrl.Result{RequeueAfter: homeWaitRetry}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}

	groupIDs, err := r.Storage.FolderGIDs(ctx, h.Namespace, h.Spec.Owner)
	if err != nil {
		return ctrl.Result{}, err
	}

	claim := &corev1.PersistentVolumeClaim{}
	switch err := r.Reader.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: m.NewClaim}, claim); {
	case apierrors.IsNotFound(err):
		nc := backend.HomeClaim(h, choice.HomeClass, wantSize(h, cs))
		nc.Name = m.NewClaim
		if err := r.Create(ctx, nc); err != nil && !apierrors.IsAlreadyExists(err) {
			return r.fail(ctx, h, []string{"create claim " + m.NewClaim + ": " + err.Error()})
		}
	case err != nil:
		return ctrl.Result{}, err
	case !owns(claim, h.Spec.Owner):
		return r.fail(ctx, h, []string{fmt.Sprintf("claim %s exists and is not this home's owner's; not using it", m.NewClaim)})
	case !claim.DeletionTimestamp.IsZero():
		return ctrl.Result{RequeueAfter: homeWaitRetry}, nil
	}

	// The new volume is readied by its own Job, which mounts only it: the copy
	// Job mounts two volumes and so must not set fsGroup.
	prep, err := preparer{Client: r.Client, Reader: r.Reader}.run(ctx, homeJobOwner(r.Rows, h, base), "home", model.RoleHomePrepare, func() *batchv1.Job {
		key := client.ObjectKey{Namespace: h.Namespace, Name: prepareJobNameFor(m.NewClaim)}
		return homePrepareJob(r.Image, key, h.Spec.Owner, m.NewClaim, uid, gid, false)
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	switch prep.outcome {
	case prepareRunning:
		return ctrl.Result{RequeueAfter: homeWaitRetry}, nil
	case prepareDone:
	default:
		return r.fail(ctx, h, append([]string{"preparing the new claim failed"}, prep.evidence...))
	}

	job := r.copyJob(h, uid, gid, groupIDs)
	existing := &batchv1.Job{}
	switch err := r.Reader.Get(ctx, client.ObjectKeyFromObject(job), existing); {
	case apierrors.IsNotFound(err):
		// Last look before the old claim is read: a pod now would write to it
		// during the copy. The new claim stays; the next pass resumes here.
		if pods, err := r.ownerPods(ctx, h); err != nil {
			return ctrl.Result{}, err
		} else if len(pods) > 0 {
			setMigrating(h, metav1.ConditionTrue, reasonWaiting, "Waiting for the owner's apps to stop.")
			return ctrl.Result{RequeueAfter: homeWaitRetry}, nil
		}
		if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("create copy job: %w", err)
		}
		m.Job = job.Name
		return ctrl.Result{RequeueAfter: homeWaitRetry}, nil
	case err != nil:
		return ctrl.Result{}, err
	case existing.Labels[model.LabelHome] != h.ID || existing.Labels[model.LabelRole] != model.RoleHomeMigrate:
		return r.fail(ctx, h, []string{fmt.Sprintf("job %s exists and is not this home's copy job; not using it", existing.Name)})
	}
	m.Job = existing.Name
	switch {
	case jobHas(existing, batchv1.JobComplete):
		// Writes made after the copy passed would be dropped at the switch, so a
		// pod that exists now fails the move; the admin retries once it is gone.
		pods, err := r.ownerPods(ctx, h)
		if err != nil {
			return ctrl.Result{}, err
		}
		if len(pods) > 0 {
			return r.fail(ctx, h, []string{"pod " + strings.Join(pods, ", ") + " of the owner is running, so it may have written to the old claim during the copy; nothing was switched"})
		}
		return r.switchClaim(ctx, h, cs, choice)
	case jobHas(existing, batchv1.JobFailed):
		var pods corev1.PodList
		if err := r.Reader.List(ctx, &pods, client.InNamespace(h.Namespace), client.MatchingLabels{batchv1.JobNameLabel: existing.Name}); err != nil {
			return ctrl.Result{}, fmt.Errorf("list copy pods: %w", err)
		}
		return r.fail(ctx, h, jobEvidence(existing, pods.Items))
	}
	return ctrl.Result{RequeueAfter: homeWaitRetry}, nil
}

func copyJobName(newClaim string, attempt int32) string {
	return naming.NamespaceName(model.ProductName+"-migrate", newClaim+"-a"+strconv.Itoa(int(attempt)))
}

// copyJob is the Job that copies the old claim to the new one and checks the
// result (`javo storage copy`, in the javo image). It runs as the owner, mounts the old
// claim read-only, adds the owner's folder gids as supplemental groups and sets no fsGroup (it mounts two volumes). The attempt is
// in its name: a retry never meets the Job of the one before.
func (r *MigrationReconciler) copyJob(h *model.Home, uid, gid int64, groups []int64) *batchv1.Job {
	m := h.Status.Migration
	labels := map[string]string{model.LabelRole: model.RoleHomeMigrate, model.LabelOwner: h.Spec.Owner, model.LabelHome: h.ID}
	claim := func(name string, ro bool) corev1.VolumeSource {
		return corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: name, ReadOnly: ro}}
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: copyJobName(m.NewClaim, m.Attempt), Namespace: h.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(int32(0)),
			TTLSecondsAfterFinished: ptr.To(int32(copyJobTTL)),
			ActiveDeadlineSeconds:   ptr.To(int64(copyDeadline)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptr.To(true),
						RunAsUser:    ptr.To(uid),
						RunAsGroup:   ptr.To(gid),
						// The copy can only set groups the process is in: a file
						// of a team group would otherwise keep the wrong gid.
						SupplementalGroups: groups,
						SeccompProfile:     &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:    "copy",
						Image:   r.Image.Ref,
						Command: []string{"/" + model.ProductName, "storage", "copy", "--old", CopyOldMount, "--new", CopyNewMount},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "old", MountPath: CopyOldMount, ReadOnly: true},
							{Name: "new", MountPath: CopyNewMount},
						},
						TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							ReadOnlyRootFilesystem:   ptr.To(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "old", VolumeSource: claim(h.Status.ClaimName, true)},
						{Name: "new", VolumeSource: claim(m.NewClaim, false)},
					},
				},
			},
		},
	}
	r.Image.Pull(&job.Spec.Template.Spec)
	return job
}

// switchClaim points the home at the new claim. The old claim is labelled
// first, so a pass that dies in between repeats harmlessly.
func (r *MigrationReconciler) switchClaim(ctx context.Context, h *model.Home, cs *model.ClusterStorage, choice model.StorageChoice) (ctrl.Result, error) {
	m := h.Status.Migration
	keep := 7 * 24 * time.Hour
	if d := cs.Spec.Migration.KeepOldFor; d != nil {
		keep = d.Duration
	}
	until := metav1.NewTime(r.now().Add(keep))

	old := &corev1.PersistentVolumeClaim{}
	switch err := r.Reader.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: h.Status.ClaimName}, old); {
	case err == nil:
		marked := old.DeepCopy()
		if marked.Labels == nil {
			marked.Labels = map[string]string{}
		}
		marked.Labels[model.LabelRetainUntil] = until.UTC().Format(time.DateOnly)
		delete(marked.Labels, model.LabelLive)
		if err := r.Patch(ctx, marked, client.MergeFrom(old)); err != nil {
			return ctrl.Result{}, fmt.Errorf("label the old claim: %w", err)
		}
	case !apierrors.IsNotFound(err):
		return ctrl.Result{}, err
	}
	if err := r.setLive(ctx, h.Namespace, m.NewClaim, h.Spec.Owner); err != nil {
		return ctrl.Result{}, err
	}
	oldName := h.Status.ClaimName
	recordClaim(h, choice.Mode, m.NewClaim, choice.HomeClass, cs)
	h.Status.Migration = &model.HomeMigration{
		Phase: model.MigrationSwitched, Job: m.Job, OldClaim: oldName, RetainUntil: &until, Attempt: m.Attempt,
	}
	setMigrating(h, metav1.ConditionFalse, reasonSwitched, "The home now uses "+string(choice.Mode)+" storage.")
	return ctrl.Result{RequeueAfter: keep}, nil
}

// setLive marks the owner's claim name as the one that holds the home's files.
func (r *MigrationReconciler) setLive(ctx context.Context, ns, name, owner string) error {
	c := &corev1.PersistentVolumeClaim{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, c); err != nil {
		return fmt.Errorf("mark the new claim live: %w", err)
	}
	if !owns(c, owner) {
		return fmt.Errorf("claim %s is not this home's owner's", name)
	}
	marked := c.DeepCopy()
	if marked.Labels == nil {
		marked.Labels = map[string]string{}
	}
	marked.Labels[model.LabelLive] = "true"
	if err := r.Patch(ctx, marked, client.MergeFrom(c)); err != nil {
		return fmt.Errorf("mark the new claim live: %w", err)
	}
	return nil
}

// retain deletes the old claim once its retention is over. Only a claim that
// is the home's owner's, carries the retain-until label and is not the live
// claim is deleted, and only the very object that was read.
func (r *MigrationReconciler) retain(ctx context.Context, h *model.Home) (ctrl.Result, error) {
	m := h.Status.Migration
	if m.RetainUntil != nil {
		if wait := m.RetainUntil.Sub(r.now()); wait > 0 {
			return ctrl.Result{RequeueAfter: wait}, nil
		}
	}
	old := &corev1.PersistentVolumeClaim{}
	err := r.Reader.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: m.OldClaim}, old)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return ctrl.Result{}, err
	case m.OldClaim != h.Status.ClaimName && owns(old, h.Spec.Owner) && old.Labels[model.LabelRetainUntil] != "" && old.DeletionTimestamp.IsZero():
		if err := r.Delete(ctx, old, client.Preconditions{UID: &old.UID}); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("delete the old claim: %w", err)
		}
	}
	if err := r.sweep(ctx, h); err != nil {
		return ctrl.Result{}, err
	}
	h.Status.Migration = nil
	return ctrl.Result{}, nil
}

// sweep deletes the owner's other claims whose retain-until day is over: the
// ones kept by an earlier round that a later round replaced in the status.
// The live claim and the one being copied to are never touched.
func (r *MigrationReconciler) sweep(ctx context.Context, h *model.Home) error {
	var l corev1.PersistentVolumeClaimList
	if err := r.Reader.List(ctx, &l, client.InNamespace(h.Namespace), client.MatchingLabels{model.LabelOwner: h.Spec.Owner}); err != nil {
		return err
	}
	for i := range l.Items {
		c := &l.Items[i]
		day, err := time.Parse(time.DateOnly, c.Labels[model.LabelRetainUntil])
		if err != nil || c.Name == h.Status.ClaimName || c.Labels[model.LabelLive] != "" ||
			!owns(c, h.Spec.Owner) || !c.DeletionTimestamp.IsZero() || r.now().Before(day.Add(24*time.Hour)) {
			continue
		}
		if err := r.Delete(ctx, c, client.Preconditions{UID: &c.UID}); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete the old claim %s: %w", c.Name, err)
		}
	}
	return nil
}

// fail leaves the home on its old claim, removes the new one and records why.
func (r *MigrationReconciler) fail(ctx context.Context, h *model.Home, evidence []string) (ctrl.Result, error) {
	m := h.Status.Migration
	if err := r.discard(ctx, h, false); err != nil {
		return ctrl.Result{}, err
	}
	var lines []string
	for _, e := range evidence {
		for _, l := range strings.Split(e, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
	}
	lines = lines[:min(len(lines), evidenceLines)]
	h.Status.Migration = &model.HomeMigration{Phase: model.MigrationFailed, Job: m.Job, Attempt: m.Attempt, Evidence: lines}

	reason, _ := reasons.Lookup(reasons.MigrationFailed)
	msg := reason.What
	if len(lines) > 0 {
		msg += " (" + strings.Join(lines[:min(len(lines), messageLines)], "; ") + ")"
	}
	setMigrating(h, metav1.ConditionFalse, string(reasons.MigrationFailed), msg)
	return ctrl.Result{}, nil
}

// cancel undoes a move that is under way or failed because it is no longer
// wanted: the schedule was removed or the mode was changed back.
func (r *MigrationReconciler) cancel(ctx context.Context, h *model.Home) error {
	if h.Status.Migration == nil || h.Status.Migration.Phase == model.MigrationSwitched {
		return nil
	}
	if err := r.discard(ctx, h, true); err != nil {
		return err
	}
	h.Status.Migration = nil
	meta.RemoveStatusCondition(&h.Status.Conditions, model.ConditionMigrating)
	return nil
}

// discard removes what a move made: the new claim, its prepare Job and, with
// withCopy, the copy Job (a failed one is kept for its logs until its TTL).
// Everything is checked for being this home's before it is deleted.
func (r *MigrationReconciler) discard(ctx context.Context, h *model.Home, withCopy bool) error {
	m := h.Status.Migration
	if m.NewClaim == "" {
		return nil
	}
	names := []string{prepareJobNameFor(m.NewClaim)}
	if withCopy {
		names = append(names, copyJobName(m.NewClaim, m.Attempt))
	}
	for _, name := range names {
		j := &batchv1.Job{}
		switch err := r.Reader.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: name}, j); {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return err
		case j.Labels[model.LabelHome] != h.ID:
			continue
		}
		if err := r.Delete(ctx, j, client.Preconditions{UID: &j.UID}, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete job %s: %w", name, err)
		}
	}
	claim := &corev1.PersistentVolumeClaim{}
	switch err := r.Reader.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: m.NewClaim}, claim); {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return err
	case m.NewClaim == h.Status.ClaimName || !owns(claim, h.Spec.Owner) || !claim.DeletionTimestamp.IsZero():
		return nil
	}
	if err := r.Delete(ctx, claim, client.Preconditions{UID: &claim.UID}); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete new claim %s: %w", m.NewClaim, err)
	}
	return nil
}

// setMigrating records the Migrating condition. While it is True the
// workspace reconciler holds the owner's apps.
func setMigrating(h *model.Home, status metav1.ConditionStatus, reason, msg string) {
	if status == metav1.ConditionTrue {
		r, _ := reasons.Lookup(reasons.HomeMigrating)
		msg = r.What + " " + msg
	}
	meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
		Type: model.ConditionMigrating, Status: status, Reason: reason, Message: msg,
	})
}

// SetupWithManager reconciles homes, and every home when the findings (a
// mode choice) or the admin's requests (a schedule, an attempt) change. Its
// Jobs and the owner's pods are looked at again while a move runs.
func (r *MigrationReconciler) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	if r.Store == nil || r.Reader == nil || r.UIDs == nil || len(r.Backends) == 0 || r.Image.Ref == "" || r.Storage.Folders == nil {
		return errors.New("storage.MigrationReconciler needs Store, Reader, UIDs, Backends, Image and Storage")
	}
	all := func(ctx context.Context) ([]reconcile.Request, error) { return r.homeRequests(ctx, nil) }
	everyHome := func(ctx context.Context, _ string) ([]reconcile.Request, error) { return r.homeRequests(ctx, nil) }
	return ctrl.NewControllerManagedBy(mgr).Named("storage-migration").
		WatchesRawSource(dbsource.For(feed.Subscribe(), store.TableHomes, 0, all)).
		WatchesRawSource(dbsource.Fan(feed.Subscribe(), store.TableStorageStatus, everyHome)).
		WatchesRawSource(dbsource.Fan(feed.Subscribe(), store.TableStorageSettings, everyHome)).
		Complete(r)
}

package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
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

	"github.com/nebari-dev/javo/internal/backup"
	"github.com/nebari-dev/javo/internal/backup/restic"
	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/loop/managed"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
)

// BackupSecretName is the Secret, in each namespace with homes, that holds
// the bucket's access key and that namespace's own repository password
// (backup.NamespacePassword) for the backup and restore Jobs there. The
// controller writes it from its own mounted copy; the backup password
// itself stays in the release namespace.
const BackupSecretName = model.ProductName + "-backup"

// Keys of BackupSecretName.
const (
	secretAccessKeyID     = "AWS_ACCESS_KEY_ID"
	secretSecretAccessKey = "AWS_SECRET_ACCESS_KEY"
	secretPassword        = "RESTIC_PASSWORD"
	secretCACert          = "ca.crt"
)

const (
	// backupJobTTL keeps a finished Job (and its pod's log) for a day.
	backupJobTTL = 24 * 60 * 60
	// backupDeadline fails a backup or restore that has not ended in six hours.
	backupDeadline = 6 * 60 * 60
	// backupPoll is how often a running Job is looked at.
	backupPoll = 10 * time.Second
	// backupBusyRetry is how soon a home that cannot be backed up now (no
	// claim yet, being moved) is looked at again.
	backupBusyRetry = 5 * time.Minute
	// caDir is where the Jobs see the bucket's CA certificate.
	caDir = "/etc/" + model.ProductName + "-backup/ca"
)

// HomeBackups is the bucket the homes are backed up to, when and what is kept.
type HomeBackups struct {
	Target restic.Target
	// CACert is the content of Target.CACertFile, given to the Jobs.
	CACert []byte
	Keep   restic.Keep
	// Rekey moves the repository repo of namespace ns to the namespace's
	// password (backup.Rekey) before a Job opens it with that password.
	Rekey func(ctx context.Context, ns, repo string) error
}

// BackupReconciler backs every prepared home up to the bucket on schedule
// with a restic Job that reads it as its owner (apps keep running), and
// brings a home back from a backup when an admin asks (Spec.Restore): the
// owner's apps are held (Restoring=True) until the files are back.
type BackupReconciler struct {
	client.Client
	Rows
	// Reader reads Jobs, Pods, claims and Secrets without the manager's cache.
	Reader  client.Reader
	UIDs    ids.UserIDs
	Storage podstorage.Resolver
	// Image is the javo image, which carries restic (`javo backup job`).
	Image   podspec.Image
	Backups *HomeBackups
	Now     func() time.Time
	// People says who a home's owner is, for the backup's log, and whether
	// they have an account at all; nil means everyone does.
	People PersonLabels
}

// PersonLabels says how a person reads (model.Person.Label) and whether
// there is such a person.
type PersonLabels interface {
	PersonLabel(ctx context.Context, owner string) (label string, account bool, err error)
}

// PeopleLabels is PersonLabels from the people table.
type PeopleLabels struct{ People *store.People }

// PersonLabel reads the person called owner.
func (p PeopleLabels) PersonLabel(ctx context.Context, owner string) (string, bool, error) {
	person, err := p.People.ByName(ctx, owner)
	if errors.Is(err, store.ErrNotFound) {
		return "", false, nil
	} else if err != nil {
		return "", false, err
	}
	return person.Label(), true, nil
}

// noAccount is how a home volume whose owner has no account reads in a log.
const noAccount = "no account"

// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update

func (r *BackupReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile moves one home's restore along when one is asked for, and
// otherwise its backups.
func (r *BackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	got, err := r.Store.Homes().Get(ctx, req.Name)
	if errors.Is(err, store.ErrNotFound) {
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	h, base := &got, got.DeepCopy()
	var res ctrl.Result
	if restoreWanted(h) {
		res, err = r.restore(ctx, h)
	} else {
		res, err = r.backup(ctx, h)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if conflict, err := r.WriteHome(ctx, h, base); conflict {
		return ctrl.Result{RequeueAfter: retryConflict}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	return res, nil
}

// restoreWanted is true while the restore the spec asks for has not ended.
func restoreWanted(h *model.Home) bool {
	req, st := h.Spec.Restore, h.Status.Restore
	return req != nil && (st == nil || st.ID != req.ID || !st.Ended())
}

// backup starts the home's backup when a full backup run asks for it
// (Spec.Backup) and records the outcome of the one running.
func (r *BackupReconciler) backup(ctx context.Context, h *model.Home) (ctrl.Result, error) {
	b := h.Status.Backup
	if b != nil && b.Job != "" {
		done, err := r.collectBackup(ctx, h)
		if err != nil || !done {
			return ctrl.Result{RequeueAfter: backupPoll}, err
		}
	}
	req := h.Spec.Backup
	if req == nil || (b != nil && b.Run == req.Run) {
		return ctrl.Result{}, nil
	}
	label, account, err := r.personLabel(ctx, h.Spec.Owner)
	if err != nil {
		return ctrl.Result{}, err
	}
	// A volume without an account whose claim the home controller refused
	// never gets one: the step says so instead of holding the run up.
	if c := meta.FindStatusCondition(h.Status.Conditions, model.ConditionReady); !account && h.Status.ClaimName == "" &&
		c != nil && c.Reason == string(reasons.HomeClaimForeign) {
		failBackup(h, req.Run, r.now(), "the home folder storage of "+h.Spec.Owner+" is labelled as another account's: "+c.Message)
		return ctrl.Result{}, nil
	}
	// A volume without an account is never prepared: it is read as it is.
	if h.Status.ClaimName == "" || (account && !h.Status.Prepared) || meta.IsStatusConditionTrue(h.Status.Conditions, model.ConditionMigrating) {
		return ctrl.Result{RequeueAfter: backupBusyRetry}, nil
	}
	if !account {
		// Its volume may be gone: then there is nothing to back up, and the
		// step says so instead of holding the run up.
		pvc := &corev1.PersistentVolumeClaim{}
		err := r.Reader.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: h.Status.ClaimName}, pvc)
		if apierrors.IsNotFound(err) || (err == nil && !pvc.DeletionTimestamp.IsZero()) {
			failBackup(h, req.Run, r.now(), "volume "+h.Status.ClaimName+" is gone; javo home prune --orphans forgets this home")
			return ctrl.Result{}, nil
		} else if err != nil {
			return ctrl.Result{}, err
		}
	}
	if !account && h.Status.VolumeUID == 0 {
		if res, err := r.probeOwner(ctx, h); err != nil || h.Status.VolumeUID == 0 {
			return res, err
		}
	}
	now := r.now()
	v, ok, err := r.homeVolume(ctx, h, account)
	if err != nil || !ok {
		return ctrl.Result{RequeueAfter: backupBusyRetry}, err
	}
	v.fields = []string{"step", "home", "person", label, "home", h.ID, "claim", v.claim}
	name := naming.NamespaceName(model.ProductName+"-backup", h.Spec.Owner+"-"+now.UTC().Format("200601021504"))
	if err := r.startJob(ctx, v, name, model.RoleHomeBackup, true, r.backupArgs(v, req.Run)); err != nil {
		return ctrl.Result{}, err
	}
	if h.Status.Backup == nil {
		h.Status.Backup = &model.HomeBackup{}
	}
	at := metav1.NewTime(now)
	h.Status.Backup.Run, h.Status.Backup.Job, h.Status.Backup.StartedAt = req.Run, name, &at
	return ctrl.Result{RequeueAfter: backupPoll}, nil
}

// failBackup ends the home's step of the backup run run as failed, with why.
func failBackup(h *model.Home, run string, now time.Time, why string) {
	at := metav1.NewTime(now)
	if h.Status.Backup == nil {
		h.Status.Backup = &model.HomeBackup{}
	}
	h.Status.Backup.Run, h.Status.Backup.Job, h.Status.Backup.Reason, h.Status.Backup.FailedAt = run, "", string(reasons.HomeBackupFailed), &at
	h.Status.Backup.Evidence = []string{why}
}

// personLabel is how the home's owner reads in the log ("no account" for a
// volume found without one) and whether they have an account.
func (r *BackupReconciler) personLabel(ctx context.Context, owner string) (string, bool, error) {
	if r.People == nil {
		return owner, true, nil
	}
	label, ok, err := r.People.PersonLabel(ctx, owner)
	if !ok {
		label = noAccount
	}
	return label, ok, err
}

// probeOwner finds who owns the files of a home volume without an account:
// a Job reads the owner of its root (as nobody, read-only), and the backups
// then run as that uid.
func (r *BackupReconciler) probeOwner(ctx context.Context, h *model.Home) (ctrl.Result, error) {
	v := volume{ns: h.Namespace, claim: h.Status.ClaimName, idLabel: model.LabelHome, id: h.ID, owner: h.Spec.Owner,
		uid: nobody, gid: nobody, mountPath: podstorage.HomeDir, dir: podstorage.HomeDir}
	name := naming.NamespaceName(model.ProductName+"-owner", h.Spec.Owner)
	res, ended, err := r.jobOutcome(ctx, v, name, model.RoleHomeOwnerProbe)
	switch {
	case err != nil:
		return ctrl.Result{}, err
	case !ended:
		return ctrl.Result{RequeueAfter: backupPoll}, nil
	case res.UID > 0:
		h.Status.VolumeUID, h.Status.VolumeGID = res.UID, res.GID
		return ctrl.Result{}, nil
	}
	j := &batchv1.Job{}
	err = r.Reader.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: name}, j)
	if apierrors.IsNotFound(err) {
		if err := r.startJob(ctx, v, name, model.RoleHomeOwnerProbe, true,
			[]string{"backup", "job", "owner", "--dir", v.dir}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: backupPoll}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	// It ended without an owner (the files are root's, or the Job failed):
	// the backup fails with the reason, and the next run tries again.
	if h.Status.Backup == nil {
		h.Status.Backup = &model.HomeBackup{}
	}
	at := metav1.NewTime(r.now())
	reason := res.Reason
	if reason == "" {
		reason = string(reasons.HomeBackupFailed)
	}
	if h.Spec.Backup != nil {
		h.Status.Backup.Run = h.Spec.Backup.Run
	}
	h.Status.Backup.Reason, h.Status.Backup.FailedAt = reason, &at
	h.Status.Backup.Evidence = append([]string{"could not tell who owns the volume's files"}, res.Evidence...)
	_ = r.Delete(ctx, j, client.PropagationPolicy(metav1.DeletePropagationBackground))
	return ctrl.Result{}, nil
}

// nobody is the uid and gid the owner probe runs as.
const nobody = 65534

// collectBackup records the outcome of the home's running backup Job.
func (r *BackupReconciler) collectBackup(ctx context.Context, h *model.Home) (bool, error) {
	return r.collect(ctx, volume{ns: h.Namespace, idLabel: model.LabelHome, id: h.ID}, h.Status.Backup, model.RoleHomeBackup)
}

// collect records in b the outcome of the volume's running backup Job once
// it has ended; it returns false while the Job runs.
func (r *BackupReconciler) collect(ctx context.Context, v volume, b *model.HomeBackup, role string) (bool, error) {
	res, ended, err := r.jobOutcome(ctx, v, b.Job, role)
	if err != nil || !ended {
		return false, err
	}
	b.LastJob, b.Job = b.Job, ""
	if res.Snapshot != "" {
		b.LastAt, b.Snapshot, b.Files, b.Bytes, b.Uploaded, b.Took = b.StartedAt, res.Snapshot, res.Files, res.Bytes, res.Uploaded, res.Took
	}
	if res.Reason == "" {
		b.Reason, b.FailedAt, b.Evidence = "", nil, nil
		return true, nil
	}
	at := metav1.NewTime(r.now())
	b.Reason, b.FailedAt, b.Evidence = res.Reason, &at, res.Evidence
	return true, nil
}

// restore moves the home's restore one step: hold the apps and wait until
// no pod writes to the home, run the restore Job, then let the apps go.
func (r *BackupReconciler) restore(ctx context.Context, h *model.Home) (ctrl.Result, error) {
	req := h.Spec.Restore
	if h.Status.Restore == nil || h.Status.Restore.ID != req.ID {
		h.Status.Restore = &model.HomeRestore{ID: req.ID, Phase: model.RestoreWaiting}
	}
	st := h.Status.Restore
	if st.Phase == model.RestoreRestoring {
		return r.restoring(ctx, h)
	}
	setRestoring(h, metav1.ConditionTrue, reasons.HomeRestoreWaiting)
	// A backup still running finishes first: it holds the repository.
	if h.Status.Backup != nil && h.Status.Backup.Job != "" {
		if done, err := r.collectBackup(ctx, h); err != nil || !done {
			return ctrl.Result{RequeueAfter: backupPoll}, err
		}
	}
	label, account, err := r.personLabel(ctx, h.Spec.Owner)
	if err != nil {
		return ctrl.Result{}, err
	}
	// A volume without an account is never prepared: it is written as it is.
	if h.Status.ClaimName == "" || (account && !h.Status.Prepared) {
		// The home reconciler is giving the home its claim.
		return ctrl.Result{RequeueAfter: homeWaitRetry}, nil
	}
	err = r.Reader.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: h.Status.ClaimName}, &corev1.PersistentVolumeClaim{})
	switch {
	case apierrors.IsNotFound(err):
		// Its claim is gone (a new cluster after a disaster): the home
		// reconciler makes and readies a new one, which the files go into.
		h.Status.ClaimName, h.Status.Prepared, h.Status.PrepareAttempts = "", false, 0
		return ctrl.Result{RequeueAfter: homeWaitRetry}, nil
	case err != nil:
		return ctrl.Result{}, err
	}
	pods, err := r.writers(ctx, h)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(pods) > 0 {
		return ctrl.Result{RequeueAfter: homeWaitRetry}, nil
	}
	if !account && h.Status.VolumeUID == 0 {
		if res, err := r.probeOwner(ctx, h); err != nil || h.Status.VolumeUID == 0 {
			return res, err
		}
	}
	name := naming.NamespaceName(model.ProductName+"-restore", h.Spec.Owner+"-"+req.ID)
	v, ok, err := r.homeVolume(ctx, h, account)
	if err != nil || !ok {
		return ctrl.Result{RequeueAfter: homeWaitRetry}, err
	}
	args := r.jobArgs(v, "restore")
	for _, f := range [][2]string{{"step", "restore"}, {"person", label}, {"home", h.ID}, {"claim", v.claim}} {
		args = append(args, "--field", f[0]+"="+f[1])
	}
	args = append(args, "--snapshot", req.Snapshot, "--mode", req.Mode)
	if req.Mode == model.RestoreBeside {
		args = append(args, "--folder", "restored-"+req.SnapshotTime.UTC().Format(time.DateOnly))
	}
	if err := r.startJob(ctx, v, name, model.RoleHomeRestore, false, args); err != nil {
		return ctrl.Result{}, err
	}
	st.Phase, st.Job = model.RestoreRestoring, name
	setRestoring(h, metav1.ConditionTrue, reasons.HomeRestoreRunning)
	return ctrl.Result{RequeueAfter: backupPoll}, nil
}

// restoring records the end of the restore Job and lets the apps go.
func (r *BackupReconciler) restoring(ctx context.Context, h *model.Home) (ctrl.Result, error) {
	st := h.Status.Restore
	setRestoring(h, metav1.ConditionTrue, reasons.HomeRestoreRunning)
	res, ended, err := r.jobOutcome(ctx, volume{ns: h.Namespace, idLabel: model.LabelHome, id: h.ID}, st.Job, model.RoleHomeRestore)
	if err != nil || !ended {
		return ctrl.Result{RequeueAfter: backupPoll}, err
	}
	at := metav1.NewTime(r.now())
	st.FinishedAt = &at
	if res.Reason != "" {
		st.Phase, st.Reason, st.Evidence = model.RestoreFailed, res.Reason, res.Evidence
		setRestoring(h, metav1.ConditionFalse, reasons.HomeRestoreFailed)
		return ctrl.Result{}, nil
	}
	st.Phase = model.RestoreDone
	setRestoring(h, metav1.ConditionFalse, reasons.HomeRestored)
	return ctrl.Result{}, nil
}

// setRestoring records the Restoring condition. While it is True the
// workspace reconciler holds the owner's apps.
func setRestoring(h *model.Home, status metav1.ConditionStatus, code reasons.Code) {
	r, _ := reasons.Lookup(code)
	meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
		Type: model.ConditionRestoring, Status: status, Reason: string(code), Message: r.What,
	})
}

// writers are the owner's pods that may write to the home: apps, job runs
// and the file picker's pod (the editor saves through it; the gateway lets
// it go while the home is Restoring). The storage Jobs are this controller's, and a
// run of a job from the backup template only follows the restore (it would
// otherwise wait for itself).
func (r *BackupReconciler) writers(ctx context.Context, h *model.Home) ([]string, error) {
	var pods corev1.PodList
	if err := r.Reader.List(ctx, &pods, client.InNamespace(h.Namespace), client.MatchingLabels{model.LabelOwner: h.Spec.Owner}); err != nil {
		return nil, fmt.Errorf("list the owner's pods: %w", err)
	}
	var names []string
	for _, p := range pods.Items {
		finished := p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
		switch p.Labels[model.LabelRole] {
		case model.RoleHomePrepare, model.RoleHomeMigrate, model.RoleHomeBackup, model.RoleHomeRestore:
			continue
		}
		if finished {
			continue
		}
		if id := p.Labels[model.LabelJob]; id != "" {
			j, err := r.Store.Jobs().Get(ctx, id)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			if err == nil && j.Spec.Template == model.BackupTemplate {
				continue
			}
		}
		names = append(names, p.Name)
	}
	slices.Sort(names)
	return names, nil
}

// jobOutcome reads the home's Job name: whether it has ended and what its
// container reported. A Job that is gone, or not this home's, has ended
// without a result.
func (r *BackupReconciler) jobOutcome(ctx context.Context, v volume, name, role string) (backup.JobResult, bool, error) {
	j := &batchv1.Job{}
	err := r.Reader.Get(ctx, client.ObjectKey{Namespace: v.ns, Name: name}, j)
	switch {
	case apierrors.IsNotFound(err):
		return lost("job " + name + " is gone without a result"), true, nil
	case err != nil:
		return backup.JobResult{}, false, err
	case j.Labels[v.idLabel] != v.id || j.Labels[model.LabelRole] != role:
		return lost("job " + name + " is not this volume's"), true, nil
	}
	complete, failed := jobHas(j, batchv1.JobComplete), jobHas(j, batchv1.JobFailed)
	if !complete && !failed {
		return backup.JobResult{}, false, nil
	}
	var pods corev1.PodList
	if err := r.Reader.List(ctx, &pods, client.InNamespace(v.ns), client.MatchingLabels{batchv1.JobNameLabel: name}); err != nil {
		return backup.JobResult{}, false, err
	}
	for _, p := range pods.Items {
		for _, cs := range p.Status.ContainerStatuses {
			if t := cs.State.Terminated; t != nil {
				if res, ok := backup.ParseJobResult(t.Message); ok {
					if failed && res.Reason == "" {
						res.Reason = string(reasons.HomeBackupFailed)
					}
					return res, true, nil
				}
			}
		}
	}
	if complete {
		return lost("job " + name + " ended without a result"), true, nil
	}
	res := lost("")
	res.Evidence = jobEvidence(j, pods.Items)
	return res, true, nil
}

func lost(line string) backup.JobResult {
	r := backup.JobResult{Reason: string(reasons.HomeBackupFailed)}
	if line != "" {
		r.Evidence = []string{line}
	}
	return r
}

// jobArgs are the arguments of `javo backup job <verb>` for the volume.
func (r *BackupReconciler) jobArgs(v volume, verb string) []string {
	t := r.Backups.Target
	args := []string{"backup", "job", verb,
		"--endpoint", t.Endpoint, "--bucket", t.Bucket, "--prefix", t.Prefix, "--bucket-lookup", t.BucketLookup,
		"--repo", v.repo, "--dir", v.dir}
	if t.Region != "" {
		args = append(args, "--region", t.Region)
	}
	if t.InsecureTLS {
		args = append(args, "--insecure-tls")
	}
	if len(r.Backups.CACert) > 0 {
		args = append(args, "--cacert", caDir+"/"+secretCACert)
	}
	return args
}

// volume is what a backup or restore Job reads or writes: a home or a
// shared folder.
type volume struct {
	ns, claim string
	// idLabel and id say whose Job it is (LabelHome and the home's id).
	idLabel, id string
	// owner is the home's owner, for the owner label and co-location; empty
	// for a shared folder.
	owner    string
	coLocate bool
	uid, gid int64
	groups   []int64
	repo     string
	// mountPath is where the claim is mounted, dir the folder backed up.
	mountPath, dir string
	// fields name the step on every line of its log (key, value, ...).
	fields []string
}

// homeVolume is the home h as its owner sees it, or ok false while the
// owner has no uid yet.
func (r *BackupReconciler) homeVolume(ctx context.Context, h *model.Home, account bool) (volume, bool, error) {
	uid, gid := h.Status.VolumeUID, h.Status.VolumeGID
	if account {
		var err error
		uid, gid, err = r.UIDs.UID(ctx, h.Spec.Owner)
		if errors.Is(err, ids.ErrNoUID) {
			return volume{}, false, nil
		} else if err != nil {
			return volume{}, false, err
		}
	}
	in, err := r.Storage.Input(ctx, h.Namespace, h.Spec.Owner)
	if err != nil {
		return volume{}, false, err
	}
	return volume{
		ns: h.Namespace, claim: h.Status.ClaimName, idLabel: model.LabelHome, id: h.ID, owner: h.Spec.Owner,
		coLocate: in.CoLocate, uid: uid, gid: gid, repo: backup.HomeRepo(h.Namespace, h.Spec.Owner),
		mountPath: podstorage.HomeDir, dir: podstorage.HomeDir,
	}, true, nil
}

// folderVolume is the shared folder f as a member of its group reads it.
func folderVolume(f *model.SharedFolder) volume {
	return volume{
		ns: f.Namespace, claim: f.Status.ClaimName, idLabel: model.LabelFolder, id: f.ID,
		uid: podspec.FixedUID, gid: podspec.FixedGID, groups: []int64{f.Status.GID},
		repo: backup.FolderRepo(f.Namespace, f.Name), mountPath: folderMount, dir: folderMount + "/" + podstorage.FolderSubPath,
		fields: []string{"step", "folder", "folder", f.Name, "group", f.Spec.Group, "claim", f.Status.ClaimName},
	}
}

// folderMount is where a shared folder's claim is mounted in its Jobs.
const folderMount = "/folder"

// backupArgs are the arguments of the backup Job of v in run.
func (r *BackupReconciler) backupArgs(v volume, run string) []string {
	args := r.jobArgs(v, "backup")
	for i := 0; i+1 < len(v.fields); i += 2 {
		args = append(args, "--field", v.fields[i]+"="+v.fields[i+1])
	}
	return append(args, "--run", run,
		"--keep-daily", strconv.Itoa(r.Backups.Keep.Daily),
		"--keep-weekly", strconv.Itoa(r.Backups.Keep.Weekly),
		"--keep-monthly", strconv.Itoa(r.Backups.Keep.Monthly))
}

// startJob creates the volume's Job name unless it exists. A Job that opens
// the volume's repository gets it on the namespace's password first.
func (r *BackupReconciler) startJob(ctx context.Context, v volume, name, role string, readOnly bool, args []string) error {
	if err := r.ensureSecret(ctx, v.ns); err != nil {
		return err
	}
	if v.repo != "" {
		if err := r.Backups.Rekey(ctx, v.ns, v.repo); err != nil {
			return fmt.Errorf("move %s to the namespace's backup password: %w", v.repo, err)
		}
	}
	j := r.volumeJob(v, name, role, readOnly, args)
	if err := r.Create(ctx, j); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create %s job: %w", role, err)
	}
	return nil
}

// ensureSecret writes the namespace's copy of the bucket's key and the
// namespace's repository password.
func (r *BackupReconciler) ensureSecret(ctx context.Context, ns string) error {
	t := r.Backups.Target
	want := map[string][]byte{
		secretAccessKeyID: []byte(t.AccessKeyID), secretSecretAccessKey: []byte(t.SecretAccessKey),
		secretPassword: []byte(backup.NamespacePassword(t.Password, ns)),
	}
	if len(r.Backups.CACert) > 0 {
		want[secretCACert] = r.Backups.CACert
	}
	cur := &corev1.Secret{}
	switch err := r.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: BackupSecretName}, cur); {
	case apierrors.IsNotFound(err):
		s := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: BackupSecretName, Namespace: ns},
			Type:       corev1.SecretTypeOpaque, Data: want,
		}
		if err := r.Create(ctx, s); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create the backup secret: %w", err)
		}
		return nil
	case err != nil:
		return err
	}
	same := len(cur.Data) == len(want)
	for k, v := range want {
		same = same && bytes.Equal(cur.Data[k], v)
	}
	if same {
		return nil
	}
	cur.Data = want
	if err := r.Update(ctx, cur); err != nil {
		return fmt.Errorf("update the backup secret: %w", err)
	}
	return nil
}

// volumeJob is the Job that runs `javo backup job` as the volume's user
// (a home's owner) with the volume mounted (read-only for a backup), on the
// owner's node when a home's disk attaches to one node.
func (r *BackupReconciler) volumeJob(v volume, name, role string, readOnly bool, args []string) *batchv1.Job {
	labels := map[string]string{model.LabelRole: role, v.idLabel: v.id}
	if v.owner != "" {
		labels[model.LabelOwner] = v.owner
	}
	var affinity *corev1.Affinity
	if v.coLocate {
		affinity = &corev1.Affinity{PodAffinity: &corev1.PodAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{model.LabelOwner: v.owner}},
			TopologyKey:   corev1.LabelHostname,
		}}}}
	}
	fromSecret := func(name, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: BackupSecretName}, Key: key}}}
	}
	volumes := []corev1.Volume{
		{Name: podstorage.HomeVolume, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
			ClaimName: v.claim, ReadOnly: readOnly}}},
		{Name: podstorage.TmpVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	mounts := []corev1.VolumeMount{
		{Name: podstorage.HomeVolume, MountPath: v.mountPath, ReadOnly: readOnly},
		{Name: podstorage.TmpVolume, MountPath: "/tmp"},
	}
	if len(r.Backups.CACert) > 0 {
		volumes = append(volumes, corev1.Volume{Name: "ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: BackupSecretName, Items: []corev1.KeyToPath{{Key: secretCACert, Path: secretCACert}}}}})
		mounts = append(mounts, corev1.VolumeMount{Name: "ca", MountPath: caDir, ReadOnly: true})
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: v.ns, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(int32(0)),
			TTLSecondsAfterFinished: ptr.To(int32(backupJobTTL)),
			ActiveDeadlineSeconds:   ptr.To(int64(backupDeadline)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: ptr.To(false),
					EnableServiceLinks:           ptr.To(false),
					Affinity:                     affinity,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:       ptr.To(true),
						RunAsUser:          ptr.To(v.uid),
						RunAsGroup:         ptr.To(v.gid),
						SupplementalGroups: v.groups,
						SeccompProfile:     &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:  "restic",
						Image: r.Image.Ref,
						Args:  args,
						Env: []corev1.EnvVar{
							fromSecret("AWS_ACCESS_KEY_ID", secretAccessKeyID),
							fromSecret("AWS_SECRET_ACCESS_KEY", secretSecretAccessKey),
							fromSecret("RESTIC_PASSWORD", secretPassword),
							// The node goes on every log line; restic reports progress each second.
							{Name: "NODE_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"}}},
							{Name: "RESTIC_PROGRESS_FPS", Value: "1"},
						},
						VolumeMounts: mounts,
						// Only what the step wrote counts as its result, never its
						// log tail (which may carry file names from the home).
						TerminationMessagePolicy: corev1.TerminationMessageReadFile,
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							ReadOnlyRootFilesystem:   ptr.To(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
					Volumes: volumes,
				},
			},
		},
	}
	r.Image.Pull(&job.Spec.Template.Spec)
	return job
}

// SetupWithManager reconciles every home when it changes (a run or a
// restore asked for), and on the requeues a running Job asks for.
func (r *BackupReconciler) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	if r.Store == nil || r.Reader == nil || r.UIDs == nil || r.Image.Ref == "" || r.Backups == nil || r.Backups.Rekey == nil || r.Storage.Homes == nil {
		return errors.New("storage.BackupReconciler needs Store, Reader, UIDs, Image, Backups (with Rekey) and Storage")
	}
	all := func(ctx context.Context) ([]string, error) { return r.HomeIDs(ctx, nil) }
	return ctrl.NewControllerManagedBy(mgr).Named("storage-backup").
		WatchesRawSource(managed.Source(dbsource.For(feed.Subscribe(), store.TableHomes, 0, all))).
		Complete(r)
}

// FolderBackupReconciler backs every ready shared folder up as part of the
// full backup run its spec asks for, like the homes: a restic Job that reads
// the folder as a member of its group, never as root.
type FolderBackupReconciler struct {
	BackupReconciler
}

// Reconcile moves one shared folder's backup along.
func (r *FolderBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	f, err := r.Store.SharedFolders().Get(ctx, req.Name)
	if errors.Is(err, store.ErrNotFound) {
		return ctrl.Result{}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	version := f.Version
	v := folderVolume(&f)
	res, changed, err := r.step(ctx, &f, v)
	if err != nil || !changed {
		return res, err
	}
	if _, err := r.Store.SharedFolders().Update(ctx, f, version); errors.Is(err, store.ErrConflict) {
		return ctrl.Result{RequeueAfter: retryConflict}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	return res, nil
}

func (r *FolderBackupReconciler) step(ctx context.Context, f *model.SharedFolder, v volume) (ctrl.Result, bool, error) {
	b := f.Status.Backup
	if b != nil && b.Job != "" {
		done, err := r.collect(ctx, v, b, model.RoleFolderBackup)
		if err != nil || !done {
			return ctrl.Result{RequeueAfter: backupPoll}, false, err
		}
		return ctrl.Result{}, true, nil
	}
	req := f.Spec.Backup
	if req == nil || (b != nil && b.Run == req.Run) {
		return ctrl.Result{}, false, nil
	}
	if f.Status.ClaimName == "" || !f.Status.Prepared || f.Status.GID == 0 {
		return ctrl.Result{RequeueAfter: backupBusyRetry}, false, nil
	}
	now := r.now()
	name := naming.NamespaceName(model.ProductName+"-backup-folder", f.Name+"-"+now.UTC().Format("200601021504"))
	if err := r.startJob(ctx, v, name, model.RoleFolderBackup, true, r.backupArgs(v, req.Run)); err != nil {
		return ctrl.Result{}, false, err
	}
	if f.Status.Backup == nil {
		f.Status.Backup = &model.HomeBackup{}
	}
	at := metav1.NewTime(now)
	f.Status.Backup.Run, f.Status.Backup.Job, f.Status.Backup.StartedAt = req.Run, name, &at
	return ctrl.Result{RequeueAfter: backupPoll}, true, nil
}

// SetupWithManager reconciles every shared folder when it changes.
func (r *FolderBackupReconciler) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	if r.Store == nil || r.Reader == nil || r.Image.Ref == "" || r.Backups == nil || r.Backups.Rekey == nil {
		return errors.New("storage.FolderBackupReconciler needs Store, Reader, Image and Backups (with Rekey)")
	}
	all := func(ctx context.Context) ([]string, error) {
		fs, err := r.Store.SharedFolders().List(ctx)
		return dbsource.IDs(fs, func(f *model.SharedFolder) string { return f.ID }, nil), err
	}
	return ctrl.NewControllerManagedBy(mgr).Named("storage-folder-backup").
		WatchesRawSource(managed.Source(dbsource.For(feed.Subscribe(), store.TableSharedFolders, 0, all))).
		Complete(r)
}

// VolumeFinder finds the home volumes javo made that have no home row (from
// before the database, or a row lost since) and gives each one, so a full
// backup holds every home volume and the Backups page lists it. A volume
// found while a full backup runs joins that run.
type VolumeFinder struct {
	Reader client.Reader
	Store  *store.Store
	// Namespaces are where homes live; empty means every namespace.
	Namespaces []string
	// Every is how often it looks besides on every change of the homes;
	// zero means every five minutes.
	Every time.Duration
	Now   func() time.Time
}

// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch

// runUnderWay is how long after a full backup run was asked for a volume
// found then still joins it.
const runUnderWay = time.Hour

// AdoptHomes gives a home row to every claim named like a home of the owner
// it is labelled with, except claims of a removed person
// (model.LabelForgotten), records each in the audit log and returns how many
// it made.
func (f VolumeFinder) AdoptHomes(ctx context.Context) (int, error) {
	now := time.Now
	if f.Now != nil {
		now = f.Now
	}
	homes, err := f.Store.Homes().List(ctx)
	if err != nil {
		return 0, err
	}
	var current *model.BackupRequest
	for i := range homes {
		if r := homes[i].Spec.Backup; r != nil && (current == nil || r.At.After(current.At.Time)) {
			current = r
		}
	}
	if current != nil && now().Sub(current.At.Time) > runUnderWay {
		current = nil
	}
	nss := f.Namespaces
	if len(nss) == 0 {
		nss = []string{""}
	}
	n := 0
	for _, ns := range nss {
		var l corev1.PersistentVolumeClaimList
		opts := []client.ListOption{client.HasLabels{model.LabelOwner}}
		if ns != "" {
			opts = append(opts, client.InNamespace(ns))
		}
		if err := f.Reader.List(ctx, &l, opts...); err != nil {
			return n, fmt.Errorf("list the home volumes: %w", err)
		}
		for i := range l.Items {
			c := &l.Items[i]
			owner := c.Labels[model.LabelOwner]
			if naming.HomeClaimGen(owner, c.Name) < 1 || !c.DeletionTimestamp.IsZero() || c.Labels[model.LabelForgotten] == "true" {
				continue
			}
			switch _, err := f.Store.Homes().ByOwner(ctx, c.Namespace, owner); {
			case err == nil:
				continue
			case !errors.Is(err, store.ErrNotFound):
				return n, err
			}
			h, err := f.Store.Homes().Ensure(ctx, c.Namespace, owner, nil)
			if err != nil {
				return n, fmt.Errorf("a home row for volume %s: %w", c.Name, err)
			}
			n++
			detail, _ := json.Marshal(map[string]string{"claim": c.Name})
			if err := f.Store.Audit().Append(ctx, store.AuditEvent{
				Actor: model.SystemController, Via: "system", Action: "home.adopt", ObjectKind: "home",
				ObjectName: owner, ObjectID: h.ID, ObjectNamespace: c.Namespace, ObjectOwner: owner, Allowed: true, Detail: detail,
			}); err != nil {
				return n, fmt.Errorf("audit the home row for volume %s: %w", c.Name, err)
			}
			if current != nil {
				h.Spec.Backup = current
				if _, err := f.Store.Homes().Update(ctx, h, h.Version); err != nil && !errors.Is(err, store.ErrConflict) {
					return n, err
				}
			}
		}
	}
	return n, nil
}

// Reconcile looks for volumes without a row; any request does.
func (f VolumeFinder) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	every := f.Every
	if every == 0 {
		every = 5 * time.Minute
	}
	_, err := f.AdoptHomes(ctx)
	return ctrl.Result{RequeueAfter: every}, err
}

// SetupWithManager looks on every change of the homes (a full backup asked
// for) and every Every.
func (f VolumeFinder) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	if f.Reader == nil || f.Store == nil {
		return errors.New("storage.VolumeFinder needs Reader and Store")
	}
	one := func(context.Context) ([]string, error) { return singletonID, nil }
	return ctrl.NewControllerManagedBy(mgr).Named("storage-volume-finder").
		WatchesRawSource(managed.Source(dbsource.For(feed.Subscribe(), store.TableHomes, 0, one))).
		Complete(f)
}

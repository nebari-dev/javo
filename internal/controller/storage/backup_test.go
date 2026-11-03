package storage

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/backup"
	"github.com/nebari-dev/javo/internal/backup/restic"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
)

func backupReconciler(t *testing.T) *BackupReconciler {
	t.Helper()
	e := env(t)
	r := &BackupReconciler{
		Client: e.k8s, Reader: e.k8s, Rows: testRows(t), Image: testImage, UIDs: fixedUIDs(t),
		Storage: podstorage.Resolver{Homes: e.db.Homes(), Folders: e.db.SharedFolders()},
		Backups: &HomeBackups{
			Target: restic.Target{
				Endpoint: "https://s3.example.com", Bucket: "backups", Prefix: "javo", BucketLookup: "path",
				AccessKeyID: "id", SecretAccessKey: "key", Password: "pw",
			},
			Keep: restic.Keep{Daily: 7, Weekly: 4, Monthly: 6},
		},
		Now: func() time.Time { return e.backupClock },
	}
	return r
}

func reconcileBackup(t *testing.T, r *BackupReconciler, owner string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: getHome(t, owner).ID}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// readyHome is a bound, prepared perUser home.
func readyHome(t *testing.T, owner string) {
	t.Helper()
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 1})
	newHome(t, owner, "10Gi")
	settled(t, homeReconciler(t, env(t).k8s), owner)
}

func jobsOf(t *testing.T, owner, role string) []batchv1.Job {
	t.Helper()
	var l batchv1.JobList
	must(t, env(t).k8s.List(ctx, &l, client.InNamespace(homeNS), client.MatchingLabels{model.LabelRole: role, model.LabelOwner: owner}))
	return l.Items
}

// finishWith ends the Job as finishJob does, its container reporting r.
func finishWith(t *testing.T, j batchv1.Job, ok bool, r backup.JobResult) {
	t.Helper()
	finishJob(t, j, ok, r.Message())
}

// askRun asks every home to be backed up as part of run.
func askRun(t *testing.T, run string) {
	t.Helper()
	if _, _, err := env(t).db.Homes().RequestBackupRun(ctx, model.Actor{Person: "admin"}, run, env(t).backupClock); err != nil {
		t.Fatal(err)
	}
}

func TestNothingIsBackedUpUntilARunAsks(t *testing.T) {
	t.Parallel()
	owner := uniqueOwner("ann")
	readyHome(t, owner)
	r := backupReconciler(t)
	reconcileBackup(t, r, owner)
	if b := getHome(t, owner).Status.Backup; b != nil {
		t.Fatalf("backup state = %+v", b)
	}
	if n := len(jobsOf(t, owner, model.RoleHomeBackup)); n != 0 {
		t.Fatalf("%d backup jobs before a run asked", n)
	}
}

func TestARunBacksTheHomeUpAsItsOwnerReadOnly(t *testing.T) {
	t.Parallel()
	owner := uniqueOwner("ben")
	readyHome(t, owner)
	askRun(t, "run-1")
	// An app of the owner keeps running: a backup only reads.
	appPod(t, owner, owner+"-lab")
	r := backupReconciler(t)
	reconcileBackup(t, r, owner)

	jobs := jobsOf(t, owner, model.RoleHomeBackup)
	if len(jobs) != 1 {
		t.Fatalf("%d backup jobs, want 1", len(jobs))
	}
	j := jobs[0]
	if j.Labels[model.LabelHome] != getHome(t, owner).ID {
		t.Errorf("labels = %v", j.Labels)
	}
	pod := j.Spec.Template.Spec
	if pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser == 0 || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot {
		t.Errorf("pod security = %+v", pod.SecurityContext)
	}
	c := pod.Containers[0]
	if c.Image != testImage.Ref || !pullsTestImage(pod) {
		t.Errorf("image = %s, pull secrets %v", c.Image, pod.ImagePullSecrets)
	}
	args := strings.Join(c.Args, " ")
	for _, want := range []string{"backup job backup", "--repo homes/" + homeNS + "/" + owner, "--dir " + podstorage.HomeDir,
		"--endpoint https://s3.example.com", "--bucket backups", "--bucket-lookup path", "--run run-1", "--keep-daily 7", "--keep-weekly 4", "--keep-monthly 6"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	home := slices.IndexFunc(c.VolumeMounts, func(m corev1.VolumeMount) bool { return m.MountPath == podstorage.HomeDir })
	if home < 0 || !c.VolumeMounts[home].ReadOnly {
		t.Errorf("home mount = %+v", c.VolumeMounts)
	}
	for _, e := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "RESTIC_PASSWORD"} {
		i := slices.IndexFunc(c.Env, func(v corev1.EnvVar) bool { return v.Name == e })
		if i < 0 || c.Env[i].ValueFrom == nil || c.Env[i].ValueFrom.SecretKeyRef == nil || c.Env[i].Value != "" {
			t.Errorf("%s is not read from a Secret: %+v", e, c.Env)
		}
	}
	// The values are in the namespace's Secret, never in the Job.
	var s corev1.Secret
	must(t, env(t).k8s.Get(ctx, types.NamespacedName{Namespace: homeNS, Name: BackupSecretName}, &s))
	if string(s.Data["RESTIC_PASSWORD"]) != "pw" || string(s.Data["AWS_SECRET_ACCESS_KEY"]) != "key" {
		t.Errorf("secret keys = %v", slices.Collect(func(yield func(string) bool) {
			for k := range s.Data {
				if !yield(k) {
					return
				}
			}
		}))
	}
	b := getHome(t, owner).Status.Backup
	if b.Job != j.Name || b.StartedAt == nil || b.Run != "run-1" {
		t.Fatalf("backup state = %+v", b)
	}
	// A second pass while it runs makes no second Job.
	reconcileBackup(t, r, owner)
	if n := len(jobsOf(t, owner, model.RoleHomeBackup)); n != 1 {
		t.Fatalf("%d backup jobs after a second pass", n)
	}
}

func TestABackupsOutcomeIsRecorded(t *testing.T) {
	t.Parallel()
	owner := uniqueOwner("cy")
	readyHome(t, owner)
	askRun(t, "run-1")
	r := backupReconciler(t)
	reconcileBackup(t, r, owner)
	finishWith(t, jobsOf(t, owner, model.RoleHomeBackup)[0], true, backup.JobResult{Snapshot: "abc123", Files: 12, Bytes: 4096})
	reconcileBackup(t, r, owner)
	b := getHome(t, owner).Status.Backup
	if b.Job != "" || b.LastJob == "" || b.Snapshot != "abc123" || b.Files != 12 || b.LastAt == nil || b.Reason != "" {
		t.Fatalf("after a good backup: %+v", b)
	}
	// The run is over for this home: no second Job for the same run.
	reconcileBackup(t, r, owner)
	if n := len(jobsOf(t, owner, model.RoleHomeBackup)); n != 1 {
		t.Fatalf("%d jobs for one run", n)
	}

	// The next night's run fails: the last good backup stays, with the reason.
	env(t).backupClock = env(t).backupClock.Add(24 * time.Hour)
	defer func() { env(t).backupClock = env(t).backupClock.Add(-24 * time.Hour) }()
	askRun(t, "run-2")
	reconcileBackup(t, r, owner)
	jobs := jobsOf(t, owner, model.RoleHomeBackup)
	if len(jobs) != 2 {
		t.Fatalf("%d jobs on the second night", len(jobs))
	}
	slices.SortFunc(jobs, func(a, b batchv1.Job) int { return a.CreationTimestamp.Compare(b.CreationTimestamp.Time) })
	next := jobs[slices.IndexFunc(jobs, func(j batchv1.Job) bool { return j.Name == getHome(t, owner).Status.Backup.Job })]
	finishWith(t, next, false, backup.JobResult{Reason: string(reasons.BackupDenied), Evidence: []string{"Access Denied"}})
	reconcileBackup(t, r, owner)
	b = getHome(t, owner).Status.Backup
	if b.Snapshot != "abc123" || b.Reason != string(reasons.BackupDenied) || b.FailedAt == nil || len(b.Evidence) == 0 {
		t.Fatalf("after a failed backup: %+v", b)
	}
}

func restoring(t *testing.T, owner string) *metav1.Condition {
	t.Helper()
	return meta.FindStatusCondition(getHome(t, owner).Status.Conditions, model.ConditionRestoring)
}

func requestRestore(t *testing.T, owner, mode string) {
	t.Helper()
	_, err := env(t).db.Homes().RequestRestore(ctx, model.Actor{Person: "admin"}, getHome(t, owner).ID, model.HomeRestoreRequest{
		Snapshot: "abc123", SnapshotTime: metav1.NewTime(time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)), Mode: mode,
	}, env(t).backupClock)
	must(t, err)
}

func TestARestoreHoldsTheAppsRunsAndLetsThemGo(t *testing.T) {
	t.Parallel()
	owner := uniqueOwner("dee")
	readyHome(t, owner)
	pod := appPod(t, owner, owner+"-lab")
	requestRestore(t, owner, model.RestoreBeside)
	r := backupReconciler(t)

	reconcileBackup(t, r, owner)
	if c := restoring(t, owner); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("Restoring = %+v, want True while it waits", c)
	}
	if st := getHome(t, owner).Status.Restore; st == nil || st.Phase != model.RestoreWaiting {
		t.Fatalf("restore = %+v", st)
	}
	if n := len(jobsOf(t, owner, model.RoleHomeRestore)); n != 0 {
		t.Fatal("a restore job started while an app runs")
	}

	must(t, env(t).k8s.Delete(ctx, pod, client.GracePeriodSeconds(0)))
	reconcileBackup(t, r, owner)
	jobs := jobsOf(t, owner, model.RoleHomeRestore)
	if len(jobs) != 1 {
		t.Fatalf("%d restore jobs", len(jobs))
	}
	c := jobs[0].Spec.Template.Spec.Containers[0]
	args := strings.Join(c.Args, " ")
	for _, want := range []string{"backup job restore", "--snapshot abc123", "--mode beside", "--folder restored-2026-11-01"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	home := slices.IndexFunc(c.VolumeMounts, func(m corev1.VolumeMount) bool { return m.MountPath == podstorage.HomeDir })
	if home < 0 || c.VolumeMounts[home].ReadOnly {
		t.Errorf("home mount = %+v", c.VolumeMounts)
	}
	if st := getHome(t, owner).Status.Restore; st.Phase != model.RestoreRestoring || st.Job != jobs[0].Name {
		t.Fatalf("restore = %+v", st)
	}

	finishWith(t, jobs[0], true, backup.JobResult{Snapshot: "abc123"})
	reconcileBackup(t, r, owner)
	h := getHome(t, owner)
	if h.Status.Restore.Phase != model.RestoreDone || h.Status.Restore.FinishedAt == nil {
		t.Fatalf("restore = %+v", h.Status.Restore)
	}
	if c := restoring(t, owner); c == nil || c.Status != metav1.ConditionFalse {
		t.Fatalf("Restoring = %+v, want False so the apps start", c)
	}
}

// The run of the restore job itself runs in its owner's home; it does not
// hold up the restore of that very home.
func TestTheRestoreJobsOwnRunDoesNotHoldUpItsHome(t *testing.T) {
	t.Parallel()
	owner := uniqueOwner("gus")
	readyHome(t, owner)
	job, err := env(t).db.Jobs().Create(ctx, model.Actor{Person: owner}, &model.Job{Namespace: homeNS, Name: "restore", Owner: owner,
		Spec: model.JobSpec{Template: model.BackupTemplate, Engine: "command", Command: []string{"/javo", "backup", "restore-run"}}})
	must(t, err)
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: owner + "-restore-run", Namespace: homeNS, Labels: map[string]string{model.LabelOwner: owner, model.LabelJob: job.ID}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "x"}}},
	}
	must(t, env(t).k8s.Create(ctx, p))
	t.Cleanup(func() { _ = env(t).k8s.Delete(ctx, p, client.GracePeriodSeconds(0)) })
	requestRestore(t, owner, model.RestoreReplace)
	reconcileBackup(t, backupReconciler(t), owner)
	if n := len(jobsOf(t, owner, model.RoleHomeRestore)); n != 1 {
		t.Fatalf("%d restore jobs: the restore job's own run held it up", n)
	}
}

func TestAFailedRestoreSaysWhyAndLetsTheAppsGo(t *testing.T) {
	t.Parallel()
	owner := uniqueOwner("eve")
	readyHome(t, owner)
	requestRestore(t, owner, model.RestoreReplace)
	r := backupReconciler(t)
	reconcileBackup(t, r, owner)
	jobs := jobsOf(t, owner, model.RoleHomeRestore)
	if len(jobs) != 1 {
		t.Fatalf("%d restore jobs", len(jobs))
	}
	finishWith(t, jobs[0], false, backup.JobResult{Reason: string(reasons.BackupWrongPassword), Evidence: []string{"Fatal: wrong password"}})
	reconcileBackup(t, r, owner)
	st := getHome(t, owner).Status.Restore
	if st.Phase != model.RestoreFailed || st.Reason != string(reasons.BackupWrongPassword) || len(st.Evidence) == 0 {
		t.Fatalf("restore = %+v", st)
	}
	if c := restoring(t, owner); c == nil || c.Status != metav1.ConditionFalse || c.Reason != string(reasons.HomeRestoreFailed) {
		t.Fatalf("Restoring = %+v", c)
	}
}

// After a disaster the database comes back with homes whose claims are
// gone: a restore lets the home get a new claim first.
func TestARestoreOfAHomeWithoutItsClaimGivesItANewOne(t *testing.T) {
	t.Parallel()
	owner := uniqueOwner("fay")
	readyHome(t, owner)
	editHome(t, homeNS, owner, func(h *model.Home) { h.Status.ClaimName = naming.HomeClaimNameGen(owner, 7) })
	requestRestore(t, owner, model.RestoreReplace)
	r := backupReconciler(t)
	reconcileBackup(t, r, owner)
	h := getHome(t, owner)
	if h.Status.ClaimName != "" || h.Status.Prepared {
		t.Fatalf("home keeps the missing claim: %+v", h.Status)
	}
	if n := len(jobsOf(t, owner, model.RoleHomeRestore)); n != 0 {
		t.Fatal("restore job before the home has a claim")
	}
}

// A full run backs up every ready shared folder too: read-only, as a member
// of its group (the folder's gid), never as root.
func TestARunBacksUpASharedFolderAsAGroupMember(t *testing.T) {
	t.Parallel()
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 1})
	f := newFolder(t, "/data", nil)
	f.Status = model.SharedFolderStatus{ClaimName: "folder-" + f.Name, GID: 200123, Prepared: true}
	got, err := env(t).db.SharedFolders().Update(ctx, *f, f.Version)
	must(t, err)
	askRun(t, "run-9")
	r := &FolderBackupReconciler{BackupReconciler: *backupReconciler(t)}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: got.ID}}); err != nil {
		t.Fatal(err)
	}
	var l batchv1.JobList
	must(t, env(t).k8s.List(ctx, &l, client.InNamespace(homeNS), client.MatchingLabels{model.LabelFolder: got.ID, model.LabelRole: model.RoleFolderBackup}))
	if len(l.Items) != 1 {
		t.Fatalf("%d folder backup jobs", len(l.Items))
	}
	pod := l.Items[0].Spec.Template.Spec
	if sc := pod.SecurityContext; sc.RunAsUser == nil || *sc.RunAsUser == 0 || !slices.Contains(sc.SupplementalGroups, 200123) {
		t.Errorf("pod security = %+v", sc)
	}
	c := pod.Containers[0]
	args := strings.Join(c.Args, " ")
	for _, want := range []string{"--repo folders/" + homeNS + "/" + got.Name, "--dir /folder/data", "--run run-9"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if !c.VolumeMounts[0].ReadOnly || pod.Volumes[0].PersistentVolumeClaim.ClaimName != "folder-"+got.Name {
		t.Errorf("mounts %+v volumes %+v", c.VolumeMounts, pod.Volumes)
	}

	finishWith(t, l.Items[0], true, backup.JobResult{Snapshot: "f1"})
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: got.ID}}); err != nil {
		t.Fatal(err)
	}
	after, err := env(t).db.SharedFolders().Get(ctx, got.ID)
	must(t, err)
	if b := after.Status.Backup; b == nil || b.Run != "run-9" || b.Snapshot != "f1" || b.Job != "" {
		t.Fatalf("folder backup = %+v", b)
	}
}

func envOf(c corev1.Container, name string) *corev1.EnvVar {
	if i := slices.IndexFunc(c.Env, func(e corev1.EnvVar) bool { return e.Name == name }); i >= 0 {
		return &c.Env[i]
	}
	return nil
}

// Each backup Job's log line names the step, the person (display name,
// username, local or SSO), the home, its claim and the node it runs on, and
// restic reports its progress every second.
func TestABackupJobCarriesWhoAndWhereForItsLog(t *testing.T) {
	t.Parallel()
	owner := uniqueOwner("hal")
	readyHome(t, owner)
	askRun(t, "run-1")
	r := backupReconciler(t)
	r.People = labelOf(map[string]string{owner: "Hal Example (hal, local)"})
	reconcileBackup(t, r, owner)
	jobs := jobsOf(t, owner, model.RoleHomeBackup)
	if len(jobs) != 1 {
		t.Fatalf("%d jobs", len(jobs))
	}
	c := jobs[0].Spec.Template.Spec.Containers[0]
	args := strings.Join(c.Args, " ")
	for _, want := range []string{"--field step=home", "--field home=" + getHome(t, owner).ID, "--field claim=" + naming.HomeClaimName(owner),
		"--field person=Hal Example (hal, local)"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if e := envOf(c, "NODE_NAME"); e == nil || e.ValueFrom == nil || e.ValueFrom.FieldRef == nil || e.ValueFrom.FieldRef.FieldPath != "spec.nodeName" {
		t.Errorf("NODE_NAME = %+v", e)
	}
	if e := envOf(c, "RESTIC_PROGRESS_FPS"); e == nil || e.Value == "" {
		t.Errorf("RESTIC_PROGRESS_FPS = %+v", e)
	}
}

// labelOf answers who a home's owner is, as the people table would.
type labelOf map[string]string

func (l labelOf) PersonLabel(_ context.Context, owner string) (string, bool, error) {
	v, ok := l[owner]
	return v, ok, nil
}

// A home volume with no account (found from before, its owner has no uid)
// is still backed up: first a Job reads who owns its files, then the backup
// runs as that uid, read-only.
func TestAHomeVolumeWithoutAnAccountIsBackedUpAsItsFilesOwner(t *testing.T) {
	t.Parallel()
	owner := uniqueOwner("old")
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 1})
	newHome(t, owner, "10Gi")
	claim := naming.HomeClaimName(owner)
	must(t, env(t).k8s.Create(ctx, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: claim, Namespace: homeNS, Labels: map[string]string{model.LabelOwner: owner}},
		Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}},
	}))
	editHome(t, homeNS, owner, func(h *model.Home) { h.Status.ClaimName = claim })
	askRun(t, "run-1")
	r := backupReconciler(t)
	r.UIDs = ids.People{People: env(t).db.People(store.IDRange{}), Mode: config.UIDModeAllocated}
	r.People = labelOf{}
	reconcileBackup(t, r, owner)
	probes := jobsOf(t, owner, model.RoleHomeOwnerProbe)
	if len(probes) != 1 || len(jobsOf(t, owner, model.RoleHomeBackup)) != 0 {
		t.Fatalf("%d probe jobs before the backup", len(probes))
	}
	pod := probes[0].Spec.Template.Spec
	if *pod.SecurityContext.RunAsUser == 0 || !pod.Containers[0].VolumeMounts[0].ReadOnly {
		t.Fatalf("probe pod = %+v", pod)
	}
	finishWith(t, probes[0], true, backup.JobResult{UID: 1001, GID: 100})
	reconcileBackup(t, r, owner)
	h := getHome(t, owner)
	if h.Status.VolumeUID != 1001 || h.Status.VolumeGID != 100 {
		t.Fatalf("volume owner = %d:%d", h.Status.VolumeUID, h.Status.VolumeGID)
	}
	jobs := jobsOf(t, owner, model.RoleHomeBackup)
	if len(jobs) != 1 {
		t.Fatalf("%d backup jobs", len(jobs))
	}
	sc := jobs[0].Spec.Template.Spec.SecurityContext
	if *sc.RunAsUser != 1001 || *sc.RunAsGroup != 100 {
		t.Fatalf("runs as %d:%d", *sc.RunAsUser, *sc.RunAsGroup)
	}
	if args := strings.Join(jobs[0].Spec.Template.Spec.Containers[0].Args, " "); !strings.Contains(args, "--field person=no account") {
		t.Errorf("args %q", args)
	}

	// It is restored the same way, though it was never prepared.
	finishWith(t, jobs[0], true, backup.JobResult{Snapshot: "s1"})
	reconcileBackup(t, r, owner)
	requestRestore(t, owner, model.RestoreReplace)
	reconcileBackup(t, r, owner)
	restores := jobsOf(t, owner, model.RoleHomeRestore)
	if len(restores) != 1 {
		t.Fatalf("%d restore jobs for the volume with no account", len(restores))
	}
	if sc := restores[0].Spec.Template.Spec.SecurityContext; *sc.RunAsUser != 1001 {
		t.Fatalf("restore runs as %d", *sc.RunAsUser)
	}
	if args := strings.Join(restores[0].Spec.Template.Spec.Containers[0].Args, " "); !strings.Contains(args, "--field step=restore") {
		t.Errorf("restore args %q", args)
	}
}

// Every home volume javo made is found, even one whose row is gone (from
// before the database): it gets a home row, so it is listed and backed up.
func TestHomeVolumesWithoutARowGetOne(t *testing.T) {
	t.Parallel()
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 1})
	lost, known := uniqueOwner("lost"), uniqueOwner("kept")
	newHome(t, known, "10Gi")
	for _, c := range []struct{ name, owner string }{
		{naming.HomeClaimName(lost), lost}, {naming.HomeClaimName(known), known}, {"folder-" + uniqueOwner("data"), ""},
	} {
		labels := map[string]string{}
		if c.owner != "" {
			labels[model.LabelOwner] = c.owner
		}
		must(t, env(t).k8s.Create(ctx, &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: c.name, Namespace: homeNS, Labels: labels},
			Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}},
		}))
	}
	e := env(t)
	a := VolumeFinder{Reader: e.k8s, Store: e.db, Namespaces: []string{homeNS}, Now: func() time.Time { return e.backupClock }}
	n, err := a.AdoptHomes(ctx)
	must(t, err)
	// envtest keeps the claims of earlier tests, whose rows are gone too.
	if n < 1 {
		t.Fatalf("adopted %d, want the lost one", n)
	}
	if hs, _ := env(t).db.Homes().ListByOwner(ctx, known); len(hs) != 1 {
		t.Fatalf("the known home has %d rows", len(hs))
	}
	if _, err := env(t).db.Homes().ByOwner(ctx, homeNS, lost); err != nil {
		t.Fatalf("no row for the lost volume: %v", err)
	}
	if n, _ := a.AdoptHomes(ctx); n != 0 {
		t.Fatalf("adopted again: %d", n)
	}

	// A volume found while a full backup runs joins that run.
	askRun(t, "run-7")
	late := uniqueOwner("late")
	must(t, env(t).k8s.Create(ctx, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: naming.HomeClaimName(late), Namespace: homeNS, Labels: map[string]string{model.LabelOwner: late}},
		Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}},
	}))
	_, err = a.AdoptHomes(ctx)
	must(t, err)
	if h, err := env(t).db.Homes().ByOwner(ctx, homeNS, late); err != nil || h.Spec.Backup == nil || h.Spec.Backup.Run != "run-7" {
		t.Fatalf("the late volume's home = %+v, %v", h.Spec.Backup, err)
	}
}

// A home row with no account whose volume is gone (or going) does not hold a
// run up: its step fails at once, saying so.
func TestAGoneVolumeWithNoAccountFailsItsStepAtOnce(t *testing.T) {
	t.Parallel()
	owner := uniqueOwner("gone")
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 1})
	newHome(t, owner, "10Gi")
	editHome(t, homeNS, owner, func(h *model.Home) { h.Status.ClaimName = naming.HomeClaimName(owner) })
	askRun(t, "run-1")
	r := backupReconciler(t)
	r.People = labelOf{}
	reconcileBackup(t, r, owner)
	b := getHome(t, owner).Status.Backup
	if b == nil || b.Run != "run-1" || b.Reason == "" || b.Job != "" || len(jobsOf(t, owner, model.RoleHomeOwnerProbe)) != 0 {
		t.Fatalf("backup = %+v", b)
	}
}

// A home row with no account whose claim the home controller refused as
// another account's (owner annotation disagrees with its label) never gets a
// claim: its step fails at once, saying so, instead of holding the run up.
func TestAForeignVolumeWithNoAccountFailsItsStepAtOnce(t *testing.T) {
	t.Parallel()
	owner := uniqueOwner("foreign")
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 1})
	newHome(t, owner, "10Gi")
	editHome(t, homeNS, owner, func(h *model.Home) {
		meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{Type: model.ConditionReady, Status: metav1.ConditionFalse,
			Reason: string(reasons.HomeClaimForeign), Message: "claim is not labelled as belonging to this home's owner"})
	})
	askRun(t, "run-1")
	r := backupReconciler(t)
	r.People = labelOf{}
	reconcileBackup(t, r, owner)
	b := getHome(t, owner).Status.Backup
	if b == nil || b.Run != "run-1" || b.Reason == "" || b.FailedAt == nil || b.Job != "" || len(b.Evidence) == 0 {
		t.Fatalf("backup = %+v", b)
	}
}

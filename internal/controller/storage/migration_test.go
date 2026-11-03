package storage

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
)

// clock is the migration reconciler's time in these tests.
var clock = time.Now().Truncate(time.Second)

func migrationReconciler(c client.Client) *MigrationReconciler {
	return &MigrationReconciler{
		Client: c, Reader: c, Rows: testRows(), Image: testImage,
		Backends: testBackends(), UIDs: fixedUIDs(),
		Storage: podstorage.Resolver{Homes: db.Homes(), Folders: db.SharedFolders()},
		Now:     func() time.Time { return clock },
	}
}

func reconcileMigration(t *testing.T, r *MigrationReconciler, owner string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: getHome(t, owner).ID}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// movableHomes makes prepared perUser homes, then switches the cluster's
// choice to the shared mode, so each home is on another mode than the cluster.
func movableHomes(t *testing.T, owners ...string) {
	t.Helper()
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3, expandable: true})
	hr := homeReconciler(k8s)
	for _, o := range owners {
		newHome(t, o, "10Gi")
		settled(t, hr, o)
	}
	cs := get(t)
	cs.Status.Choice = &model.StorageChoice{Mode: model.HomeModeShared, HomeClass: "files"}
	setStatus(t, cs.Status)
}

func scheduleAt(t *testing.T, at time.Time) {
	t.Helper()
	mode := get(t).Status.Choice.Mode
	setRequests(t, func(r *model.StorageRequests) {
		r.Migration.StartAt = &metav1.Time{Time: at}
		r.Migration.To = mode // what `migrate schedule` records
	})
}

func bumpAttempt(t *testing.T) {
	t.Helper()
	setRequests(t, func(r *model.StorageRequests) { r.Migration.Attempt++ })
}

func appPod(t *testing.T, owner, name string) *corev1.Pod {
	t.Helper()
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: homeNS, Labels: map[string]string{model.LabelOwner: owner, model.LabelApp: name}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "x"}}},
	}
	must(t, k8s.Create(ctx, p))
	t.Cleanup(func() { _ = client.IgnoreNotFound(k8s.Delete(ctx, p, client.GracePeriodSeconds(0))) })
	return p
}

func copyJobs(t *testing.T, owner string) []batchv1.Job {
	t.Helper()
	var l batchv1.JobList
	must(t, k8s.List(ctx, &l, client.InNamespace(homeNS), client.MatchingLabels{
		model.LabelRole: model.RoleHomeMigrate, model.LabelOwner: owner}))
	return l.Items
}

func phase(t *testing.T, owner string) model.MigrationPhase {
	t.Helper()
	if m := getHome(t, owner).Status.Migration; m != nil {
		return m.Phase
	}
	return ""
}

func migrating(t *testing.T, owner string) *metav1.Condition {
	t.Helper()
	return meta.FindStatusCondition(getHome(t, owner).Status.Conditions, model.ConditionMigrating)
}

// claimGone is true for a claim that is deleted or being deleted (nothing in
// envtest clears the protection finalizer).
func claimGone(t *testing.T, name string) bool {
	t.Helper()
	c := &corev1.PersistentVolumeClaim{}
	err := k8s.Get(ctx, types.NamespacedName{Namespace: homeNS, Name: name}, c)
	return apierrors.IsNotFound(err) || (err == nil && !c.DeletionTimestamp.IsZero())
}

func TestChangingTheModeAloneCreatesNoJobAndLeavesHomesReady(t *testing.T) {
	owner := uniqueOwner("ann")
	movableHomes(t, owner)
	r := migrationReconciler(k8s)
	reconcileMigration(t, r, owner)

	h := getHome(t, owner)
	if h.Status.Migration != nil || h.Status.Mode != model.HomeModePerUser {
		t.Fatalf("home changed by a mode change alone: %+v", h.Status)
	}
	if c := ready(t, h); c.Status != metav1.ConditionTrue {
		t.Fatalf("Ready = %+v", c)
	}
	if c := migrating(t, owner); c != nil && c.Status == metav1.ConditionTrue {
		t.Fatalf("Migrating = %+v", c)
	}
	if n := len(copyJobs(t, owner)); n != 0 {
		t.Fatalf("%d copy jobs", n)
	}
}

func TestAFutureStartMarksOnlyTheHomesOnAnotherModeAsScheduled(t *testing.T) {
	a, b, c := uniqueOwner("ann"), uniqueOwner("ben"), uniqueOwner("cy")
	movableHomes(t, a, b)
	// c is already on the chosen mode.
	newHome(t, c, "10Gi")
	editHome(t, homeNS, c, func(hc *model.Home) {
		hc.Status.Mode, hc.Status.ClaimName, hc.Status.Prepared = model.HomeModeShared, naming.HomeClaimNameGen(c, 1), true
	})

	start := clock.Add(2 * time.Hour)
	scheduleAt(t, start)
	r := migrationReconciler(k8s)
	res := reconcileMigration(t, r, a)
	reconcileMigration(t, r, b)
	reconcileMigration(t, r, c)

	if phase(t, a) != model.MigrationScheduled || phase(t, b) != model.MigrationScheduled {
		t.Fatalf("phases = %q, %q, want Scheduled", phase(t, a), phase(t, b))
	}
	if phase(t, c) != "" {
		t.Fatalf("home already on the chosen mode is %q", phase(t, c))
	}
	if res.RequeueAfter != 2*time.Hour {
		t.Errorf("requeue = %v, want 2h (wakes at the start time)", res.RequeueAfter)
	}
	// Scheduled only drives the banner: apps still run and nothing is copied.
	if cnd := migrating(t, a); cnd != nil && cnd.Status == metav1.ConditionTrue {
		t.Errorf("Migrating = %+v while only scheduled", cnd)
	}
	if n := len(copyJobs(t, a)); n != 0 {
		t.Errorf("%d copy jobs before the start time", n)
	}
	// An admin who removes the schedule takes the banner away again.
	setRequests(t, func(r *model.StorageRequests) { r.Migration.StartAt = nil })
	reconcileMigration(t, r, a)
	if phase(t, a) != "" {
		t.Errorf("phase = %q after unscheduling", phase(t, a))
	}
}

func TestARunningAppHoldsTheHomeWaitingUntilItStops(t *testing.T) {
	owner := uniqueOwner("dan")
	movableHomes(t, owner)
	pod := appPod(t, owner, "lab-"+owner)
	scheduleAt(t, clock.Add(-time.Minute))
	r := migrationReconciler(k8s)

	res := reconcileMigration(t, r, owner)
	if phase(t, owner) != model.MigrationWaiting {
		t.Fatalf("phase = %q, want Waiting", phase(t, owner))
	}
	// Migrating=True is what holds the app (workspace reconciler: HomeMigrating).
	if c := migrating(t, owner); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("Migrating = %+v, want True so the app is held", c)
	}
	if res.RequeueAfter == 0 {
		t.Error("no requeue while waiting for the app to stop")
	}
	if _, err := getClaimErr(naming.HomeClaimNameGen(owner, 2)); !apierrors.IsNotFound(err) {
		t.Fatalf("new claim made while an app still runs: %v", err)
	}
	if n := len(copyJobs(t, owner)); n != 0 {
		t.Fatalf("%d copy jobs while an app runs", n)
	}

	must(t, k8s.Delete(ctx, pod, client.GracePeriodSeconds(0)))
	reconcileMigration(t, r, owner)
	if phase(t, owner) != model.MigrationCopying {
		t.Fatalf("phase = %q after the app stopped, want Copying", phase(t, owner))
	}
	h := getHome(t, owner)
	newName := naming.HomeClaimNameGen(owner, 2)
	if h.Status.Migration.NewClaim != newName || h.Status.ClaimName != naming.HomeClaimNameGen(owner, 1) {
		t.Fatalf("new claim %q, current %q", h.Status.Migration.NewClaim, h.Status.ClaimName)
	}
	nc := getClaim(t, newName)
	if nc.Spec.AccessModes[0] != corev1.ReadWriteMany || *nc.Spec.StorageClassName != "files" || !owns(nc, owner) {
		t.Fatalf("new claim = %+v", nc.Spec)
	}
}

func getClaimErr(name string) (*corev1.PersistentVolumeClaim, error) {
	c := &corev1.PersistentVolumeClaim{}
	return c, k8s.Get(ctx, types.NamespacedName{Namespace: homeNS, Name: name}, c)
}

// copying takes one home to a running copy Job (the new claim is prepared by
// its own Job first, which the test completes).
func copying(t *testing.T, r *MigrationReconciler, owner string) batchv1.Job {
	t.Helper()
	scheduleAt(t, clock.Add(-time.Minute))
	reconcileMigration(t, r, owner) // Copying: new claim and its prepare Job
	finishJob(t, prepareJobFor(t, owner, naming.HomeClaimNameGen(owner, 2)), true, "")
	reconcileMigration(t, r, owner) // the copy Job
	jobs := copyJobs(t, owner)
	if len(jobs) != 1 {
		t.Fatalf("copy jobs = %d, want 1", len(jobs))
	}
	return jobs[0]
}

func prepareJobFor(t *testing.T, owner, claim string) batchv1.Job {
	t.Helper()
	j := batchv1.Job{}
	must(t, k8s.Get(ctx, types.NamespacedName{Namespace: homeNS, Name: prepareJobNameFor(claim)}, &j))
	if j.Labels[model.LabelRole] != model.RoleHomePrepare || j.Labels[model.LabelOwner] != owner {
		t.Fatalf("job %s labels = %v", j.Name, j.Labels)
	}
	return j
}

func TestTheCopyJobMountsOldReadOnlyAndNewAsTheOwnerWithoutFSGroup(t *testing.T) {
	owner := uniqueOwner("eve")
	movableHomes(t, owner)
	r := migrationReconciler(k8s)
	j := copying(t, r, owner)

	spec := j.Spec.Template.Spec
	h := getHome(t, owner)
	if len(spec.Volumes) != 2 {
		t.Fatalf("volumes = %+v", spec.Volumes)
	}
	var old, nw *corev1.PersistentVolumeClaimVolumeSource
	for _, v := range spec.Volumes {
		switch v.PersistentVolumeClaim.ClaimName {
		case h.Status.ClaimName:
			old = v.PersistentVolumeClaim
		case h.Status.Migration.NewClaim:
			nw = v.PersistentVolumeClaim
		}
	}
	if old == nil || nw == nil || !old.ReadOnly || nw.ReadOnly {
		t.Fatalf("old = %+v, new = %+v, want old read-only and new writable", old, nw)
	}
	for _, m := range spec.Containers[0].VolumeMounts {
		if m.MountPath == "/old" && !m.ReadOnly {
			t.Errorf("/old is mounted writable")
		}
	}
	sc := spec.SecurityContext
	if sc.FSGroup != nil {
		t.Fatal("copy job sets fsGroup with two volumes mounted")
	}
	if *sc.RunAsUser != 1000 || *sc.RunAsGroup != 100 || !*sc.RunAsNonRoot {
		t.Fatalf("runs as %d:%d nonroot=%v", *sc.RunAsUser, *sc.RunAsGroup, *sc.RunAsNonRoot)
	}
	// The javo image copies and checks (`javo storage copy`); no other image.
	if ctr := spec.Containers[0]; ctr.Image != testImage.Ref || !pullsTestImage(spec) || strings.Join(ctr.Command, " ") != "/javo storage copy --old /old --new /new" || *spec.AutomountServiceAccountToken {
		t.Fatalf("image %q, command %q, token mounted %v", ctr.Image, ctr.Command, *spec.AutomountServiceAccountToken)
	}
	if j.Spec.TTLSecondsAfterFinished == nil || j.Spec.ActiveDeadlineSeconds == nil || *j.Spec.BackoffLimit != 0 || j.Labels[model.LabelHome] != h.ID {
		t.Fatalf("ttl %v deadline %v backoff %v labels %v", j.Spec.TTLSecondsAfterFinished, j.Spec.ActiveDeadlineSeconds, *j.Spec.BackoffLimit, j.Labels)
	}
	if h.Status.Migration.Job != j.Name {
		t.Errorf("status.migration.job = %q, want %q (the status command prints its logs command)", h.Status.Migration.Job, j.Name)
	}
	if h.Status.ClaimName != naming.HomeClaimNameGen(owner, 1) || h.Status.Mode != model.HomeModePerUser {
		t.Fatalf("home switched before the copy finished: %+v", h.Status)
	}
	if c := migrating(t, owner); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("Migrating = %+v", c)
	}
}

// A partial copy, a mismatch or an eviction never moves the
// home. It stays on its old claim, writable, with the evidence.
func TestAFailedCopyKeepsTheHomeOnItsOldClaimWithTheEvidence(t *testing.T) {
	for name, msg := range map[string]string{
		"partial transfer": "copy failed: partial transfer\nsecret: open /old/secret: permission denied",
		"verify mismatch":  "verify found differences\ncontent notes/draft.txt",
	} {
		t.Run(name, func(t *testing.T) {
			owner := uniqueOwner("fay")
			movableHomes(t, owner)
			r := migrationReconciler(k8s)
			j := copying(t, r, owner)
			finishJob(t, j, false, msg)
			reconcileMigration(t, r, owner)

			h := getHome(t, owner)
			if h.Status.ClaimName != naming.HomeClaimNameGen(owner, 1) || h.Status.Mode != model.HomeModePerUser {
				t.Fatalf("home moved on a failed copy: %+v", h.Status)
			}
			if phase(t, owner) != model.MigrationFailed {
				t.Fatalf("phase = %q", phase(t, owner))
			}
			c := migrating(t, owner)
			first := strings.SplitN(msg, "\n", 2)[0]
			if c == nil || c.Status != metav1.ConditionFalse || c.Reason != string(reasons.MigrationFailed) || !strings.Contains(c.Message, first) {
				t.Fatalf("Migrating = %+v, want False/MigrationFailed mentioning %q", c, first)
			}
			if !strings.Contains(strings.Join(h.Status.Migration.Evidence, "\n"), strings.SplitN(msg, "\n", 2)[1]) {
				t.Errorf("evidence = %q, want the failing paths", h.Status.Migration.Evidence)
			}
			if !claimGone(t, naming.HomeClaimNameGen(owner, 2)) {
				t.Error("the new claim was kept")
			}
			if claimGone(t, h.Status.ClaimName) {
				t.Fatal("the old claim was removed")
			}
		})
	}
}

func TestAnEvictedOrTimedOutJobIsAFailureToo(t *testing.T) {
	owner := uniqueOwner("gus")
	movableHomes(t, owner)
	r := migrationReconciler(k8s)
	j := copying(t, r, owner)
	started := metav1.NewTime(time.Now().Add(-time.Minute))
	j.Status.StartTime = &started
	j.Status.Failed = 1
	j.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded", Message: "Job was active longer than specified deadline"},
		{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded", Message: "Job was active longer than specified deadline"},
	}
	must(t, k8s.Status().Update(ctx, &j))
	reconcileMigration(t, r, owner)
	c := migrating(t, owner)
	if phase(t, owner) != model.MigrationFailed || c == nil || !strings.Contains(c.Message, "DeadlineExceeded") {
		t.Fatalf("phase %q, Migrating %+v", phase(t, owner), c)
	}
}

func TestOnlyFailedHomesRetryWhenTheAttemptIsBumped(t *testing.T) {
	a, b := uniqueOwner("hal"), uniqueOwner("ida")
	movableHomes(t, a, b)
	r := migrationReconciler(k8s)
	ja := copying(t, r, a)
	copying(t, r, b) // running; its Job stays active
	finishJob(t, ja, false, "copy failed: partial transfer\nnotes: lstat /old/notes: no such file or directory")
	reconcileMigration(t, r, a)
	reconcileMigration(t, r, a)
	if phase(t, a) != model.MigrationFailed {
		t.Fatalf("a failed home retried without a bump: %q", phase(t, a))
	}

	bumpAttempt(t)
	reconcileMigration(t, r, a)
	reconcileMigration(t, r, b)
	if phase(t, a) != model.MigrationCopying {
		t.Errorf("failed home phase = %q after the bump, want Copying", phase(t, a))
	}
	if phase(t, b) != model.MigrationCopying || len(copyJobs(t, b)) != 1 {
		t.Errorf("running home phase = %q with %d jobs: the bump must not touch it", phase(t, b), len(copyJobs(t, b)))
	}
	// The retry copies to a claim of its own and runs a new Job.
	ha := getHome(t, a)
	retryClaim := ha.Status.Migration.NewClaim
	if retryClaim == naming.HomeClaimNameGen(a, 2) {
		t.Fatalf("retry reused the failed attempt's claim %s", retryClaim)
	}
	finishJob(t, prepareJobFor(t, a, retryClaim), true, "")
	reconcileMigration(t, r, a)
	if n := len(copyJobs(t, a)); n != 2 {
		t.Errorf("copy jobs = %d, want the failed one kept and a new one", n)
	}
}

func TestACompletedCopySwitchesTheHomeAndKeepsTheOldClaimLabelled(t *testing.T) {
	owner := uniqueOwner("joy")
	movableHomes(t, owner)
	r := migrationReconciler(k8s)
	j := copying(t, r, owner)
	oldName := getHome(t, owner).Status.ClaimName
	finishJob(t, j, true, "")
	reconcileMigration(t, r, owner)

	h := getHome(t, owner)
	newName := naming.HomeClaimNameGen(owner, 2)
	if h.Status.ClaimName != newName || h.Status.Mode != model.HomeModeShared || h.Status.StorageClassName != "files" {
		t.Fatalf("status = %+v", h.Status)
	}
	m := h.Status.Migration
	retain := clock.Add(7 * 24 * time.Hour)
	if m.Phase != model.MigrationSwitched || m.OldClaim != oldName || m.RetainUntil == nil || !m.RetainUntil.Time.Equal(retain) {
		t.Fatalf("migration = %+v, want Switched, old %s, retain until %s", m, oldName, retain)
	}
	if got := getClaim(t, oldName).Labels[model.LabelRetainUntil]; got != retain.UTC().Format("2006-01-02") {
		t.Errorf("old claim retain-until label = %q", got)
	}
	// Apps start again by themselves: the hold is lifted.
	if c := migrating(t, owner); c == nil || c.Status != metav1.ConditionFalse || c.Reason == string(reasons.MigrationFailed) {
		t.Fatalf("Migrating = %+v, want False", c)
	}
	// A pass after the switch changes nothing (no second Job, no new claim).
	reconcileMigration(t, r, owner)
	if len(copyJobs(t, owner)) != 1 || getHome(t, owner).Status.ClaimName != newName {
		t.Fatal("a pass after the switch changed the home")
	}

	// Once the grace period is over the old claim goes, and the home forgets it.
	clock = clock.Add(7*24*time.Hour + time.Minute)
	t.Cleanup(func() { clock = clock.Add(-(7*24*time.Hour + time.Minute)) })
	reconcileMigration(t, r, owner)
	if !claimGone(t, oldName) {
		t.Error("old claim kept past retain-until")
	}
	if getHome(t, owner).Status.Migration != nil {
		t.Errorf("migration = %+v after the old claim went", getHome(t, owner).Status.Migration)
	}
	if claimGone(t, newName) {
		t.Fatal("the live claim was removed")
	}
}

func TestTheOldClaimIsOnlyDeletedWhenItIsTheHomesOwnAndLabelled(t *testing.T) {
	owner := uniqueOwner("kit")
	movableHomes(t, owner)
	// A claim that carries the name but not the labels the migration puts on.
	foreign := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "someone-elses-claim", Namespace: homeNS},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: *getHome(t, owner).Spec.Size}},
		},
	}
	must(t, k8s.Create(ctx, foreign))
	past := metav1.NewTime(clock.Add(-time.Hour))
	editHome(t, homeNS, owner, func(h *model.Home) {
		h.Status.Migration = &model.HomeMigration{Phase: model.MigrationSwitched, OldClaim: foreign.Name, RetainUntil: &past}
	})

	reconcileMigration(t, migrationReconciler(k8s), owner)
	if claimGone(t, foreign.Name) {
		t.Fatal("a claim without the retain-until label was deleted")
	}
	if getHome(t, owner).Status.Migration != nil {
		t.Error("the home kept pointing at a claim it did not delete")
	}
	// The home's live claim is never "the old one", whatever the status says.
	editHome(t, homeNS, owner, func(h *model.Home) {
		h.Status.Migration = &model.HomeMigration{Phase: model.MigrationSwitched, OldClaim: h.Status.ClaimName, RetainUntil: &past}
	})
	h := getHome(t, owner)
	reconcileMigration(t, migrationReconciler(k8s), owner)
	if claimGone(t, h.Status.ClaimName) {
		t.Fatal("the live claim was deleted as the old one")
	}
}

func TestChangingTheModeBackCancelsAScheduledMove(t *testing.T) {
	owner := uniqueOwner("lee")
	movableHomes(t, owner)
	r := migrationReconciler(k8s)
	j := copying(t, r, owner)
	_ = j
	setChoiceMode(t, model.HomeModePerUser)
	reconcileMigration(t, r, owner)
	if phase(t, owner) != "" || !claimGone(t, naming.HomeClaimNameGen(owner, 2)) {
		t.Fatalf("phase %q, new claim gone %v", phase(t, owner), claimGone(t, naming.HomeClaimNameGen(owner, 2)))
	}
	if c := migrating(t, owner); c != nil && c.Status == metav1.ConditionTrue {
		t.Fatalf("Migrating = %+v after cancelling", c)
	}
}

func setChoiceMode(t *testing.T, mode model.HomeMode) {
	t.Helper()
	cs := get(t)
	cs.Status.Choice.Mode = mode
	setStatus(t, cs.Status)
}

// A schedule belongs to the round it was made for. After the homes moved,
// flipping the mode back (or the retention ending after it) must not move
// anything: the old start time is in the past, and an admin has not scheduled
// a new move.
func TestAFinishedRoundsScheduleDoesNotMoveHomesWhenTheModeChangesLater(t *testing.T) {
	owner := uniqueOwner("ned")
	movableHomes(t, owner)
	r := migrationReconciler(k8s)
	j := copying(t, r, owner)
	finishJob(t, j, true, "")
	reconcileMigration(t, r, owner)
	if phase(t, owner) != model.MigrationSwitched {
		t.Fatalf("phase = %q, want Switched", phase(t, owner))
	}

	setChoiceMode(t, model.HomeModePerUser) // the admin changes the mode back
	clock = clock.Add(7*24*time.Hour + time.Minute)
	t.Cleanup(func() { clock = clock.Add(-(7*24*time.Hour + time.Minute)) })
	reconcileMigration(t, r, owner) // the retention ends
	reconcileMigration(t, r, owner)
	reconcileMigration(t, r, owner)

	h := getHome(t, owner)
	if h.Status.Migration != nil || h.Status.Mode != model.HomeModeShared || h.Status.ClaimName != naming.HomeClaimNameGen(owner, 2) {
		t.Fatalf("home left Ready without a new schedule: %+v", h.Status)
	}
	if c := migrating(t, owner); c != nil && c.Status == metav1.ConditionTrue {
		t.Fatalf("Migrating = %+v", c)
	}
	if n := len(copyJobs(t, owner)); n != 1 {
		t.Fatalf("%d copy jobs, want only the first round's", n)
	}
}

// A pod that appears after the wait must not write to the old
// claim during the copy and have those writes dropped at the switch.
func TestAnAppPodThatAppearsDuringTheCopyBlocksTheSwitch(t *testing.T) {
	owner := uniqueOwner("oli")
	movableHomes(t, owner)
	r := migrationReconciler(k8s)
	j := copying(t, r, owner)
	appPod(t, owner, "late-"+owner)
	finishJob(t, j, true, "")
	reconcileMigration(t, r, owner)

	h := getHome(t, owner)
	if h.Status.ClaimName != naming.HomeClaimNameGen(owner, 1) || h.Status.Mode != model.HomeModePerUser {
		t.Fatalf("home switched with an app pod running: %+v", h.Status)
	}
	m := h.Status.Migration
	if m == nil || m.Phase != model.MigrationFailed || !strings.Contains(strings.Join(m.Evidence, "\n"), "late-"+owner) {
		t.Fatalf("migration = %+v, want Failed with evidence naming the pod", m)
	}
}

func TestNoCopyJobStartsWhileAnAppPodExists(t *testing.T) {
	owner := uniqueOwner("pam")
	movableHomes(t, owner)
	r := migrationReconciler(k8s)
	scheduleAt(t, clock.Add(-time.Minute))
	reconcileMigration(t, r, owner) // Copying: the new claim and its prepare Job
	finishJob(t, prepareJobFor(t, owner, naming.HomeClaimNameGen(owner, 2)), true, "")
	pod := appPod(t, owner, "late-"+owner) // made by a pass that read the Home before the hold

	res := reconcileMigration(t, r, owner)
	if n := len(copyJobs(t, owner)); n != 0 || res.RequeueAfter == 0 {
		t.Fatalf("%d copy jobs, requeue %v while an app pod exists", n, res.RequeueAfter)
	}
	if c := migrating(t, owner); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("Migrating = %+v, want the hold kept", c)
	}

	must(t, k8s.Delete(ctx, pod, client.GracePeriodSeconds(0)))
	reconcileMigration(t, r, owner)
	if n := len(copyJobs(t, owner)); n != 1 {
		t.Fatalf("%d copy jobs after the pod went, want 1", n)
	}
	if got := getHome(t, owner).Status.Migration.NewClaim; got != naming.HomeClaimNameGen(owner, 2) {
		t.Fatalf("new claim = %s, the prepared one must be reused", got)
	}
}

// memberOf is the group membership of owners.
type memberOf map[string][]string

func (m memberOf) Groups(_ context.Context, owner string) ([]string, error) { return m[owner], nil }

// The copy runs as the owner with the owner's folder gids as supplemental
// groups: the copy cannot set a group the process is not a member of.
func TestTheCopyJobCarriesTheOwnersFolderGidsAsSupplementalGroups(t *testing.T) {
	owner := uniqueOwner("quin")
	group := "team-" + owner
	movableHomes(t, owner)
	f := newFolder(t, group, nil)
	f.Status = model.SharedFolderStatus{
		ClaimName: "folder-claim", GID: 2345, Prepared: true,
		Conditions: []metav1.Condition{{Type: model.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Now()}},
	}
	_, err := db.SharedFolders().Update(ctx, *f, f.Version)
	must(t, err)
	r := migrationReconciler(k8s)
	r.Storage.Groups = memberOf{owner: {group}}
	j := copying(t, r, owner)

	if got := j.Spec.Template.Spec.SecurityContext.SupplementalGroups; !slices.Equal(got, []int64{2345}) {
		t.Fatalf("supplemental groups = %v, want [2345]", got)
	}
}

// The claim that holds the files is found from the claims themselves: a home
// whose status is lost (a restore of an older database) lands on the
// switched-to claim, not on the old one kept for the grace period or on a
// new empty one.
func TestARecreatedSwitchedHomeAdoptsTheLiveClaimNotTheRetainedOne(t *testing.T) {
	for name, after := range map[string]time.Duration{"inside the retention": time.Hour, "after the old claim went": 8 * 24 * time.Hour} {
		t.Run(name, func(t *testing.T) {
			owner := uniqueOwner("kim")
			movableHomes(t, owner)
			r := migrationReconciler(k8s)
			j := copying(t, r, owner)
			finishJob(t, j, true, "")
			reconcileMigration(t, r, owner)
			oldName, liveName := naming.HomeClaimNameGen(owner, 1), naming.HomeClaimNameGen(owner, 2)

			clock = clock.Add(after)
			t.Cleanup(func() { clock = clock.Add(-after) })
			if after > 7*24*time.Hour {
				reconcileMigration(t, r, owner)
				if !claimGone(t, oldName) {
					t.Fatal("old claim kept past retain-until")
				}
			}
			editHome(t, homeNS, owner, func(h *model.Home) { h.Status = model.HomeStatus{} })

			reconcileHome(t, homeReconciler(k8s), owner)
			got := getHome(t, owner).Status
			if got.ClaimName != liveName || got.Mode != model.HomeModeShared {
				t.Fatalf("recreated home status = %+v, want claim %s on shared", got, liveName)
			}
			if !claimGone(t, oldName) {
				if c := getClaim(t, oldName); c.Labels[model.LabelLive] != "" {
					t.Error("the retained claim still says it is live")
				}
			}
		})
	}
}

// A new schedule applies even while the previous round's old claim is still
// kept: the home moves again, and the claim kept from the first round goes
// with the second round's retention.
func TestASwitchedHomeMovesAgainWhenAnotherModeIsScheduledDuringRetention(t *testing.T) {
	owner := uniqueOwner("lee")
	movableHomes(t, owner)
	r := migrationReconciler(k8s)
	j := copying(t, r, owner)
	finishJob(t, j, true, "")
	reconcileMigration(t, r, owner)
	if phase(t, owner) != model.MigrationSwitched {
		t.Fatalf("phase = %q, want Switched", phase(t, owner))
	}

	setChoiceMode(t, model.HomeModePerUser)
	scheduleAt(t, clock.Add(-time.Minute))
	bumpAttempt(t)
	gen3 := naming.HomeClaimNameGen(owner, 3)
	reconcileMigration(t, r, owner)
	if m := getHome(t, owner).Status.Migration; m == nil || m.Phase == model.MigrationSwitched || m.NewClaim != gen3 {
		t.Fatalf("migration = %+v, want a new round copying to %s", m, gen3)
	}
	finishJob(t, prepareJobFor(t, owner, gen3), true, "")
	reconcileMigration(t, r, owner)
	jobs := copyJobs(t, owner)
	finishJob(t, jobs[len(jobs)-1], true, "")
	reconcileMigration(t, r, owner)
	if h := getHome(t, owner); h.Status.ClaimName != gen3 || h.Status.Mode != model.HomeModePerUser {
		t.Fatalf("status = %+v, want %s on perUser", h.Status, gen3)
	}

	clock = clock.Add(8 * 24 * time.Hour)
	t.Cleanup(func() { clock = clock.Add(-8 * 24 * time.Hour) })
	reconcileMigration(t, r, owner)
	for _, n := range []string{naming.HomeClaimNameGen(owner, 1), naming.HomeClaimNameGen(owner, 2)} {
		if !claimGone(t, n) {
			t.Errorf("claim %s kept past its retention", n)
		}
	}
	if claimGone(t, gen3) {
		t.Fatal("the live claim was removed")
	}
}

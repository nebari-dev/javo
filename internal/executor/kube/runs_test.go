package kube

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/workload"
)

var nightly = workload.Ref{Namespace: "javo", Name: "alice-nightly-0123456789", ID: "job-1", Owner: "alice"}

func runSpec(ref workload.Ref, slot time.Time) workload.Spec {
	return workload.Spec{
		Ref:   ref,
		Main:  workload.Unit{Name: workload.MainUnit, Image: "example.com/lab:1"},
		Batch: &workload.Batch{Retries: 2, Timeout: 90 * time.Second, FailNow: []int32{65, 137}, Slot: slot},
	}
}

func jobOf(t *testing.T, c client.Client, ref workload.Ref) *batchv1.Job {
	t.Helper()
	job := &batchv1.Job{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, job); err != nil {
		t.Fatal(err)
	}
	return job
}

// finish marks the run's Job complete, as the Job controller does.
func finish(t *testing.T, c client.Client, ref workload.Ref) {
	t.Helper()
	job := jobOf(t, c, ref)
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := c.Status().Update(ctx, job); err != nil {
		t.Fatal(err)
	}
}

func runsOn(objs ...client.Object) (Runs, client.Client) {
	e, c := executorOn(objs...)
	return e.Runs(), c
}

func TestARunIsAJobHeldUntilItIsRecorded(t *testing.T) {
	runs, c := runsOn()
	if err := runs.Start(ctx, runSpec(nightly, time.Time{})); err != nil {
		t.Fatal(err)
	}
	// Starting it again changes nothing.
	if err := runs.Start(ctx, runSpec(nightly, time.Time{})); err != nil {
		t.Fatal(err)
	}
	job := jobOf(t, c, nightly)
	if job.Labels[model.LabelJob] != "job-1" || job.Labels[model.LabelOwner] != "alice" || job.Labels[LabelTrigger] != TriggerManual ||
		job.Annotations[annotationInstance] != "manual" || !slices.Contains(job.Finalizers, FinalizerRecordRun) {
		t.Errorf("job labels %v, annotations %v, finalizers %v", job.Labels, job.Annotations, job.Finalizers)
	}
	spec := job.Spec
	if *spec.BackoffLimit != 2 || *spec.ActiveDeadlineSeconds != 90 || *spec.TTLSecondsAfterFinished != 3600 {
		t.Errorf("retries %d, deadline %d, ttl %d", *spec.BackoffLimit, *spec.ActiveDeadlineSeconds, *spec.TTLSecondsAfterFinished)
	}
	rules := spec.PodFailurePolicy.Rules
	if len(rules) != 2 || rules[0].Action != batchv1.PodFailurePolicyActionIgnore || rules[1].Action != batchv1.PodFailurePolicyActionFailJob ||
		*rules[1].OnExitCodes.ContainerName != podspec.ContainerName || !slices.Equal(rules[1].OnExitCodes.Values, []int32{65, 137}) {
		t.Errorf("pod failure policy %+v", rules)
	}
	if tpl := spec.Template; tpl.Labels[model.LabelJob] != "job-1" || tpl.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("pod template labels %v, restart %s", tpl.Labels, tpl.Spec.RestartPolicy)
	}

	st, err := runs.Observe(ctx, nightly)
	if err != nil || st == nil || st.Run == nil || st.Run.Handle != nightly.Name || !st.Run.Manual || st.Run.Done {
		t.Fatalf("Observe = %+v, %v", st, err)
	}

	// Stopped while going: it ends, and stays to be recorded.
	if err := runs.Stop(ctx, nightly); err != nil {
		t.Fatal(err)
	}
	st, err = runs.Observe(ctx, nightly)
	if err != nil || st == nil || !st.Run.Done || !st.Run.Stopped || st.Run.Finished.IsZero() {
		t.Fatalf("Observe after Stop = %+v, %v", st, err)
	}
	// Stopped again, now that it has ended: let go of.
	if err := runs.Stop(ctx, nightly); err != nil {
		t.Fatal(err)
	}
	if st, err := runs.Observe(ctx, nightly); st != nil || err != nil {
		t.Fatalf("Observe of a run let go of = %+v, %v", st, err)
	}
	if err := runs.Stop(ctx, nightly); err != nil {
		t.Errorf("Stop of a run that is gone: %v", err)
	}
}

func TestAScheduledRunCarriesItsSlot(t *testing.T) {
	runs, c := runsOn()
	slot := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
	if err := runs.Start(ctx, runSpec(nightly, slot)); err != nil {
		t.Fatal(err)
	}
	job := jobOf(t, c, nightly)
	if job.Labels[LabelTrigger] != TriggerSchedule || job.Annotations[annotationInstance] != "" {
		t.Errorf("labels %v, annotations %v", job.Labels, job.Annotations)
	}
	st, err := runs.Observe(ctx, nightly)
	if err != nil || st.Run.Manual || !st.Run.Slot.Equal(slot) {
		t.Fatalf("Observe = %+v, %v", st.Run, err)
	}
}

func TestStopLetsGoOfAFinishedRunAndLeavesItsJob(t *testing.T) {
	runs, c := runsOn()
	if err := runs.Start(ctx, runSpec(nightly, time.Time{})); err != nil {
		t.Fatal(err)
	}
	finish(t, c, nightly)
	for range 2 {
		if err := runs.Stop(ctx, nightly); err != nil {
			t.Fatal(err)
		}
	}
	// The Job stays until its time to live ends, for its pod's log.
	job := jobOf(t, c, nightly)
	if len(job.Finalizers) != 0 || !job.DeletionTimestamp.IsZero() {
		t.Errorf("finalizers %v, deleted at %v", job.Finalizers, job.DeletionTimestamp)
	}
	if st, err := runs.Observe(ctx, nightly); err != nil || st == nil || !st.Run.Done || !st.Run.Succeeded {
		t.Errorf("Observe = %+v, %v", st, err)
	}
}

// A Job something else made for the job (a CronJob of an earlier version)
// is held from the first look.
func TestObserveHoldsARunItDidNotStart(t *testing.T) {
	made := RenderRun(runSpec(nightly, time.Time{}), podspec.Isolation{})
	made.Finalizers = nil
	runs, c := runsOn(made)
	if _, err := runs.Observe(ctx, nightly); err != nil {
		t.Fatal(err)
	}
	if job := jobOf(t, c, nightly); !slices.Contains(job.Finalizers, FinalizerRecordRun) {
		t.Errorf("finalizers %v", job.Finalizers)
	}
}

func TestAnotherJobsRunIsNeverTheRuns(t *testing.T) {
	runs, c := runsOn()
	if err := runs.Start(ctx, runSpec(nightly, time.Time{})); err != nil {
		t.Fatal(err)
	}
	other := nightly
	other.ID = "job-2"
	if _, err := runs.Observe(ctx, other); !errors.Is(err, executor.ErrForeign) {
		t.Errorf("Observe = %v", err)
	}
	if err := runs.Stop(ctx, other); err != nil {
		t.Fatal(err)
	}
	if job := jobOf(t, c, nightly); !job.DeletionTimestamp.IsZero() {
		t.Error("another job's run was stopped")
	}
}

func TestAppsAndRunsAreListedApart(t *testing.T) {
	e, _ := executorOn()
	runs := e.Runs()
	if err := e.Start(ctx, workload.Spec{Ref: sales, Main: workload.Unit{Name: workload.MainUnit, Image: "example.com/lab:1"}}); err != nil {
		t.Fatal(err)
	}
	if err := runs.Start(ctx, runSpec(nightly, time.Time{})); err != nil {
		t.Fatal(err)
	}
	if got, err := runs.List(ctx); err != nil || !sameRefs(got, []workload.Ref{nightly}) {
		t.Errorf("runs = %v, %v", got, err)
	}
	if got, err := e.List(ctx); err != nil || !sameRefs(got, []workload.Ref{sales}) {
		t.Errorf("apps = %v, %v", got, err)
	}
}

// runPod is a pod of the run, as the Job controller makes them.
func runPod(name string, made time.Time, cs ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: nightly.Namespace, Name: name, CreationTimestamp: metav1.NewTime(made),
			Labels: map[string]string{LabelJobName: nightly.Name, model.LabelJob: nightly.ID, model.LabelOwner: nightly.Owner}},
		Status: corev1.PodStatus{ContainerStatuses: cs},
	}
}

func TestRunLogsAreTheNewestAttempts(t *testing.T) {
	running := corev1.ContainerStatus{Name: podspec.ContainerName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	waiting := corev1.ContainerStatus{Name: podspec.ContainerName, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}}
	e, c := executorOn(runPod("first", runT0, running), runPod("second", runT0.Add(time.Minute), running))
	logs := &podLogs{}
	e.PodLogs = logs
	rc, err := e.Runs().Logs(ctx, nightly, executor.LogOptions{Follow: true})
	if err != nil || rc == nil {
		t.Fatalf("Logs = %v, %v", rc, err)
	}
	rc.Close()
	if logs.pod != "second" || logs.opts.Container != podspec.ContainerName || !logs.opts.Follow {
		t.Errorf("read pod %s with %+v", logs.pod, logs.opts)
	}

	// A third attempt that has not started has nothing to read yet.
	if err := c.Create(ctx, runPod("third", runT0.Add(time.Hour), waiting)); err != nil {
		t.Fatal(err)
	}
	if rc, err := e.Runs().Logs(ctx, nightly, executor.LogOptions{}); rc != nil || err != nil {
		t.Errorf("Logs of a run not started = %v, %v", rc, err)
	}
	// Neither has a run with no pod, nor another job's run of that name.
	other := nightly
	other.ID = "job-2"
	for _, ref := range []workload.Ref{other, {Namespace: "javo", Name: "none", ID: "job-1", Owner: "alice"}} {
		if rc, err := e.Runs().Logs(ctx, ref, executor.LogOptions{}); rc != nil || err != nil {
			t.Errorf("Logs of %s = %v, %v", ref.Name, rc, err)
		}
	}
}

func TestARunsBundleIsTheJobsAndGoesWithTheRun(t *testing.T) {
	e, c := executorOn()
	bundles := Secrets{Reader: c, Client: c, Runs: true}
	b := executor.Bundle{Data: map[string][]byte{"PGPASSWORD": []byte("s3cret")}, Meta: map[string]string{model.AnnotationSecrets: "fp"}, Rebuildable: true}
	// Written before the run starts, so its pod never starts without it.
	if err := bundles.Put(ctx, nightly, "run.run-secrets", b); err != nil {
		t.Fatal(err)
	}
	sec := stored(t, c, "run.run-secrets")
	if sec.Labels[model.LabelJob] != "job-1" || sec.Labels[model.LabelApp] != "" || len(sec.OwnerReferences) != 0 {
		t.Errorf("labels %v, owners %v", sec.Labels, sec.OwnerReferences)
	}
	if err := e.Runs().Start(ctx, runSpec(nightly, time.Time{})); err != nil {
		t.Fatal(err)
	}
	if err := bundles.Put(ctx, nightly, "run.run-secrets", b); err != nil {
		t.Fatal(err)
	}
	sec = stored(t, c, "run.run-secrets")
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].Kind != "Job" || sec.OwnerReferences[0].Name != nightly.Name {
		t.Errorf("owners %v", sec.OwnerReferences)
	}
	got, err := bundles.Get(ctx, nightly, "run.run-secrets")
	if err != nil || string(got.Data["PGPASSWORD"]) != "s3cret" || got.Meta[model.AnnotationSecrets] != "fp" {
		t.Errorf("Get = %+v, %v", got, err)
	}

	// An app's Secret of that name, or another job's, is not the job's.
	other := nightly
	other.ID = "job-2"
	if _, err := bundles.Get(ctx, other, "run.run-secrets"); !errors.Is(err, executor.ErrForeign) {
		t.Errorf("another job read it: %v", err)
	}
	if _, err := (Secrets{Reader: c, Client: c}).Get(ctx, nightly, "run.run-secrets"); !errors.Is(err, executor.ErrForeign) {
		t.Errorf("an app read it: %v", err)
	}
}

func TestTidyDropsAJobsSecretsUnderTheirOldNames(t *testing.T) {
	job := workload.Ref{Namespace: "javo", Name: "alice-nightly", ID: "job-1", Owner: "alice"}
	secret := func(name, id string, annotations map[string]string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "javo", Name: name, Annotations: annotations,
			Labels: map[string]string{model.LabelJob: id}}}
	}
	runs, c := runsOn(
		secret("alice-nightly-javo-secrets", "job-1", nil),
		secret("alice-nightly.job-secrets", "job-1", nil),
		secret("alice-nightly-api", "job-1", map[string]string{model.AnnotationTokenID: "tok-1"}),
		secret("alice-nightly.job-api", "job-1", nil),
		secret("alice-other-api", "job-2", nil),
	)
	var revoked []string
	revoke := func(_ context.Context, id string) error { revoked = append(revoked, id); return nil }
	if err := runs.Tidy(ctx, job, revoke); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(revoked, []string{"tok-1"}) {
		t.Errorf("revoked %v", revoked)
	}
	var left corev1.SecretList
	if err := c.List(ctx, &left); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range left.Items {
		names = append(names, s.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"alice-nightly.job-api", "alice-other-api"}) {
		t.Errorf("left %v", names)
	}
}

// An earlier version fired a job's schedule with a CronJob. Retiring it
// stops it firing and removes it without ending a run it started: its
// Jobs and the Secret its runs read lose it as owner first, and the last
// slot it fired is handed over before it goes.
func TestRetireRemovesTheCronJobAndKeepsItsRuns(t *testing.T) {
	job := workload.Ref{Namespace: "javo", Name: "alice-nightly", ID: "job-1", Owner: "alice"}
	made := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	last := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
	labels := map[string]string{model.LabelJob: "job-1", model.LabelOwner: "alice"}
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "javo", Name: "alice-nightly", UID: "cj-uid", Labels: labels, CreationTimestamp: metav1.NewTime(made)},
		Status:     batchv1.CronJobStatus{LastScheduleTime: ptr.To(metav1.NewTime(last))},
	}
	owner := []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: cj.Name, UID: cj.UID, Controller: ptr.To(true)}}
	// The CronJob made this run for a later slot and has not said so yet.
	later := last.Add(24 * time.Hour)
	run := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "javo", Name: "alice-nightly-29384756", Labels: labels, OwnerReferences: owner,
		Annotations: map[string]string{annotationScheduled: later.Format(time.RFC3339)}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "javo", Name: "alice-nightly.job-secrets", Labels: labels, OwnerReferences: owner}}
	runs, c := runsOn(cj, run, secret)

	if got, err := runs.Schedules(ctx); err != nil || !sameRefs(got, []workload.Ref{job}) {
		t.Fatalf("Schedules = %v, %v", got, err)
	}
	var through time.Time
	keep := func(at time.Time) error {
		// Still there, and no longer firing, when the time is handed over.
		got := &batchv1.CronJob{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(cj), got); err != nil || !ptr.Deref(got.Spec.Suspend, false) {
			t.Errorf("the CronJob when its time is kept: suspended %v, %v", got.Spec.Suspend, err)
		}
		through = at
		return nil
	}
	if err := runs.Retire(ctx, job, keep); err != nil {
		t.Fatal(err)
	}
	if !through.Equal(later) {
		t.Errorf("handed over %s, want %s", through, later)
	}
	if !gone(c, &batchv1.CronJob{}, job) {
		t.Error("the CronJob is still there")
	}
	left := jobOf(t, c, workload.Ref{Namespace: "javo", Name: run.Name})
	if len(left.OwnerReferences) != 0 || left.Spec.TTLSecondsAfterFinished == nil {
		t.Errorf("the run's owners %v, time to live %v", left.OwnerReferences, left.Spec.TTLSecondsAfterFinished)
	}
	if sec := stored(t, c, secret.Name); len(sec.OwnerReferences) != 0 {
		t.Errorf("the Secret's owners %v", sec.OwnerReferences)
	}
	if got, err := runs.Schedules(ctx); err != nil || len(got) != 0 {
		t.Errorf("Schedules after = %v, %v", got, err)
	}

	// With no CronJob there is nothing to hand over, and one of another
	// job is left alone.
	called := false
	if err := runs.Retire(ctx, job, func(time.Time) error { called = true; return nil }); err != nil || called {
		t.Errorf("Retire with no CronJob: %v, kept %v", err, called)
	}
	other := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: "javo", Name: "alice-nightly", Labels: map[string]string{model.LabelJob: "job-2"}}}
	if err := c.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := runs.Retire(ctx, job, keep); err != nil || gone(c, &batchv1.CronJob{}, job) {
		t.Errorf("another job's CronJob: %v", err)
	}
}

// A CronJob that never fired hands over when it was made: the schedule
// counts from there.
func TestRetireOfACronJobThatNeverFiredHandsOverWhenItWasMade(t *testing.T) {
	job := workload.Ref{Namespace: "javo", Name: "alice-nightly", ID: "job-1", Owner: "alice"}
	made := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	runs, _ := runsOn(&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: "javo", Name: "alice-nightly",
		Labels: map[string]string{model.LabelJob: "job-1"}, CreationTimestamp: metav1.NewTime(made)}})
	var through time.Time
	if err := runs.Retire(ctx, job, func(at time.Time) error { through = at; return nil }); err != nil || !through.Equal(made) {
		t.Errorf("handed over %s, %v", through, err)
	}
}

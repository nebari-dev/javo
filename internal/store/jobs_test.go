package store_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func TestJobRoundTripAndSpecUpdate(t *testing.T) {
	s := storetest.New(t)
	in := newJob("alice", "nightly")
	in.Spec.Schedule = &model.JobSchedule{Cron: "0 2 * * *", TimeZone: "UTC", Concurrency: "Forbid"}
	in.Spec.Parameters = []model.JobParameter{{Name: "n", Type: "integer", Default: "3"}}
	created, err := s.Jobs().Create(ctx, alice, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Jobs().Get(ctx, created.ID)
	if err != nil || got.Spec.Schedule == nil || got.Spec.Schedule.Cron != "0 2 * * *" || got.Spec.Parameters[0].Default != "3" ||
		got.Spec.History.KeepRuns != 20 || got.Version != 1 || got.CreatedAt.IsZero() {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := s.Jobs().Create(ctx, alice, newJob("alice", "nightly")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("duplicate name: %v", err)
	}
	updated, err := s.Jobs().UpdateSpec(ctx, alice, created.ID, 1, func(j *model.Job) error {
		j.Spec.Schedule.Paused = true
		return nil
	})
	if err != nil || updated.Version != 2 {
		t.Fatalf("UpdateSpec = %+v, %v", updated, err)
	}
	if _, err := s.Jobs().UpdateSpec(ctx, alice, created.ID, 1, func(*model.Job) error { return nil }); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale update = %v", err)
	}
	bad := newJob("alice", "bad")
	bad.Spec.Schedule = &model.JobSchedule{Cron: "not cron", TimeZone: "UTC", Concurrency: "Forbid"}
	var verr *model.ValidationError
	if _, err := s.Jobs().Create(ctx, alice, bad); !errors.As(err, &verr) {
		t.Fatalf("invalid cron: %v", err)
	}
	list, _ := s.Jobs().List(ctx, "alice")
	if len(list) != 1 {
		t.Fatalf("List = %d jobs", len(list))
	}
}

// A job keeps the size its runs use: a named size, or a custom CPU and
// memory, and moves between the two.
func TestJobSizeRoundTrip(t *testing.T) {
	s := storetest.New(t)
	in := newJob("alice", "nightly")
	in.Resources = &model.AppResources{CPU: resource.MustParse("2500m"), Memory: resource.MustParse("6Gi")}
	created, err := s.Jobs().Create(ctx, alice, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Jobs().Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r := got.Resources; got.Size != "" || r == nil || r.CPU.Cmp(resource.MustParse("2.5")) != 0 || r.Memory.Cmp(resource.MustParse("6Gi")) != 0 {
		t.Fatalf("custom amount lost in the round trip: size %q, resources %+v", got.Size, got.Resources)
	}
	moved, err := s.Jobs().UpdateSpec(ctx, alice, got.ID, got.Version, func(j *model.Job) error {
		j.Size, j.Resources = "medium", nil
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Jobs().Get(ctx, moved.ID); got.Size != "medium" || got.Resources != nil {
		t.Fatalf("to medium: size %q, resources %+v", got.Size, got.Resources)
	}
	both := newJob("alice", "both")
	both.Size, both.Resources = "medium", &model.AppResources{CPU: resource.MustParse("1"), Memory: resource.MustParse("1Gi")}
	var verr *model.ValidationError
	if _, err := s.Jobs().Create(ctx, alice, both); !errors.As(err, &verr) {
		t.Fatalf("size and resources both: %v", err)
	}
}

func TestJobSetStatusKeepsSpec(t *testing.T) {
	s := storetest.New(t)
	j := createJob(t, s, "alice", "nightly")
	if _, err := s.Jobs().UpdateSpec(ctx, alice, j.ID, 1, func(j *model.Job) error {
		j.Spec.Kernel = "python4"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next := t0.Add(time.Hour)
	if err := s.Jobs().SetStatus(ctx, j.ID, 1, model.JobStatus{Phase: model.JobPhaseReady, NextRunTimes: []time.Time{next}, LastScheduleTime: t0}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Jobs().Get(ctx, j.ID)
	if got.Spec.Kernel != "python4" || got.Version != 2 || got.Status.Phase != model.JobPhaseReady ||
		got.Status.ObservedVersion != 1 || len(got.Status.NextRunTimes) != 1 || !got.Status.LastScheduleTime.Equal(t0) {
		t.Fatalf("got = %+v", got)
	}
}

func TestRequestRunAndCancel(t *testing.T) {
	s := storetest.New(t)
	j := createJob(t, s, "alice", "nightly")
	run, err := s.Jobs().RequestRun(ctx, alice, j.ID, map[string]string{"n": "5"})
	if err != nil {
		t.Fatal(err)
	}
	if run.ID == "" || run.Trigger != "manual" || run.Outcome != "" || run.RequestedAt.IsZero() || run.Parameters["n"] != "5" {
		t.Fatalf("run = %+v", run)
	}
	if err := s.Jobs().RequestCancel(ctx, alice, run.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Jobs().Run(ctx, run.ID)
	if !got.CancelRequested {
		t.Fatalf("cancel not recorded: %+v", got)
	}
	// The controller reports progress; the cancel request stays.
	got.K8sJob, got.StartTime, got.Outcome = "nightly-abc", t0, model.RunOutcomeRunning
	if _, err := s.Jobs().RecordRun(ctx, got); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Jobs().Run(ctx, run.ID)
	if after.K8sJob != "nightly-abc" || after.Outcome != model.RunOutcomeRunning || !after.CancelRequested {
		t.Fatalf("after = %+v", after)
	}
	if _, err := s.Jobs().RequestRun(ctx, alice, "nope", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("run of a missing job = %v", err)
	}
	if err := s.Jobs().RequestCancel(ctx, alice, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cancel of a missing run = %v", err)
	}
}

func TestRecordRunCreatesScheduledRun(t *testing.T) {
	s := storetest.New(t)
	j := createJob(t, s, "alice", "nightly")
	run, err := s.Jobs().RecordRun(ctx, model.Run{
		JobID: j.ID, Trigger: "schedule", K8sJob: "nightly-1", ScheduledTime: t0, StartTime: t0,
		Outcome: model.RunOutcomeSucceeded, FinishTime: t0.Add(time.Minute), OutputPath: "runs/1", Outputs: []string{"out.html"},
	})
	if err != nil || run.ID == "" {
		t.Fatalf("RecordRun = %+v, %v", run, err)
	}
	got, _ := s.Jobs().Run(ctx, run.ID)
	if got.Outputs[0] != "out.html" || !got.FinishTime.Equal(t0.Add(time.Minute)) || got.Trigger != "schedule" || !got.RequestedAt.IsZero() {
		t.Fatalf("got = %+v", got)
	}
	if _, err := s.Jobs().RecordRun(ctx, model.Run{ID: "nope", JobID: j.ID}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update of a missing run = %v", err)
	}
}

func TestPruneRunsKeepsNewest(t *testing.T) {
	s := storetest.New(t)
	j := createJob(t, s, "alice", "nightly")
	other := createJob(t, s, "alice", "other")
	record := func(job string, outcome string, finished bool) model.Run {
		t.Helper()
		r := model.Run{JobID: job, Trigger: "schedule", Outcome: outcome, StartTime: t0}
		if finished {
			r.FinishTime = t0.Add(time.Minute)
		}
		run, err := s.Jobs().RecordRun(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	var finished []model.Run
	for range 5 {
		finished = append(finished, record(j.ID, model.RunOutcomeSucceeded, true))
	}
	running := record(j.ID, model.RunOutcomeRunning, false)
	otherRun := record(other.ID, model.RunOutcomeSucceeded, true)
	n, err := s.Jobs().PruneRuns(ctx, model.Actor{System: model.SystemController}, j.ID, 2, nil)
	if err != nil || n != 3 {
		t.Fatalf("PruneRuns = %d, %v; want 3", n, err)
	}
	left, _ := s.Jobs().Runs(ctx, j.ID)
	if len(left) != 3 || left[0].ID != running.ID || left[1].ID != finished[4].ID || left[2].ID != finished[3].ID {
		ids := []string{}
		for _, r := range left {
			ids = append(ids, r.ID)
		}
		t.Fatalf("left %v, want the running run and the two newest finished ones", ids)
	}
	if _, err := s.Jobs().Run(ctx, otherRun.ID); err != nil {
		t.Fatalf("another job's run was pruned: %v", err)
	}
}

// A run whose Kubernetes Job still exists stays, even beyond keep: the
// controller would otherwise record that Job again as a new run.
func TestPruneRunsKeepsRunsOfLiveJobs(t *testing.T) {
	s := storetest.New(t)
	j := createJob(t, s, "alice", "nightly")
	for _, k := range []string{"j-1", "j-2", "j-3", "j-4"} {
		if _, err := s.Jobs().RecordRun(ctx, model.Run{JobID: j.ID, Trigger: "schedule", K8sJob: k,
			Outcome: model.RunOutcomeFailed, StartTime: t0, FinishTime: t0.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.Jobs().PruneRuns(ctx, model.Actor{System: model.SystemController}, j.ID, 1, []string{"j-1", "j-3"})
	if err != nil || n != 1 {
		t.Fatalf("PruneRuns = %d, %v; want 1 (only j-2 is neither newest nor live)", n, err)
	}
	left, _ := s.Jobs().Runs(ctx, j.ID)
	var got []string
	for _, r := range left {
		got = append(got, r.K8sJob)
	}
	if !slices.Equal(got, []string{"j-4", "j-3", "j-1"}) {
		t.Fatalf("left %v", got)
	}
}

// "Newest" is by when the run was scheduled or started, not by when its row
// was inserted: a re-recorded old run must not push a newer one out.
func TestPruneRunsOrdersByRunTimeNotInsertOrder(t *testing.T) {
	s := storetest.New(t)
	j := createJob(t, s, "alice", "nightly")
	for i, k := range []string{"new", "old"} { // inserted newest first
		at := t0.Add(-time.Duration(i) * time.Hour)
		if _, err := s.Jobs().RecordRun(ctx, model.Run{JobID: j.ID, Trigger: "schedule", K8sJob: k,
			Outcome: model.RunOutcomeSucceeded, ScheduledTime: at, StartTime: at, FinishTime: at.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.Jobs().PruneRuns(ctx, model.Actor{System: model.SystemController}, j.ID, 1, nil); err != nil || n != 1 {
		t.Fatalf("PruneRuns = %d, %v", n, err)
	}
	left, _ := s.Jobs().Runs(ctx, j.ID)
	if len(left) != 1 || left[0].K8sJob != "new" {
		t.Fatalf("left %+v, want the run scheduled last", left)
	}
}

func TestPurgeJobRemovesRuns(t *testing.T) {
	s := storetest.New(t)
	j := createJob(t, s, "alice", "nightly")
	run, _ := s.Jobs().RequestRun(ctx, alice, j.ID, nil)
	if err := s.Jobs().MarkDeleted(ctx, alice, j.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Jobs().Purge(ctx, alice, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Jobs().Run(ctx, run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("run outlived its job: %v", err)
	}
	all, _ := s.Jobs().ListAll(ctx)
	if len(all) != 0 {
		t.Fatalf("ListAll = %d", len(all))
	}
}

// A resync, a finalizer retry or a reconcile that runs twice records the same
// Kubernetes Job again: it must stay one run.
func TestRecordRunTwiceForTheSameK8sJobKeepsOneRun(t *testing.T) {
	s := storetest.New(t)
	j := createJob(t, s, "alice", "nightly")
	first, err := s.Jobs().RecordRun(ctx, model.Run{
		JobID: j.ID, Trigger: "schedule", K8sJob: "nightly-1", ScheduledTime: t0, StartTime: t0, Outcome: model.RunOutcomeRunning,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Jobs().RecordRun(ctx, model.Run{
		JobID: j.ID, Trigger: "schedule", K8sJob: "nightly-1", ScheduledTime: t0, StartTime: t0,
		FinishTime: t0.Add(time.Minute), Outcome: model.RunOutcomeSucceeded,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Outcome != model.RunOutcomeSucceeded {
		t.Fatalf("second = %+v, want the first run (%s) updated", second, first.ID)
	}
	runs, _ := s.Jobs().Runs(ctx, j.ID)
	if len(runs) != 1 {
		t.Fatalf("%d runs, want 1", len(runs))
	}
	// Another job may reuse the Kubernetes Job name.
	other := createJob(t, s, "alice", "other")
	if _, err := s.Jobs().RecordRun(ctx, model.Run{JobID: other.ID, Trigger: "schedule", K8sJob: "nightly-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestRecordRunAgainKeepsNotifiedTimeOutputsAndTimes(t *testing.T) {
	s := storetest.New(t)
	j := createJob(t, s, "alice", "nightly")
	first, err := s.Jobs().RecordRun(ctx, model.Run{
		JobID: j.ID, Trigger: "schedule", K8sJob: "nightly-1", StartTime: t0,
		FinishTime: t0.Add(time.Minute), Outcome: model.RunOutcomeSucceeded,
		OutputPath: "out/1", Outputs: []string{"a.csv"},
	})
	if err != nil {
		t.Fatal(err)
	}
	notified := t0.Add(2 * time.Minute)
	if err := s.Jobs().MarkRunNotified(ctx, first.ID, notified); err != nil {
		t.Fatal(err)
	}
	// A resync records the same Job again from the Job object alone, by
	// K8sJob and then by ID.
	for _, id := range []string{"", first.ID} {
		got, err := s.Jobs().RecordRun(ctx, model.Run{
			ID: id, JobID: j.ID, Trigger: "schedule", K8sJob: "nightly-1", Outcome: model.RunOutcomeSucceeded,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != first.ID || !got.NotifiedAt.Equal(notified) || len(got.Outputs) != 1 || got.Outputs[0] != "a.csv" ||
			got.OutputPath != "out/1" || !got.StartTime.Equal(t0) || !got.FinishTime.Equal(t0.Add(time.Minute)) {
			t.Fatalf("id %q: got %+v, want notified time, outputs and times kept", id, got)
		}
	}
	if err := s.Jobs().MarkRunNotified(ctx, "nope", notified); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("notify a missing run = %v", err)
	}
}

func TestPruneRunsIsAudited(t *testing.T) {
	s := storetest.New(t)
	j := createJob(t, s, "alice", "nightly")
	for range 3 {
		if _, err := s.Jobs().RecordRun(ctx, model.Run{JobID: j.ID, Trigger: "schedule", Outcome: model.RunOutcomeSucceeded, FinishTime: t0}); err != nil {
			t.Fatal(err)
		}
	}
	before := auditCount(t, s)
	ctl := model.Actor{System: model.SystemController}
	if n, err := s.Jobs().PruneRuns(ctx, ctl, j.ID, 3, nil); err != nil || n != 0 {
		t.Fatalf("PruneRuns(3) = %d, %v", n, err)
	}
	if auditCount(t, s) != before {
		t.Fatal("a prune that deleted nothing left an audit row")
	}
	if n, err := s.Jobs().PruneRuns(ctx, ctl, j.ID, 1, nil); err != nil || n != 2 {
		t.Fatalf("PruneRuns(1) = %d, %v", n, err)
	}
	events, _ := s.Audit().List(ctx, 1)
	if len(events) != 1 || events[0].Action != "job.prune_runs" || events[0].Actor != model.SystemController ||
		events[0].ObjectKind != "job" || events[0].ObjectName != j.Name {
		t.Fatalf("audit = %+v", events)
	}
}

// Two people may each have a job of the same short name, also in one namespace.
func TestTwoOwnersMayUseTheSameJobName(t *testing.T) {
	s := storetest.New(t)
	a := createJob(t, s, "alice", "nightly")
	b := createJob(t, s, "bob", "nightly")
	got, err := s.Jobs().GetByOwnerName(ctx, "bob", "nightly")
	if err != nil || got.ID != b.ID || got.ID == a.ID {
		t.Fatalf("bob's nightly = %+v, %v", got, err)
	}
	if _, err := s.Jobs().GetByOwnerName(ctx, "carol", "nightly"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("carol's nightly = %v, want ErrNotFound", err)
	}
}

func TestUnnotifiedFailedRunsListsOnlyFailuresNotYetTold(t *testing.T) {
	s := storetest.New(t)
	j := createJob(t, s, "alice", "nightly")
	record := func(k8sJob, outcome string) model.Run {
		t.Helper()
		r, err := s.Jobs().RecordRun(ctx, model.Run{JobID: j.ID, Trigger: "schedule", K8sJob: k8sJob, Outcome: outcome, FinishTime: t0})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	failed := record("n-1", model.RunOutcomeFailed)
	timedOut := record("n-2", model.RunOutcomeTimedOut)
	lost := record("n-3", model.RunOutcomeLost)
	record("n-4", model.RunOutcomeSucceeded)
	record("n-5", model.RunOutcomeRunning)
	record("n-6", model.RunOutcomeSkipped)
	told := record("n-7", model.RunOutcomeFailed)
	if err := s.Jobs().MarkRunNotified(ctx, told.ID, t0); err != nil {
		t.Fatal(err)
	}

	got, err := s.Jobs().UnnotifiedFailedRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	want := []string{failed.ID, timedOut.ID, lost.ID}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Fatalf("runs %v, want %v (oldest first)", ids, want)
	}
}

func notifiedFalse(version int64) metav1.Condition {
	return metav1.Condition{Type: model.ConditionNotified, Status: metav1.ConditionFalse, Reason: "SendFailed", Message: "m", ObservedGeneration: version}
}

// The reconciler read the row before the notifier set Notified=False; its
// write must not drop the condition.
func TestSetStatusKeepsStoredNotifiedCondition(t *testing.T) {
	s := storetest.New(t)
	j := createJob(t, s, "alice", "nightly")
	if err := s.Jobs().SetCondition(ctx, j.ID, notifiedFalse(1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Jobs().SetStatus(ctx, j.ID, 1, model.JobStatus{Phase: model.JobPhaseReady}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Jobs().Get(ctx, j.ID)
	c := meta.FindStatusCondition(got.Status.Conditions, model.ConditionNotified)
	if c == nil || c.Status != metav1.ConditionFalse || got.Status.Phase != model.JobPhaseReady {
		t.Fatalf("status = %+v", got.Status)
	}
}

// The notifier read the row before the reconciler wrote a new phase; adding
// the condition must not bring back the old phase or observed version.
func TestSetConditionKeepsNewerPhase(t *testing.T) {
	s := storetest.New(t)
	j := createJob(t, s, "alice", "nightly")
	if err := s.Jobs().SetStatus(ctx, j.ID, 1, model.JobStatus{Phase: model.JobPhaseBlocked}); err != nil {
		t.Fatal(err)
	}
	if err := s.Jobs().SetCondition(ctx, j.ID, notifiedFalse(1)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Jobs().Get(ctx, j.ID)
	if got.Status.Phase != model.JobPhaseBlocked || got.Status.ObservedVersion != 1 ||
		meta.FindStatusCondition(got.Status.Conditions, model.ConditionNotified) == nil {
		t.Fatalf("status = %+v", got.Status)
	}
	if err := s.Jobs().SetCondition(ctx, "nope", notifiedFalse(1)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing job = %v", err)
	}
}

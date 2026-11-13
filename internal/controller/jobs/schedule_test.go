package jobs

import (
	"slices"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/jobs/schedule"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// noon is when the timed tests start: half past, so no slot of an hourly
// schedule is due.
var noon = time.Date(2026, 11, 13, 12, 30, 0, 0, time.UTC)

// hour is the hourly slot at h o'clock UTC that day.
func hour(h int) time.Time { return time.Date(2026, 11, 13, h, 0, 0, 0, time.UTC) }

// timed is one job reconciled by hand at times the test picks: the
// controller is stopped, so nothing happens between two looks.
type timed struct {
	h   *harness
	r   *Reconciler
	job model.Job
	now time.Time
}

// hourly is a job that runs at every full hour UTC under the overlap rule
// concurrency, first looked at at noon.
func hourly(t *testing.T, concurrency string, mutate func(*model.Job)) *timed {
	t.Helper()
	h := newHarness(t)
	h.stop()
	h.template(nil)
	c := &timed{h: h, now: noon}
	c.job = h.definition("hourly", func(j *model.Job) {
		j.Spec.Schedule = &model.JobSchedule{Cron: "0 * * * *", TimeZone: "UTC", Concurrency: concurrency}
		if mutate != nil {
			mutate(j)
		}
	})
	c.r = h.direct()
	c.r.Now = func() time.Time { return c.now }
	c.look(noon)
	return c
}

// look reconciles the job at the time now.
func (c *timed) look(now time.Time) time.Duration {
	c.h.t.Helper()
	c.now = now
	res, err := c.r.Reconcile(bg, c.job.ID)
	if err != nil {
		c.h.t.Fatalf("reconcile at %s: %v", now.Format(time.TimeOnly), err)
	}
	return res
}

// slots are the job's scheduled runs, by slot.
func (c *timed) slots() []model.Run {
	var out []model.Run
	for _, r := range c.h.runs(c.job) {
		if r.Trigger == TriggerSchedule {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b model.Run) int { return a.Slot.Compare(b.Slot) })
	return out
}

// only is the job's one scheduled run.
func (c *timed) only() model.Run {
	c.h.t.Helper()
	runs := c.slots()
	if len(runs) != 1 {
		c.h.t.Fatalf("%d scheduled runs, want 1: %+v", len(runs), runs)
	}
	return runs[0]
}

// k8sJob is the Kubernetes Job of a run that was started.
func (c *timed) k8sJob(run model.Run) *batchv1.Job {
	c.h.t.Helper()
	k := &batchv1.Job{}
	if err := k8s.Get(bg, client.ObjectKey{Namespace: c.h.ns, Name: run.Handle}, k); err != nil {
		c.h.t.Fatalf("the Job of the run for %s: %v", run.Slot.Format(time.TimeOnly), err)
	}
	return k
}

// succeed stands in for the Job controller: it marks the run's Job complete.
func (c *timed) succeed(run model.Run) {
	c.h.t.Helper()
	done := metav1.NewTime(c.now)
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		k := c.k8sJob(run)
		k.Status.StartTime = &done
		k.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: done},
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: done},
		}
		k.Status.CompletionTime = &done
		return k8s.Status().Update(bg, k)
	})
	if err != nil {
		c.h.t.Fatal(err)
	}
}

func outcomes(runs []model.Run) []string {
	out := make([]string, len(runs))
	for i, r := range runs {
		out[i] = r.Slot.Format("15:04") + " " + r.Outcome
	}
	return out
}

// A slot that comes due starts one run, for that slot, however often the
// job is looked at; a schedule starts nothing for slots before it was first
// seen.
func TestADueSlotStartsOneRunForIt(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Forbid", nil)
	if runs := c.slots(); len(runs) != 0 {
		t.Fatalf("runs before any slot was due: %+v", runs)
	}
	c.look(hour(13).Add(-time.Second))
	if runs := c.slots(); len(runs) != 0 {
		t.Fatalf("runs a second before the slot: %+v", runs)
	}
	res := c.look(hour(13))
	c.look(hour(13).Add(time.Second))
	c.look(hour(13).Add(20 * time.Minute))
	run := c.only()
	if !run.Slot.Equal(hour(13)) || !run.ScheduledTime.Equal(hour(13)) || run.Outcome != model.RunOutcomeRunning || run.Handle == "" {
		t.Fatalf("run = %+v", run)
	}
	k := c.k8sJob(run)
	if k.Labels[LabelTrigger] != TriggerSchedule || k.Annotations["batch.kubernetes.io/cronjob-scheduled-timestamp"] != hour(13).Format(time.RFC3339) {
		t.Errorf("the run's Job: labels %v, annotations %v", k.Labels, k.Annotations)
	}
	got := c.h.job(c.job.ID)
	if !got.Status.LastScheduleTime.Equal(hour(13)) || len(got.Status.NextRunTimes) != 3 || !got.Status.NextRunTimes[0].Equal(hour(14)) {
		t.Errorf("status = %+v", got.Status)
	}
	// The job is looked at again when the next slot comes.
	if res <= 0 || res > time.Hour+time.Second {
		t.Errorf("looked at again after %s", res)
	}
}

// Two controllers, or one that lost what it wrote last, decide a slot once.
func TestASlotIsNeverStartedTwice(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Allow", nil)
	before := c.h.job(c.job.ID).Status
	c.look(hour(13).Add(time.Second))
	first := c.only()

	// A second controller that still believes nothing was decided.
	if err := c.h.db.Jobs().SetStatus(bg, c.job.ID, c.job.Version, before); err != nil {
		t.Fatal(err)
	}
	other := c.h.direct()
	other.Now = func() time.Time { return hour(13).Add(2 * time.Second) }
	if _, err := other.Reconcile(bg, c.job.ID); err != nil {
		t.Fatal(err)
	}
	if run := c.only(); run.ID != first.ID || run.Handle != first.Handle {
		t.Fatalf("run = %+v, want %+v", run, first)
	}
	var jobs batchv1.JobList
	if err := k8s.List(bg, &jobs, client.InNamespace(c.h.ns)); err != nil || len(jobs.Items) != 1 {
		t.Fatalf("%d Jobs, %v", len(jobs.Items), err)
	}
}

// A schedule runs at the wall-clock time of its zone, through the days the
// clocks change: exactly the slots the schedule's own preview names.
func TestSlotsFollowTheTimeZoneThroughAClockChange(t *testing.T) {
	t.Parallel()
	const cron, zone = "30 2 * * *", "Europe/Berlin"
	// Clocks go forward on 29 March 2026 (02:30 does not exist that day)
	// and back on 25 October (02:30 comes twice).
	for name, start := range map[string]time.Time{
		"forward": time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		"back":    time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.stop()
			h.template(nil)
			c := &timed{h: h, now: start}
			c.job = h.definition("nightly", func(j *model.Job) {
				j.Spec.Schedule = &model.JobSchedule{Cron: cron, TimeZone: zone, Concurrency: "Allow"}
			})
			c.r = h.direct()
			c.r.Now = func() time.Time { return c.now }
			c.look(start)
			want, err := schedule.Next(cron, zone, start, 4)
			if err != nil {
				t.Fatal(err)
			}
			// Looked at every half hour for the four days.
			for at := start; !at.After(want.Next[3]); at = at.Add(30 * time.Minute) {
				c.look(at)
			}
			var got []time.Time
			for _, r := range c.slots() {
				got = append(got, r.Slot)
			}
			if !slices.EqualFunc(got, want.Next, func(a, b time.Time) bool { return a.Equal(b) }) {
				t.Fatalf("slots started %v, want %v", got, want.Next)
			}
			// 02:30 in Berlin is 01:30 UTC in winter and 00:30 UTC in summer.
			if first, last := got[0].Hour(), got[3].Hour(); first == last {
				t.Fatalf("slots %v did not move with the clock change", got)
			}
		})
	}
}

// Forbid: a slot that comes due while the run before it is still going
// waits, and starts as soon as that run ends. This is what the Kubernetes
// CronJob controller did: it left its last schedule time where it was and
// started the slot at its next look.
func TestForbidStartsTheWaitingSlotWhenTheRunBeforeItEnds(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Forbid", nil)
	c.look(hour(13))
	first := c.only()

	c.look(hour(14))
	runs := c.slots()
	if len(runs) != 2 || runs[1].Outcome != "" || runs[1].Handle != "" {
		t.Fatalf("runs = %v, want the 14:00 slot waiting", outcomes(runs))
	}
	c.succeed(first)
	c.look(hour(14).Add(20 * time.Minute))
	runs = c.slots()
	if len(runs) != 2 || runs[0].Outcome != model.RunOutcomeSucceeded || runs[1].Outcome != model.RunOutcomeRunning || !runs[1].Slot.Equal(hour(14)) {
		t.Fatalf("runs = %v, want the 14:00 slot started late", outcomes(runs))
	}
	if len(c.h.runsWith(c.job, model.RunOutcomeSkipped)) != 0 {
		t.Fatal("a slot that ran was also recorded as skipped")
	}
}

// Forbid: a slot still waiting when the next one comes due never runs, and
// is recorded as Skipped when that is decided; the newer slot waits in its
// place.
func TestForbidSkipsAWaitingSlotWhenTheNextOneComesDue(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Forbid", nil)
	c.look(hour(13))
	first := c.only()
	c.look(hour(14))
	c.look(hour(15))
	c.look(hour(15).Add(time.Minute))
	runs := c.slots()
	if got, want := outcomes(runs), []string{"13:00 Running", "14:00 Skipped", "15:00 "}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
	if skipped := runs[1]; skipped.Reason != string(reasons.RunSkipped) || skipped.Handle != "" || !skipped.ScheduledTime.Equal(hour(14)) || skipped.FinishTime.IsZero() {
		t.Fatalf("skipped = %+v", skipped)
	}
	c.succeed(first)
	c.look(hour(15).Add(2 * time.Minute))
	if got, want := outcomes(c.slots()), []string{"13:00 Succeeded", "14:00 Skipped", "15:00 Running"}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
	var jobs batchv1.JobList
	if err := k8s.List(bg, &jobs, client.InNamespace(c.h.ns)); err != nil || len(jobs.Items) != 2 {
		t.Fatalf("%d Jobs, want 2 (the skipped slot has none): %v", len(jobs.Items), err)
	}
}

// Forbid looks at scheduled runs only, as the CronJob controller did: a
// run started by hand that is still going holds no slot back.
func TestForbidDoesNotWaitForARunStartedByHand(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Forbid", nil)
	if _, err := c.h.db.Jobs().RequestRun(bg, actor, c.job.ID, nil); err != nil {
		t.Fatal(err)
	}
	c.look(noon.Add(time.Minute))
	c.look(hour(13))
	if run := c.only(); run.Outcome != model.RunOutcomeRunning {
		t.Fatalf("the 13:00 slot = %+v", run)
	}
}

// Allow: a slot starts whatever is still going.
func TestAllowStartsASlotBesideTheRunBeforeIt(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Allow", nil)
	c.look(hour(13))
	c.look(hour(14))
	if got, want := outcomes(c.slots()), []string{"13:00 Running", "14:00 Running"}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
}

// Replace: the scheduled run still going is stopped, and recorded as
// cancelled, and the slot starts at once.
func TestReplaceStopsTheRunBeforeItAndStartsTheSlot(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Replace", nil)
	c.look(hour(13))
	first := c.only()
	c.look(hour(14))
	if k := c.k8sJob(first); k.DeletionTimestamp.IsZero() {
		t.Fatal("the run before was not stopped")
	}
	c.look(hour(14).Add(time.Second))
	runs := c.slots()
	if got, want := outcomes(runs), []string{"13:00 Cancelled", "14:00 Running"}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
	if runs[0].Reason != string(reasons.RunCancelled) {
		t.Fatalf("replaced run = %+v", runs[0])
	}
	// Recorded, so the executor let go of it.
	if err := k8s.Get(bg, client.ObjectKey{Namespace: c.h.ns, Name: first.Handle}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the replaced run's Job: %v", err)
	}
}

// A slot the controller comes to later than the starting deadline is not
// run: it is recorded as Missed. One within the deadline starts late.
func TestASlotPastItsStartingDeadlineIsMissed(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Allow", func(j *model.Job) {
		j.Spec.Schedule.StartingDeadline = &metav1.Duration{Duration: 10 * time.Minute}
	})
	// The controller was down from noon until 14:20.
	c.look(hour(14).Add(20 * time.Minute))
	c.look(hour(14).Add(21 * time.Minute))
	missed := c.only()
	if !missed.Slot.Equal(hour(14)) || missed.Outcome != model.RunOutcomeMissed || missed.Reason != string(reasons.RunMissed) ||
		missed.Handle != "" || !missed.ScheduledTime.Equal(hour(14)) || missed.FinishTime.IsZero() {
		t.Fatalf("run = %+v, want the 14:00 slot missed", missed)
	}
	// Down again until 15:05: inside the deadline.
	c.look(hour(15).Add(5 * time.Minute))
	if got, want := outcomes(c.slots()), []string{"14:00 Missed", "15:00 Running"}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
	var jobs batchv1.JobList
	if err := k8s.List(bg, &jobs, client.InNamespace(c.h.ns)); err != nil || len(jobs.Items) != 1 {
		t.Fatalf("%d Jobs, want 1: %v", len(jobs.Items), err)
	}
}

// A slot that waited for the run before it past its starting deadline is
// missed too.
func TestAWaitingSlotPastItsStartingDeadlineIsMissed(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Forbid", func(j *model.Job) {
		j.Spec.Schedule.StartingDeadline = &metav1.Duration{Duration: 10 * time.Minute}
	})
	c.look(hour(13))
	first := c.only()
	c.look(hour(14))
	c.succeed(first)
	c.look(hour(14).Add(11 * time.Minute))
	if got, want := outcomes(c.slots()), []string{"13:00 Succeeded", "14:00 Missed"}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
}

// After the controller was down over several slots it starts the newest
// one only, as the CronJob controller did: the others are gone.
func TestCatchingUpStartsOnlyTheNewestSlot(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Allow", nil)
	c.look(hour(17).Add(10 * time.Minute))
	c.look(hour(17).Add(11 * time.Minute))
	if run := c.only(); !run.Slot.Equal(hour(17)) || run.Outcome != model.RunOutcomeRunning {
		t.Fatalf("run = %+v, want the 17:00 slot only", run)
	}
}

// A paused schedule starts nothing. Resumed, it starts the newest slot
// that passed meanwhile, as a CronJob no longer suspended did.
func TestAPausedScheduleStartsNothingUntilResumed(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Allow", nil)
	pause := func(paused bool) {
		t.Helper()
		if _, err := c.h.db.Jobs().UpdateSpec(bg, actor, c.job.ID, c.h.job(c.job.ID).Version, func(j *model.Job) error {
			j.Spec.Schedule.Paused = paused
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	pause(true)
	c.look(hour(13))
	c.look(hour(14).Add(10 * time.Minute))
	if runs := c.slots(); len(runs) != 0 {
		t.Fatalf("a paused schedule started %v", outcomes(runs))
	}
	if got := c.h.job(c.job.ID); got.Status.Phase != model.JobPhasePaused || len(got.Status.NextRunTimes) != 0 {
		t.Fatalf("status = %+v", got.Status)
	}
	pause(false)
	c.look(hour(14).Add(20 * time.Minute))
	if run := c.only(); !run.Slot.Equal(hour(14)) || run.Outcome != model.RunOutcomeRunning {
		t.Fatalf("run = %+v, want the 14:00 slot", run)
	}
}

// A schedule added to a job starts nothing for the slots before it was
// there, and one taken off and put back counts from when it came back.
func TestAScheduleCountsFromWhenItWasAdded(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Allow", nil)
	set := func(s *model.JobSchedule) {
		t.Helper()
		if _, err := c.h.db.Jobs().UpdateSpec(bg, actor, c.job.ID, c.h.job(c.job.ID).Version, func(j *model.Job) error {
			j.Spec.Schedule = s
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	sched := *c.job.Spec.Schedule
	set(nil)
	c.look(hour(13).Add(10 * time.Minute))
	set(&sched)
	c.look(hour(15).Add(10 * time.Minute))
	c.look(hour(15).Add(11 * time.Minute))
	if runs := c.slots(); len(runs) != 0 {
		t.Fatalf("a schedule added at 15:10 started %v", outcomes(runs))
	}
	c.look(hour(16))
	if run := c.only(); !run.Slot.Equal(hour(16)) {
		t.Fatalf("run = %+v, want the 16:00 slot", run)
	}
}

// At the job's run cap no slot is decided, with any overlap rule; when a
// run ends, the newest slot that passed starts.
func TestAScheduleAtItsRunCapWaits(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Allow", nil)
	c.h.db.SetLimits(store.Limits{RunsPerJob: 1})
	c.look(hour(13))
	first := c.only()
	c.look(hour(14))
	c.look(hour(15))
	if got, want := outcomes(c.slots()), []string{"13:00 Running"}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
	if got := condReason(c.h.job(c.job.ID), model.ConditionScheduled); got != string(reasons.RunLimitReached) {
		t.Fatalf("Scheduled condition says %q", got)
	}
	c.succeed(first)
	c.look(hour(15).Add(10 * time.Minute))
	c.look(hour(15).Add(11 * time.Minute))
	if got, want := outcomes(c.slots()), []string{"13:00 Succeeded", "15:00 Running"}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
	if got := condReason(c.h.job(c.job.ID), model.ConditionScheduled); got != string(reasons.RunLimitReached) {
		// The 15:00 run takes the job to its cap again.
		t.Fatalf("Scheduled condition says %q", got)
	}
}

// A blocked job decides no slot; once it can run again the newest slot
// that passed starts, as when its CronJob was no longer suspended.
func TestABlockedJobStartsNothingUntilItCanRun(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Allow", nil)
	c.h.access.allow.Store(false)
	c.look(hour(13))
	c.look(hour(14))
	if runs := c.slots(); len(runs) != 0 {
		t.Fatalf("a blocked job started %v", outcomes(runs))
	}
	c.h.access.allow.Store(true)
	c.look(hour(14).Add(30 * time.Minute))
	if run := c.only(); !run.Slot.Equal(hour(14)) || run.Outcome != model.RunOutcomeRunning {
		t.Fatalf("run = %+v, want the 14:00 slot", run)
	}
}

// A slot that is waiting can be cancelled like any run not started yet.
func TestAWaitingSlotCanBeCancelled(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Forbid", nil)
	c.look(hour(13))
	c.look(hour(14))
	waiting := c.slots()[1]
	if err := c.h.db.Jobs().RequestCancel(bg, actor, waiting.ID); err != nil {
		t.Fatal(err)
	}
	c.look(hour(14).Add(time.Minute))
	if got, want := outcomes(c.slots()), []string{"13:00 Running", "14:00 Cancelled"}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
}

// A skipped or missed slot that history no longer keeps does not come back.
func TestAPrunedSkipIsNotRecordedAgain(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Forbid", func(j *model.Job) { j.Spec.History.KeepRuns = 1 })
	c.look(hour(13))
	first := c.only()
	c.look(hour(14))
	c.look(hour(15))
	c.succeed(first)
	c.look(hour(15).Add(time.Minute))
	c.succeed(c.slots()[2])
	// History keeps the newest finished run, and the 13:00 one while the
	// executor still has it; the skip goes, and stays gone.
	for i := range 3 {
		c.look(hour(15).Add(time.Duration(2+i) * time.Minute))
	}
	if got, want := outcomes(c.slots()), []string{"13:00 Succeeded", "15:00 Succeeded"}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
}

// tpl is the template the harness's jobs run on.
func (h *harness) tpl() model.Template {
	h.t.Helper()
	tpl, err := h.db.Templates().Get(bg, h.tplName())
	if err != nil {
		h.t.Fatal(err)
	}
	return tpl
}

// oldCronJob is what an earlier version left for the job: the CronJob that
// fired its schedule, last at the slot last.
func (c *timed) oldCronJob(last time.Time) *batchv1.CronJob {
	c.h.t.Helper()
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: c.h.ns, Name: jobName(&c.job), Labels: map[string]string{model.LabelJob: c.job.ID, model.LabelOwner: c.job.Owner}},
		Spec: batchv1.CronJobSpec{Schedule: "0 * * * *", TimeZone: ptr.To("UTC"), JobTemplate: batchv1.JobTemplateSpec{
			Spec: c.h.cronJobsJob(c.job, c.h.tpl(), "template", last).Spec,
		}},
	}
	if err := k8s.Create(bg, cj); err != nil {
		c.h.t.Fatal(err)
	}
	cj.Status.LastScheduleTime = ptr.To(metav1.NewTime(last))
	if err := k8s.Status().Update(bg, cj); err != nil {
		c.h.t.Fatal(err)
	}
	return cj
}

// The first look after an upgrade takes over from the CronJob an earlier
// version fired the schedule with: the CronJob goes, the run it started
// keeps going and is recorded, the slot it fired is not started again, and
// the next slot is the controller's.
func TestUpgradeTakesOverFromTheCronJob(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.stop()
	h.template(nil)
	c := &timed{h: h, now: noon}
	c.job = h.definition("hourly", func(j *model.Job) {
		j.Spec.Schedule = &model.JobSchedule{Cron: "0 * * * *", TimeZone: "UTC", Concurrency: "Forbid"}
	})
	cj := c.oldCronJob(hour(12))
	// The run the CronJob started at 12:00 is still going, and reads the
	// Secret the CronJob owned.
	owner := []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: cj.Name, UID: cj.UID, Controller: ptr.To(true)}}
	inFlight := h.cronJobsJob(c.job, h.tpl(), cj.Name+"-29384880", hour(12))
	inFlight.OwnerReferences = owner
	if err := k8s.Create(bg, inFlight); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: h.ns, Name: cj.Name + ".job-secrets",
		Labels: map[string]string{model.LabelJob: c.job.ID, model.LabelOwner: c.job.Owner}, OwnerReferences: owner}}
	if err := k8s.Create(bg, secret); err != nil {
		t.Fatal(err)
	}

	c.r = h.direct()
	c.r.Now = func() time.Time { return c.now }
	c.look(noon)
	c.look(noon.Add(time.Second))
	if h.cronJobExists(c.job) {
		t.Fatal("the CronJob is still there")
	}
	k := &batchv1.Job{}
	if err := k8s.Get(bg, client.ObjectKeyFromObject(inFlight), k); err != nil || !k.DeletionTimestamp.IsZero() || len(k.OwnerReferences) != 0 {
		t.Fatalf("the run in flight: %v, deleted %v, owners %v", err, k.DeletionTimestamp, k.OwnerReferences)
	}
	if f, _ := h.hasFinalizer(k.Name); !f {
		t.Fatal("the run in flight is not held until it is recorded")
	}
	if h.k8sSecret(secret.Name) == nil {
		t.Fatal("the Secret the run in flight reads is gone")
	}
	run := c.only()
	if run.Handle != inFlight.Name || !run.Slot.Equal(hour(12)) || run.Outcome != model.RunOutcomeRunning {
		t.Fatalf("run = %+v, want the CronJob's 12:00 run adopted", run)
	}

	// 13:00 is the controller's: it waits for the adopted run (Forbid),
	// then starts.
	c.look(hour(13))
	if got, want := outcomes(c.slots()), []string{"12:00 Running", "13:00 "}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
	c.succeed(run)
	c.look(hour(13).Add(time.Minute))
	c.look(hour(13).Add(2 * time.Minute))
	if got, want := outcomes(c.slots()), []string{"12:00 Succeeded", "13:00 Running"}; !slices.Equal(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
	// No run reads the CronJob's Secret any more.
	if h.k8sSecret(secret.Name) != nil {
		t.Fatal("the CronJob's Secret was left behind")
	}
}

// A slot that came due between the CronJob's last start and the upgrade is
// not lost: the controller starts it, as the CronJob would have at its
// next look.
func TestUpgradeStartsTheSlotTheCronJobHadNotFired(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.stop()
	h.template(nil)
	c := &timed{h: h, now: noon}
	c.job = h.definition("hourly", func(j *model.Job) {
		j.Spec.Schedule = &model.JobSchedule{Cron: "0 * * * *", TimeZone: "UTC", Concurrency: "Forbid"}
	})
	c.oldCronJob(hour(11))
	c.r = h.direct()
	c.r.Now = func() time.Time { return c.now }
	c.look(noon)
	c.look(noon.Add(time.Second))
	if run := c.only(); !run.Slot.Equal(hour(12)) || run.Outcome != model.RunOutcomeRunning {
		t.Fatalf("run = %+v, want the 12:00 slot", run)
	}
	if h.cronJobExists(c.job) {
		t.Fatal("the CronJob is still there")
	}
}

// A job deleted before its first look after the upgrade, or whose row went
// while the controller was down, does not leave its CronJob firing.
func TestAnOldCronJobGoesWithItsJob(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.stop()
	h.template(nil)
	deleted := &timed{h: h, job: h.definition("deleted", nil)}
	orphan := &timed{h: h, job: h.definition("orphan", nil)}
	kept := &timed{h: h, job: h.definition("kept", nil)}
	for _, c := range []*timed{deleted, orphan, kept} {
		c.oldCronJob(hour(12))
	}
	if err := h.db.Jobs().MarkDeleted(bg, actor, deleted.job.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{orphan.job.ID} {
		if err := h.db.Jobs().MarkDeleted(bg, actor, id); err != nil {
			t.Fatal(err)
		}
		if err := h.db.Jobs().Purge(bg, actor, id); err != nil {
			t.Fatal(err)
		}
	}
	r := h.sweeper()
	if _, err := r.Reconcile(bg, deleted.job.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Sweep(bg); err != nil {
		t.Fatal(err)
	}
	if h.cronJobExists(deleted.job) || h.cronJobExists(orphan.job) {
		t.Fatalf("CronJobs left: deleted job %v, orphan %v", h.cronJobExists(deleted.job), h.cronJobExists(orphan.job))
	}
	if !h.cronJobExists(kept.job) {
		t.Fatal("the sweep removed the CronJob of a job that has a row")
	}
}

// A controller that stopped between starting a slot's run and recording
// that it had finds the run again at its next look: it stays one run.
func TestARunStartedButNotRecordedStaysOneRun(t *testing.T) {
	t.Parallel()
	c := hourly(t, "Allow", nil)
	c.look(hour(13))
	run := c.only()
	// As the database was just before the start was recorded.
	storetest.Exec(t, c.h.db, "UPDATE job_runs SET handle = '', outcome = '', reason = '' WHERE id = ?", run.ID)
	c.look(hour(13).Add(time.Second))
	c.look(hour(13).Add(2 * time.Second))
	if got := c.only(); got.ID != run.ID || got.Handle != run.Handle || got.Outcome != model.RunOutcomeRunning {
		t.Fatalf("run = %+v, want %+v", got, run)
	}
	if all := c.h.runs(c.job); len(all) != 1 {
		t.Fatalf("%d runs, want 1: %+v", len(all), all)
	}
}

// A schedule taken off a job before its first look after the upgrade does
// not leave the CronJob firing it.
func TestUpgradeRemovesTheCronJobOfAJobWithNoScheduleAnyMore(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.stop()
	h.template(nil)
	c := &timed{h: h, now: noon}
	c.job = h.definition("hourly", nil)
	c.oldCronJob(hour(12))
	if _, err := h.db.Jobs().UpdateSpec(bg, actor, c.job.ID, c.job.Version, func(j *model.Job) error {
		j.Spec.Schedule = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.r = h.direct()
	c.r.Now = func() time.Time { return c.now }
	c.look(noon)
	if h.cronJobExists(c.job) {
		t.Fatal("the CronJob is still there")
	}
}

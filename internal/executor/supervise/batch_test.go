package supervise_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/executor/supervise"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/workload"
)

// run is a run of ref with the steps, that may be tried again retries
// times.
func run(retries int32, steps ...workload.Unit) workload.Spec {
	s := spec(steps...)
	s.Port = 0
	s.Main.Env = []workload.Env{
		{Name: "JAVO_RUN", Value: `{"definition":"report"}`},
		{Name: "JAVO_RUN_ID", RunID: true},
		{Name: "TOKEN", From: &workload.SecretRef{Bundle: "b", Key: "k"}},
	}
	s.Batch = &workload.Batch{Retries: retries, FailNow: []int32{12}}
	return s
}

func done(st *workload.State) bool { return st != nil && st.Run != nil && st.Run.Done }

// A run runs its steps in order and then its main unit, once: when that
// exits with 0 the run is done and succeeded, and says all a record of
// it needs.
func TestRunSucceeds(t *testing.T) {
	e := &engine{messages: map[string]string{"1": `{"reason":"Succeeded"}`}}
	e.plan("first", 0)
	e.plan("second", 0)
	e.plan(workload.MainUnit, 0)
	s := run(2, workload.Unit{Name: "first"}, workload.Unit{Name: "second"})
	s.Batch.Slot = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	before := time.Now()
	sup := start(t, e, s)
	st := eventually(t, sup, "done", done)

	if got, want := e.starts(), []string{"first", "second", workload.MainUnit}; !slices.Equal(got, want) {
		t.Fatalf("started %v, want %v", got, want)
	}
	r := st.Run
	if !r.Succeeded || r.Stopped || r.TimedOut || r.OutOfMemory || r.Attempts != 1 {
		t.Fatalf("run %+v, want one attempt that succeeded", r)
	}
	if r.Handle != ref.Name || r.Manual || !r.Slot.Equal(s.Batch.Slot) {
		t.Fatalf("run %+v, want the handle %s and the slot", r, ref.Name)
	}
	if r.Started.Before(before) || r.Finished.Before(r.Started) || r.Finished.After(time.Now()) {
		t.Fatalf("started %s, finished %s", r.Started, r.Finished)
	}
	if len(r.Env) != 1 || r.Env["JAVO_RUN"] != `{"definition":"report"}` {
		t.Fatalf("variables %v, want the plain one only", r.Env)
	}
	if r.Message != `{"reason":"Succeeded"}` {
		t.Fatalf("message %q", r.Message)
	}
	if got := e.env(workload.MainUnit, workload.EnvTerminationLog); got != "/message/1" {
		t.Fatalf("the main unit was told to leave its message in %q", got)
	}
	if st.Phase != "" || st.Code != "" {
		t.Fatalf("state %+v: a run has no phase", st)
	}
}

// A run a person started has no slot.
func TestRunStartedByHandIsManual(t *testing.T) {
	e := &engine{}
	e.plan(workload.MainUnit, 0)
	st := eventually(t, start(t, e, run(0)), "done", done)
	if !st.Run.Manual || !st.Run.Slot.IsZero() {
		t.Fatalf("run %+v, want a manual one", st.Run)
	}
}

// A main unit that fails is started again, from the first step, until
// its retries are used up. The message is the newest attempt's.
func TestRunIsRetriedUntilItsRetriesAreUsedUp(t *testing.T) {
	e := &engine{messages: map[string]string{"1": "first", "3": "third"}}
	e.plan("first", 0, 0, 0)
	e.plan(workload.MainUnit, 1, 1, 1)
	sup := start(t, e, run(2, workload.Unit{Name: "first"}))
	st := eventually(t, sup, "done", done)
	if r := st.Run; r.Succeeded || r.Attempts != 3 || r.Message != "third" {
		t.Fatalf("run %+v, want three failed attempts and the last one's message", r)
	}
	want := []string{"first", workload.MainUnit, "first", workload.MainUnit, "first", workload.MainUnit}
	if got := e.starts(); !slices.Equal(got, want) {
		t.Fatalf("started %v, want %v", got, want)
	}
}

// A retry that succeeds ends the run well, and a message only an earlier
// attempt left is not the run's.
func TestRunSucceedsOnARetry(t *testing.T) {
	e := &engine{messages: map[string]string{"1": "first"}}
	e.plan(workload.MainUnit, 1, 0)
	st := eventually(t, start(t, e, run(2)), "done", done)
	if r := st.Run; !r.Succeeded || r.Attempts != 2 || r.Message != "" {
		t.Fatalf("run %+v, want success at the second attempt", r)
	}
}

// An exit code a retry cannot fix ends the run at once.
func TestRunFailsNowOnACodeARetryCannotFix(t *testing.T) {
	e := &engine{}
	e.plan(workload.MainUnit, 12)
	sup := start(t, e, run(3))
	st := eventually(t, sup, "done", done)
	if r := st.Run; r.Succeeded || r.Attempts != 1 {
		t.Fatalf("run %+v, want one failed attempt", r)
	}
	if got := e.starts(); len(got) != 1 {
		t.Fatalf("started %v", got)
	}
}

// A step that fails ends the attempt, whatever its exit code: only the
// main unit's code can end the run at once.
func TestRunStepThatFailsEndsTheAttempt(t *testing.T) {
	e := &engine{}
	e.plan("first", 12, 0)
	e.plan(workload.MainUnit, 0)
	sup := start(t, e, run(1, workload.Unit{Name: "first"}))
	st := eventually(t, sup, "done", done)
	if r := st.Run; !r.Succeeded || r.Attempts != 2 {
		t.Fatalf("run %+v, want success at the second attempt", r)
	}
	if got, want := e.starts(), []string{"first", "first", workload.MainUnit}; !slices.Equal(got, want) {
		t.Fatalf("started %v, want %v", got, want)
	}
}

// A main unit that was killed (exit code 137) was out of memory.
func TestRunKilledIsOutOfMemory(t *testing.T) {
	e := &engine{}
	e.plan(workload.MainUnit, 137)
	st := eventually(t, start(t, e, run(0)), "done", done)
	if r := st.Run; r.Succeeded || !r.OutOfMemory {
		t.Fatalf("run %+v, want out of memory", r)
	}
}

// A run is ended at its time limit, which counts from when it was
// started and over every attempt and the waits between them.
func TestRunEndsAtItsTimeLimit(t *testing.T) {
	e := &engine{messages: map[string]string{"2": "interrupted"}}
	e.plan(workload.MainUnit, 1)
	s := run(5)
	s.Batch.Timeout = 150 * time.Millisecond
	sup := start(t, e, s)
	st := eventually(t, sup, "done", done)
	r := st.Run
	if !r.TimedOut || r.Succeeded || r.Stopped || r.Timeout != s.Batch.Timeout || r.Attempts != 2 {
		t.Fatalf("run %+v, want it timed out in its second attempt", r)
	}
	if took := r.Finished.Sub(r.Started); took < s.Batch.Timeout || took > 5*time.Second {
		t.Fatalf("it ended after %s", took)
	}
	if r.Waited != 0 || r.Message != "interrupted" {
		t.Fatalf("run %+v, want no wait for a machine and the message of the attempt that was stopped", r)
	}
	if e.running(workload.MainUnit) != 0 {
		t.Fatal("the main unit runs on")
	}
}

// Stop ends a run that is going, which stays to be observed as stopped;
// Stop on the run that has ended lets go of it.
func TestStopEndsARunThatIsGoingAndThenLetsGoOfIt(t *testing.T) {
	e := &engine{}
	sup := start(t, e, run(3))
	until(t, "the main unit running", func() bool { return e.running(workload.MainUnit) == 1 })
	if st := eventually(t, sup, "going", func(st *workload.State) bool { return st != nil }); st.Run.Done || st.Run.Attempts != 1 {
		t.Fatalf("run %+v, want it going", st.Run)
	}

	if err := sup.Stop(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	st, err := sup.Observe(t.Context(), ref)
	if err != nil || !done(st) {
		t.Fatalf("after Stop: %+v, %v; want the run, done", st, err)
	}
	if r := st.Run; !r.Stopped || r.Succeeded || r.TimedOut || r.Finished.IsZero() || r.Attempts != 1 {
		t.Fatalf("run %+v, want it stopped", r)
	}
	if e.running(workload.MainUnit) != 0 {
		t.Fatal("the main unit runs on")
	}
	if refs, _ := sup.List(t.Context()); len(refs) != 1 {
		t.Fatalf("listed %v, want the run", refs)
	}
	if got := e.let(); len(got) != 0 {
		t.Fatalf("the engine was told to let go of %v while the run is kept", got)
	}

	if err := sup.Stop(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if st, _ := sup.Observe(t.Context(), ref); st != nil {
		t.Fatalf("after the second Stop: %+v", st)
	}
	if got := e.let(); !slices.Equal(got, []string{ref.Name}) {
		t.Fatalf("the engine was told to let go of %v, want the run", got)
	}
	// What the supervisor does not know may still be the engine's.
	if err := sup.Stop(t.Context(), ref); err != nil || len(e.let()) != 2 {
		t.Fatalf("Stop of a run the supervisor does not know: %v, let go of %v", err, e.let())
	}
	if refs, _ := sup.List(t.Context()); len(refs) != 0 {
		t.Fatalf("listed %v", refs)
	}
}

// A run that is done stays as it is, however often it is started again.
func TestStartOfARunThatIsDoneChangesNothing(t *testing.T) {
	e := &engine{}
	e.plan(workload.MainUnit, 0)
	s := run(0)
	sup := start(t, e, s)
	first := eventually(t, sup, "done", done)
	if err := sup.Start(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if st, _ := sup.Observe(t.Context(), ref); !st.Run.Finished.Equal(first.Run.Finished) || len(e.starts()) != 1 {
		t.Fatalf("then %+v, started %v", st.Run, e.starts())
	}
}

// While an image cannot be fetched the run is going and says what it is
// stuck on, and no attempt is used up; it goes on once the image is
// there.
func TestRunStuckOnAnImageSaysSo(t *testing.T) {
	e := &engine{}
	e.plan(workload.MainUnit, 0)
	e.refuses(workload.MainUnit, &supervise.ImageError{Err: errors.New("no such image")})
	sup := start(t, e, run(0))
	st := eventually(t, sup, "stuck", func(st *workload.State) bool { return st != nil && st.Code != "" })
	if st.Run.Done || st.Code != reasons.ImagePullFailed || len(st.Evidence) != 1 {
		t.Fatalf("state %+v, run %+v", st, st.Run)
	}
	e.refuses(workload.MainUnit, nil)
	st = eventually(t, sup, "done", done)
	if r := st.Run; !r.Succeeded || r.Attempts != 1 || st.Code != "" {
		t.Fatalf("state %+v, run %+v", st, r)
	}
}

// When what the units stand on ends, the attempt has failed; while it
// cannot be made the run waits, and the time it waits is told apart when
// the run then times out.
func TestRunWhoseGroundEndsLosesTheAttempt(t *testing.T) {
	e := &engine{}
	s := run(1)
	s.Batch.Timeout = 300 * time.Millisecond
	sup := start(t, grounded{e}, s)
	until(t, "the main unit running", func() bool { return e.running(workload.MainUnit) == 1 })
	e.refuses("ground", errors.New("no room"))
	e.kill("ground", 137)
	st := eventually(t, sup, "stuck in the second attempt", func(st *workload.State) bool { return st != nil && st.Code != "" })
	if st.Run.Done || st.Run.Attempts != 2 || st.Code != reasons.SandboxFailed {
		t.Fatalf("state %+v, run %+v", st, st.Run)
	}
	st = eventually(t, sup, "done", done)
	if r := st.Run; !r.TimedOut || r.Waited <= 0 || r.Waited >= r.Timeout {
		t.Fatalf("run %+v, want it timed out, having waited a part of its limit for the first ground", r)
	}
}

// A run whose engine cannot keep it is refused.
func TestRunOnAnEngineThatKeepsNothingIsRefused(t *testing.T) {
	sup := fast(t, struct{ supervise.Engine }{&engine{}})
	if err := sup.Start(t.Context(), run(0)); err == nil {
		t.Fatal("started")
	}
}

// next is a supervisor that adopts what the one before it left on e.
func next(t *testing.T, e supervise.Engine) *supervise.Supervisor {
	t.Helper()
	sup := fast(t, e)
	if err := sup.Adopt(t.Context()); err != nil {
		t.Fatal(err)
	}
	return sup
}

// A run that is done is found again by the next supervisor, as it ended,
// and stays until it is let go of. Nothing of it is started again.
func TestAdoptFindsARunThatIsDone(t *testing.T) {
	e := &engine{messages: map[string]string{"1": "the message"}}
	e.plan(workload.MainUnit, 3)
	first := start(t, e, run(0))
	before := eventually(t, first, "done", done)
	first.Close()

	e.leave(ref.Name, nil)
	sup := next(t, e)
	st, err := sup.Observe(t.Context(), ref)
	if err != nil || !done(st) {
		t.Fatalf("adopted %+v, %v", st, err)
	}
	if a, b := *st.Run, *before.Run; a.Succeeded || a.Attempts != 1 || a.Message != "the message" ||
		!a.Finished.Equal(b.Finished) || !a.Started.Equal(b.Started) || a.Handle != b.Handle || a.Env["JAVO_RUN"] == "" {
		t.Fatalf("adopted %+v, was %+v", a, b)
	}
	if got := e.starts(); len(got) != 1 {
		t.Fatalf("started %v", got)
	}
	if refs, _ := sup.List(t.Context()); len(refs) != 1 || refs[0] != ref {
		t.Fatalf("listed %v", refs)
	}
	if err := sup.Stop(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if st, _ := sup.Observe(t.Context(), ref); st != nil {
		t.Fatalf("after Stop: %+v", st)
	}
}

// A run whose main unit outlived the supervisor is waited for by the
// next one: the attempt goes on, and is not counted twice.
func TestAdoptWaitsForAMainUnitThatStillRuns(t *testing.T) {
	e := &engine{messages: map[string]string{"1": "the message"}}
	e.plan("first", 0)
	first := start(t, e, run(0, workload.Unit{Name: "first"}))
	until(t, "the main unit running", func() bool { return e.running(workload.MainUnit) == 1 })
	first.Leave()

	e.leave(ref.Name, map[string]supervise.Unit{workload.MainUnit: e.unit(workload.MainUnit)})
	sup := next(t, e)
	if st := eventually(t, sup, "adopted", func(st *workload.State) bool { return st != nil }); st.Run.Done || st.Run.Attempts != 1 {
		t.Fatalf("adopted %+v", st.Run)
	}
	e.kill(workload.MainUnit, 0)
	st := eventually(t, sup, "done", done)
	if r := st.Run; !r.Succeeded || r.Attempts != 1 || r.Message != "the message" {
		t.Fatalf("run %+v", r)
	}
	if got, want := e.starts(), []string{"first", workload.MainUnit}; !slices.Equal(got, want) {
		t.Fatalf("started %v, want %v", got, want)
	}
}

// A run that was going whose units are gone has lost that attempt: it is
// tried again while it has retries left, and has failed when it has none.
func TestAdoptCountsALostAttempt(t *testing.T) {
	for _, c := range []struct {
		retries   int32
		attempts  int
		succeeded bool
		wanted    int
	}{{retries: 1, attempts: 2, succeeded: true, wanted: 2}, {retries: 0, attempts: 1, wanted: 1}} {
		e := &engine{}
		first := start(t, e, run(c.retries))
		until(t, "the main unit running", func() bool { return e.running(workload.MainUnit) == 1 })
		first.Close()
		if st, _ := first.Observe(t.Context(), ref); st != nil {
			t.Fatalf("a closed supervisor still has %+v", st.Run)
		}

		e.plan(workload.MainUnit, 0)
		e.leave(ref.Name, nil)
		st := eventually(t, next(t, e), "done", done)
		if r := st.Run; r.Succeeded != c.succeeded || r.Attempts != c.attempts || r.Stopped {
			t.Fatalf("retries %d: run %+v", c.retries, r)
		}
		if got := e.starts(); len(got) != c.wanted {
			t.Fatalf("retries %d: started %v", c.retries, got)
		}
	}
}

// The time limit counts from when the run was first started, whichever
// supervisor watches it.
func TestAdoptKeepsTheTimeLimitCounting(t *testing.T) {
	e := &engine{}
	s := run(0)
	s.Batch.Timeout = 200 * time.Millisecond
	first := start(t, e, s)
	until(t, "the main unit running", func() bool { return e.running(workload.MainUnit) == 1 })
	began, _ := first.Observe(t.Context(), ref)
	first.Leave()

	e.leave(ref.Name, map[string]supervise.Unit{workload.MainUnit: e.unit(workload.MainUnit)})
	st := eventually(t, next(t, e), "done", done)
	r := st.Run
	if !r.TimedOut || !r.Started.Equal(began.Run.Started) || r.Finished.Sub(r.Started) > 5*time.Second {
		t.Fatalf("run %+v, want it timed out, counted from %s", r, began.Run.Started)
	}
	if e.running(workload.MainUnit) != 0 {
		t.Fatal("the main unit runs on")
	}
}

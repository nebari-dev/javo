package supervise_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/executor/supervise"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/source"
	"github.com/nebari-dev/javo/internal/workload"
)

// A workload's steps run in order, each to its end, before the main unit;
// with no readiness check, running is enough.
func TestStepsRunInOrderBeforeMain(t *testing.T) {
	e := &engine{}
	e.plan("first", 0)
	e.plan("second", 0)
	sup := start(t, e, spec(workload.Unit{Name: "first"}, workload.Unit{Name: "second"}))

	st := eventually(t, sup, "running", running)
	if st.Step != "" || st.Code != reasons.Ready || st.Unanswered != nil {
		t.Fatalf("state %+v, want a plain Running", st)
	}
	if got, want := e.starts(), []string{"first", "second", workload.MainUnit}; !slices.Equal(got, want) {
		t.Fatalf("started %v, want %v", got, want)
	}
}

// There is no state before Start or after Stop, and Stop ends the units.
func TestNoStateBeforeStartOrAfterStop(t *testing.T) {
	e := &engine{}
	sup := start(t, e, spec())
	if st, err := sup.Observe(t.Context(), workload.Ref{Namespace: "alice", Name: "other"}); err != nil || st != nil {
		t.Fatalf("a workload never started: %+v, %v", st, err)
	}
	eventually(t, sup, "running", running)
	if err := sup.Stop(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if st, err := sup.Observe(t.Context(), ref); err != nil || st != nil {
		t.Fatalf("after Stop: %+v, %v", st, err)
	}
	if n := e.running(workload.MainUnit); n != 0 {
		t.Fatalf("%d main units still run after Stop", n)
	}
	if err := sup.Stop(t.Context(), ref); err != nil {
		t.Fatalf("stopping it again: %v", err)
	}
}

// A step that exits with an error fails the workload at that step, with
// the exit code and its last lines as evidence, and the main unit waits.
// The step is run again, and the workload goes on once it ends well.
func TestFailingStepFailsWithEvidenceAndIsRetried(t *testing.T) {
	e := &engine{output: map[string]string{"sample-copy": "copying\ncp: no such folder\n"}}
	e.plan("sample-copy", 3, 3, 3, 0)
	sup := start(t, e, spec(workload.Unit{Name: "sample-copy"}))

	st := eventually(t, sup, "failed", failed)
	if st.Step != model.ConditionStarted || st.Code != reasons.SampleMissing {
		t.Fatalf("failed at %q with %q", st.Step, st.Code)
	}
	want := []string{"unit sample-copy exited with code 3", "copying", "cp: no such folder"}
	if !slices.Equal(st.Evidence, want) {
		t.Fatalf("evidence %q, want %q", st.Evidence, want)
	}
	if slices.Contains(e.starts(), workload.MainUnit) || st.Running {
		t.Fatalf("the main unit started after a failed step (Running %v)", st.Running)
	}
	eventually(t, sup, "running after the step ended well", func(st *workload.State) bool { return running(st) && st.Running })
}

// The steps of an app name the startup step they fail in: the code fetch
// in Built for an app that builds, the environment install in EnvReady.
func TestFailedStepNamesItsStartupStep(t *testing.T) {
	for _, c := range []struct {
		steps []string
		exit  int
		step  string
		code  reasons.Code
	}{
		{[]string{source.BuildContainer}, 1, model.ConditionBuilt, reasons.BuildFailed},
		{[]string{pixienv.InstallContainer}, 1, model.ConditionEnvReady, reasons.EnvironmentInstallFailed},
		{[]string{pixienv.InstallContainer}, pixienv.ExitProgramMissing, model.ConditionEnvReady, reasons.EnvironmentLacksProgram},
	} {
		e := &engine{}
		var steps []workload.Unit
		for _, name := range c.steps {
			steps = append(steps, workload.Unit{Name: name})
			e.plan(name, slices.Repeat([]int{c.exit}, 1000)...)
		}
		sup := start(t, e, spec(steps...))
		st := eventually(t, sup, "failed", failed)
		if st.Step != c.step || st.Code != c.code {
			t.Errorf("%v exiting with %d: failed at %q with %q, want %q with %q", c.steps, c.exit, st.Step, st.Code, c.step, c.code)
		}
		if st.Builds != (c.step == model.ConditionBuilt) || st.InstallsEnv != (c.step == model.ConditionEnvReady) {
			t.Errorf("%v: Builds %v, InstallsEnv %v", c.steps, st.Builds, st.InstallsEnv)
		}
		sup.Close()
	}
}

// A step that stays keeps running beside the main unit, which starts only
// once the step's check has passed.
func TestMainWaitsForAHelpersCheck(t *testing.T) {
	e := &engine{}
	e.plan("helper-check", 1, 1, 1, 0)
	helper := workload.Unit{Name: "helper", Stays: true, Ready: &workload.Check{Exec: []string{"test", "-e", "ready"}, Tries: 2}}
	sup := start(t, e, spec(helper))

	st := eventually(t, sup, "failed after two tries", failed)
	if len(st.Evidence) == 0 || !strings.Contains(st.Evidence[0], "did not pass its check in 2 tries") {
		t.Fatalf("evidence %q", st.Evidence)
	}
	if slices.Contains(e.starts(), workload.MainUnit) {
		t.Fatal("the main unit started before the helper's check passed")
	}
	eventually(t, sup, "running", running)
	if e.running("helper") != 1 {
		t.Fatal("the helper does not run beside the main unit")
	}
	if err := sup.Stop(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if e.running("helper") != 0 {
		t.Fatal("the helper outlived the workload")
	}
}

// A main unit that runs and does not answer its check is Starting and
// Unanswered since it started; it is Running once it answers.
func TestMainIsUnansweredUntilItsCheckPasses(t *testing.T) {
	answer := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		select {
		case <-answer:
			if r.URL.Path == "/health" {
				return
			}
		default:
		}
		rw.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := lis.Addr().String()
	_ = lis.Close()

	for _, c := range []struct {
		check      workload.Check
		addr, want string
		open       func(*engine)
	}{
		{workload.Check{HTTPPath: "/health"}, strings.TrimPrefix(srv.URL, "http://"), "GET :8080/health", func(*engine) { close(answer) }},
		{workload.Check{TCP: true}, closed, "TCP :8080", func(e *engine) {
			e.mu.Lock()
			e.addr = strings.TrimPrefix(srv.URL, "http://")
			e.mu.Unlock()
		}},
	} {
		e := &engine{addr: c.addr}
		s := spec()
		s.Main.Ready = &c.check
		before := time.Now()
		sup := start(t, e, s)
		st := eventually(t, sup, "unanswered", func(st *workload.State) bool { return st.Unanswered != nil })
		if st.Phase != "Starting" || st.Step != model.ConditionReady || st.Code != reasons.Starting || !st.Running {
			t.Fatalf("%s: state %+v", c.want, st)
		}
		if u := st.Unanswered; u.Check != c.want || u.Since.Before(before) || u.Since.After(time.Now()) {
			t.Fatalf("%s: unanswered %+v", c.want, u)
		}
		c.open(e)
		st = eventually(t, sup, "running", running)
		if st.Unanswered != nil {
			t.Fatalf("%s: a running workload is unanswered: %+v", c.want, st.Unanswered)
		}
		sup.Close()
	}
}

// A main unit that crashes fails the workload as a crashed app, with its
// last lines, and is started again.
func TestCrashedMainIsRestarted(t *testing.T) {
	e := &engine{output: map[string]string{workload.MainUnit: "Traceback\nNameError\n"}}
	e.plan(workload.MainUnit, 1)
	var seen atomic.Bool
	sup := supervise.New(supervise.Config{Engine: e, Every: 5 * time.Millisecond, Backoff: 5 * time.Millisecond})
	t.Cleanup(sup.Close)
	go func() {
		_ = sup.Watch(t.Context(), func(r workload.Ref) {
			if st, _ := sup.Observe(context.Background(), r); failed(st) && st.Code == reasons.AppCrashed &&
				slices.Equal(st.Evidence, []string{"unit app exited with code 1", "Traceback", "NameError"}) {
				seen.Store(true)
			}
		})
	}()
	// Watch registers on its own goroutine: start only once it is told of
	// a change.
	for !seen.Load() {
		_ = sup.Stop(t.Context(), ref)
		e.plan(workload.MainUnit, 1)
		if err := sup.Start(t.Context(), spec()); err != nil {
			t.Fatal(err)
		}
		eventually(t, sup, "running again", running)
		if t.Context().Err() != nil {
			t.Fatal("never told of the crash")
		}
	}
	st := eventually(t, sup, "running again", running)
	if got := st.Facts[model.ConditionStarted]; len(got) != 2 || got[1] != "restarts 1" {
		t.Fatalf("facts %q, want the restart counted", got)
	}
}

// Starting a workload that runs changes nothing; List names it, and its
// state carries the fingerprint it was first started from.
func TestStartIsIdempotentAndKeepsTheFingerprint(t *testing.T) {
	e := &engine{}
	s := spec()
	s.Fingerprint = workload.Fingerprint{Run: "r1"}
	sup := start(t, e, s)
	eventually(t, sup, "running", running)
	again := s
	again.Fingerprint = workload.Fingerprint{Run: "r2"}
	if err := sup.Start(t.Context(), again); err != nil {
		t.Fatal(err)
	}
	if got := e.starts(); len(got) != 1 {
		t.Fatalf("started %v, want the main unit once", got)
	}
	if st, _ := sup.Observe(t.Context(), ref); st == nil || st.Fingerprint.Run != "r1" {
		t.Fatalf("state %+v, want the fingerprint it was started from", st)
	}
	if refs, _ := sup.List(t.Context()); !slices.Equal(refs, []workload.Ref{ref}) {
		t.Fatalf("List: %v", refs)
	}
	_ = sup.Stop(t.Context(), ref)
	if refs, _ := sup.List(t.Context()); len(refs) != 0 {
		t.Fatalf("List after Stop: %v", refs)
	}
}

// A unit the engine cannot start fails the workload with the engine's
// reason.
func TestUnitThatDoesNotStartFails(t *testing.T) {
	e := &engine{refuse: map[string]error{workload.MainUnit: errors.New("no such program")}}
	sup := start(t, e, spec())
	st := eventually(t, sup, "failed", failed)
	if want := []string{"unit app did not start: no such program"}; !slices.Equal(st.Evidence, want) || st.Code != reasons.AppCrashed {
		t.Fatalf("%q: evidence %q", st.Code, st.Evidence)
	}
}

// A main unit that ends with exit code 137 was killed, and under a memory
// limit that is the kernel ending it for memory: no platform below says
// so more clearly.
func TestMainKilledIsOutOfMemory(t *testing.T) {
	e := &engine{}
	e.plan(workload.MainUnit, slices.Repeat([]int{137}, 1000)...)
	sup := start(t, e, spec())
	st := eventually(t, sup, "failed", failed)
	if st.Code != reasons.OutOfMemory || st.Step != model.ConditionStarted {
		t.Fatalf("failed at %q with %q, want out of memory", st.Step, st.Code)
	}
}

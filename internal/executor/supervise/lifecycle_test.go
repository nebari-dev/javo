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
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/workload"
)

// helper is a step that stays, with a check that is run in it.
var helper = workload.Unit{Name: "helper", Stays: true, Ready: &workload.Check{Exec: []string{"test", "-e", "ready"}}}

// A helper that dies is started again, and checked again, beside a main
// unit that keeps running: nothing the main unit has is lost with it.
func TestHelperThatDiesIsRestartedAlone(t *testing.T) {
	e := &engine{execs: true}
	sup := start(t, e, spec(helper))
	eventually(t, sup, "running", running)

	e.kill("helper", 2)
	until(t, "the helper running and checked again", func() bool {
		return e.running("helper") == 1 && len(e.checked()) == 2
	})
	eventually(t, sup, "running again", running)
	if got, want := e.starts(), []string{"helper", workload.MainUnit, "helper"}; !slices.Equal(got, want) {
		t.Fatalf("started %v, want %v: only the helper is started again", got, want)
	}
}

// A check with a command runs inside the unit it checks, when the engine's
// units can do that, and not in a unit started beside it.
func TestExecCheckRunsInsideTheUnit(t *testing.T) {
	e := &engine{execs: true, execCodes: []int{1, 1}}
	sup := start(t, e, spec(helper))
	eventually(t, sup, "running", running)
	if got := e.checked(); !slices.Equal(got, []string{"helper", "helper", "helper"}) {
		t.Fatalf("checks ran in %v, want three tries in the helper", got)
	}
	if slices.Contains(e.starts(), "helper-check") {
		t.Fatal("a unit was started for the check")
	}
}

// When what the units stand on ends, every unit is stopped and the
// workload starts over from its first step; while it cannot be started
// the workload has failed, and says that the machine is why.
func TestGroundEndingStartsTheWorkloadOver(t *testing.T) {
	e := &engine{execs: true}
	e.plan("first", 0)
	sup := start(t, grounded{e}, spec(workload.Unit{Name: "first"}, helper))
	eventually(t, sup, "running", running)
	once := []string{"ground", "first", "helper", workload.MainUnit}
	if got := e.starts(); !slices.Equal(got, once) {
		t.Fatalf("started %v, want %v", got, once)
	}

	e.refuses("ground", errors.New("no room"))
	e.plan("first", 0)
	e.kill("ground", 137)
	// It failed when the ground ended, and fails on while there is none.
	st := eventually(t, sup, "failed for want of a ground", func(st *workload.State) bool {
		return failed(st) && len(st.Evidence) == 1 && strings.Contains(st.Evidence[0], "no room")
	})
	if st.Step != model.ConditionStarted || st.Code != reasons.SandboxFailed || st.Running {
		t.Fatalf("state %+v, want the machine's failure", st)
	}
	if e.running("helper") != 0 || e.running(workload.MainUnit) != 0 {
		t.Fatal("units outlived what they stand on")
	}

	e.refuses("ground", nil)
	eventually(t, sup, "running again", running)
	if got := e.starts(); !slices.Equal(got, append(once, once...)) {
		t.Fatalf("started %v, want everything twice", got)
	}

	// Stop ends the units before what they stand on.
	if err := sup.Stop(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if e.running("ground") != 0 {
		t.Fatal("the ground outlived the workload")
	}
}

// An image that cannot be fetched fails the workload at ImageReady.
func TestImageErrorFailsAtImageReady(t *testing.T) {
	e := &engine{}
	e.refuses(workload.MainUnit, &supervise.ImageError{Err: errors.New("pull of example.com/app: not found")})
	sup := start(t, e, spec())
	st := eventually(t, sup, "failed", failed)
	if st.Step != model.ConditionImageReady || st.Code != reasons.ImagePullFailed {
		t.Fatalf("failed at %q with %q", st.Step, st.Code)
	}
	if want := []string{"unit app did not start: pull of example.com/app: not found"}; !slices.Equal(st.Evidence, want) {
		t.Fatalf("evidence %q", st.Evidence)
	}
}

// A check reaches the workload's address through Config.Dial.
func TestChecksConnectThroughDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	real := strings.TrimPrefix(srv.URL, "http://")
	for _, check := range []workload.Check{{HTTPPath: "/"}, {TCP: true}} {
		var asked atomic.Value
		e := &engine{addr: "notes.internal:8080"}
		sup := supervise.New(supervise.Config{Engine: e, Every: 5 * time.Millisecond, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			asked.Store(addr)
			return (&net.Dialer{}).DialContext(ctx, network, real)
		}})
		t.Cleanup(sup.Close)
		s := spec()
		s.Main.Ready = &check
		if err := sup.Start(t.Context(), s); err != nil {
			t.Fatal(err)
		}
		eventually(t, sup, "running", running)
		if asked.Load() != "notes.internal:8080" {
			t.Fatalf("dialled %v", asked.Load())
		}
		sup.Close()
	}
}

// found is a workload of ref that e already runs: its ground, its helper
// and its main unit, started a minute ago.
func found(t *testing.T, e *engine, s workload.Spec) supervise.Found {
	t.Helper()
	f := supervise.Found{Spec: s, Created: time.Now().Add(-time.Hour), MainSince: time.Now().Add(-time.Minute), Units: map[string]supervise.Unit{}}
	var err error
	if f.Ground, err = e.start("ground"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"helper", workload.MainUnit} {
		if f.Units[name], err = e.start(name); err != nil {
			t.Fatal(err)
		}
	}
	e.mu.Lock()
	e.started = nil
	e.mu.Unlock()
	return f
}

// A supervisor adopts what an earlier one left running: it starts nothing
// again, knows the workload's fingerprint and when it started, and from
// then on keeps it as one it started itself.
func TestAdoptKeepsWhatRuns(t *testing.T) {
	e := &engine{execs: true}
	s := spec(workload.Unit{Name: "first"}, helper)
	s.Fingerprint = workload.Fingerprint{Run: "r1"}
	s.Main.Ready = &workload.Check{HTTPPath: "/"}
	left := found(t, e, s)
	e.left = []supervise.Found{left}

	sup := fast(t, grounded{e})
	if err := sup.Adopt(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Nothing answers at the engine's address: the main unit is unanswered
	// since it started, a minute ago.
	st := eventually(t, sup, "unanswered", func(st *workload.State) bool { return st != nil && st.Unanswered != nil })
	if !st.Unanswered.Since.Equal(left.MainSince) || !st.Created.Equal(left.Created) || st.Fingerprint.Run != "r1" || !st.Running {
		t.Fatalf("state %+v, want what was found", st)
	}
	if refs, _ := sup.List(t.Context()); !slices.Equal(refs, []workload.Ref{ref}) {
		t.Fatalf("List: %v", refs)
	}
	if err := sup.Start(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if got := e.starts(); len(got) != 0 {
		t.Fatalf("started %v, want nothing: all of it runs", got)
	}

	// The main unit crashes: it is started again from the Spec found.
	e.kill(workload.MainUnit, 1)
	until(t, "the main unit started again", func() bool { return slices.Equal(e.starts(), []string{workload.MainUnit}) })

	if err := sup.Stop(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ground", "helper", workload.MainUnit} {
		if e.running(name) != 0 {
			t.Fatalf("%s outlived the workload", name)
		}
	}
}

// Leave ends the supervisor and stops nothing: the units run on, for the
// next supervisor to adopt.
func TestLeaveKeepsUnitsRunning(t *testing.T) {
	e := &engine{execs: true}
	sup := supervise.New(supervise.Config{Engine: grounded{e}, Every: 5 * time.Millisecond, Backoff: 5 * time.Millisecond})
	if err := sup.Start(t.Context(), spec(helper)); err != nil {
		t.Fatal(err)
	}
	eventually(t, sup, "running", running)
	sup.Leave()
	for _, name := range []string{"ground", "helper", workload.MainUnit} {
		if e.running(name) != 1 {
			t.Fatalf("%s does not run after Leave", name)
		}
	}
	if st, _ := sup.Observe(t.Context(), ref); st != nil {
		t.Fatalf("a supervisor that left still knows the workload: %+v", st)
	}
}

package supervise_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/supervise"
	"github.com/nebari-dev/javo/internal/workload"
)

// engine is an Engine with no programs behind it. A unit exits at once
// with the next code planned for its name; one with none left runs until
// it is stopped or killed.
type engine struct {
	mu      sync.Mutex
	exits   map[string][]int
	refuse  map[string]error
	output  map[string]string
	addr    string
	started []string
	live    map[string]int
	// units are the units that run now, by name.
	units map[string]*unit
	// execs makes units run their checks inside themselves; ran are the
	// units that did, and execCodes what the next ones exit with.
	execs     bool
	ran       []string
	execCodes []int
	// left is what Adopt finds.
	left []supervise.Found
}

func (e *engine) Start(_ context.Context, _ workload.Spec, u workload.Unit) (supervise.Unit, error) {
	return e.start(u.Name)
}

func (e *engine) start(name string) (supervise.Unit, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.refuse[name]; err != nil {
		return nil, err
	}
	e.started = append(e.started, name)
	r := &unit{e: e, name: name, stop: make(chan struct{}), exit: 143}
	if codes := e.exits[name]; len(codes) > 0 {
		r.code, e.exits[name] = &codes[0], codes[1:]
		return r, nil
	}
	if e.live == nil {
		e.live, e.units = map[string]int{}, map[string]*unit{}
	}
	e.live[name]++
	e.units[name] = r
	if e.execs {
		return &execUnit{r}, nil
	}
	return r, nil
}

func (e *engine) Adopt(context.Context) ([]supervise.Found, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.left, nil
}

func (e *engine) Addr(context.Context, workload.Ref, int32) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.addr == "" {
		return "", errors.New("no address")
	}
	return e.addr, nil
}

func (e *engine) Logs(_ context.Context, _ workload.Ref, o executor.LogOptions) (io.ReadCloser, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return io.NopCloser(strings.NewReader(e.output[o.Unit])), nil
}

// starts is the names of the units started so far, in order.
func (e *engine) starts() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.started)
}

// running is how many units called name run now.
func (e *engine) running(name string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.live[name]
}

// plan makes the next starts of the unit name exit with codes.
func (e *engine) plan(name string, codes ...int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.exits == nil {
		e.exits = map[string][]int{}
	}
	e.exits[name] = codes
}

// refuses makes the engine fail to start the unit name with err; nil
// lets it start again.
func (e *engine) refuses(name string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.refuse == nil {
		e.refuse = map[string]error{}
	}
	e.refuse[name] = err
}

// kill ends the running unit name from outside, with exit code code.
func (e *engine) kill(name string, code int) {
	e.mu.Lock()
	u := e.units[name]
	e.mu.Unlock()
	u.end(code)
}

// checked is the units that ran a check inside themselves, in order.
func (e *engine) checked() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.ran)
}

// grounded is an engine whose units stand on a unit called "ground".
type grounded struct{ *engine }

func (g grounded) Ground(context.Context, workload.Spec) (supervise.Unit, error) {
	return g.start("ground")
}

type unit struct {
	e    *engine
	name string
	code *int
	once sync.Once
	stop chan struct{}
	// exit is what Wait returns once the unit was stopped or killed.
	exit int
}

func (u *unit) Wait() int {
	if u.code != nil {
		return *u.code
	}
	<-u.stop
	return u.exit
}

func (u *unit) Stop() { u.end(143) }

func (u *unit) end(code int) {
	u.once.Do(func() {
		if u.code == nil {
			u.e.mu.Lock()
			u.e.live[u.name]--
			u.e.mu.Unlock()
		}
		u.exit = code
		close(u.stop)
	})
}

// execUnit is a unit that runs a check inside itself.
type execUnit struct{ *unit }

func (u *execUnit) Exec(context.Context, []string) (int, error) {
	u.e.mu.Lock()
	defer u.e.mu.Unlock()
	u.e.ran = append(u.e.ran, u.name)
	if len(u.e.execCodes) > 0 {
		code := u.e.execCodes[0]
		u.e.execCodes = u.e.execCodes[1:]
		return code, nil
	}
	return 0, nil
}

var ref = workload.Ref{Namespace: "alice", Name: "notes", ID: "a-1", Owner: "alice"}

// spec is a workload of ref whose main unit has no readiness check.
func spec(steps ...workload.Unit) workload.Spec {
	return workload.Spec{Ref: ref, Port: 8080, Main: workload.Unit{Name: workload.MainUnit}, Steps: steps}
}

// start makes a supervisor on e that checks and restarts every few
// milliseconds, and stops everything when the test ends.
func start(t *testing.T, e supervise.Engine, s workload.Spec) *supervise.Supervisor {
	t.Helper()
	sup := fast(t, e)
	if err := sup.Start(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	return sup
}

// fast is a supervisor on e that checks and restarts every few
// milliseconds, and stops everything when the test ends.
func fast(t *testing.T, e supervise.Engine) *supervise.Supervisor {
	t.Helper()
	sup := supervise.New(supervise.Config{Engine: e, Every: 5 * time.Millisecond, Backoff: 5 * time.Millisecond})
	t.Cleanup(sup.Close)
	return sup
}

// until waits for ok, which what describes.
func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("never %s", what)
}

// eventually waits until the workload's state passes ok, and returns it.
func eventually(t *testing.T, sup *supervise.Supervisor, what string, ok func(*workload.State) bool) *workload.State {
	t.Helper()
	var st *workload.State
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		if st, err = sup.Observe(t.Context(), ref); err != nil {
			t.Fatal(err)
		}
		if ok(st) {
			return st
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("never %s; last state %+v", what, st)
	return nil
}

func running(st *workload.State) bool { return st != nil && st.Phase == "Running" }
func failed(st *workload.State) bool  { return st != nil && st.Phase == "Failed" }

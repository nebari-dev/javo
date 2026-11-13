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
// it is stopped.
type engine struct {
	mu      sync.Mutex
	exits   map[string][]int
	refuse  map[string]error
	output  map[string]string
	addr    string
	started []string
	live    map[string]int
}

func (e *engine) Start(_ context.Context, _ workload.Spec, u workload.Unit) (supervise.Unit, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.refuse[u.Name]; err != nil {
		return nil, err
	}
	e.started = append(e.started, u.Name)
	r := &unit{e: e, name: u.Name, stop: make(chan struct{})}
	if codes := e.exits[u.Name]; len(codes) > 0 {
		r.code, e.exits[u.Name] = &codes[0], codes[1:]
		return r, nil
	}
	if e.live == nil {
		e.live = map[string]int{}
	}
	e.live[u.Name]++
	return r, nil
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

type unit struct {
	e    *engine
	name string
	code *int
	once sync.Once
	stop chan struct{}
}

func (u *unit) Wait() int {
	if u.code != nil {
		return *u.code
	}
	<-u.stop
	return 143
}

func (u *unit) Stop() {
	u.once.Do(func() {
		if u.code == nil {
			u.e.mu.Lock()
			u.e.live[u.name]--
			u.e.mu.Unlock()
		}
		close(u.stop)
	})
}

var ref = workload.Ref{Namespace: "alice", Name: "notes", ID: "a-1", Owner: "alice"}

// spec is a workload of ref whose main unit has no readiness check.
func spec(steps ...workload.Unit) workload.Spec {
	return workload.Spec{Ref: ref, Port: 8080, Main: workload.Unit{Name: workload.MainUnit}, Steps: steps}
}

// start makes a supervisor on e that checks and restarts every few
// milliseconds, and stops everything when the test ends.
func start(t *testing.T, e *engine, s workload.Spec) *supervise.Supervisor {
	t.Helper()
	sup := supervise.New(supervise.Config{Engine: e, Every: 5 * time.Millisecond, Backoff: 5 * time.Millisecond})
	t.Cleanup(sup.Close)
	if err := sup.Start(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	return sup
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

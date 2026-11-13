// Package supervise runs workloads where nothing else does it: Docker and
// local processes have no kubelet, so what a kubelet does for a Pod is
// done here, once, for both. An Engine starts and stops single units; the
// Supervisor runs a workload's steps in order, keeps its helpers and its
// main unit running, checks readiness, restarts what exits after a growing
// wait, and says what state the workload is in.
//
// What it knows is kept in memory. An engine whose units outlive Javo
// (containers) says what still runs, and the next Supervisor adopts it
// (Adopt) after the last one left (Leave).
package supervise

import (
	"context"
	"io"
	"maps"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/workload"
)

// Engine runs single units: a container, a process.
type Engine interface {
	// Start starts u, a unit of s or the check of one, and returns once it
	// runs. Its error is shown to people: it never holds a secret value.
	// An image that could not be fetched is an *ImageError.
	Start(ctx context.Context, s workload.Spec, u workload.Unit) (Unit, error)
	// Adopt finds the workloads an earlier Supervisor left running on the
	// engine (Leave), for this one to keep. What it finds half started it
	// ends, so that it is started again whole. An engine whose units end
	// with Javo has none.
	Adopt(ctx context.Context) ([]Found, error)
	// Addr is the host:port that reaches the workload's port.
	Addr(ctx context.Context, ref workload.Ref, port int32) (string, error)
	// Logs streams what a unit of the workload printed.
	Logs(ctx context.Context, ref workload.Ref, o executor.LogOptions) (io.ReadCloser, error)
}

// Unit is one started unit.
type Unit interface {
	// Wait returns the unit's exit code once it has exited.
	Wait() int
	// Stop ends the unit, by force when it does not go by itself, and
	// returns once it has exited. Stopping one that has exited does nothing.
	Stop()
}

// Grounded is an Engine whose units stand on something they share and
// cannot outlive: on Docker, the container that owns the workload's
// network namespace.
type Grounded interface {
	// Ground starts it for s and returns once it is there. When it ends,
	// every unit of s is stopped and the workload starts over from its
	// first step. Its error is shown to people, as Start's is.
	Ground(ctx context.Context, s workload.Spec) (Unit, error)
}

// Execer is a started Unit that can run a command inside itself. A check
// with a command (workload.Check.Exec) runs there, where it sees what
// the unit sees; a Unit that is no Execer has the check run as a unit of
// its own beside it.
type Execer interface {
	// Exec runs command to its end and returns its exit code.
	Exec(ctx context.Context, command []string) (int, error)
}

// ImageError is Start's error for a unit whose image could not be
// fetched: the workload then fails at its ImageReady step.
type ImageError struct{ Err error }

func (e *ImageError) Error() string { return e.Err.Error() }
func (e *ImageError) Unwrap() error { return e.Err }

// Found is a workload an earlier Supervisor started that still runs.
type Found struct {
	// Spec is what it was started from.
	Spec workload.Spec
	// Created is when it was made, and MainSince when its main unit
	// started.
	Created, MainSince time.Time
	// Ground is what its units stand on; nil for an Engine that is not
	// Grounded.
	Ground Unit
	// Units are its main unit and its steps that stay, by name. All of
	// them run: its other steps have ended well.
	Units map[string]Unit
}

// Config is what a Supervisor is made of.
type Config struct {
	Engine Engine
	// Every is how long to wait between two tries of a readiness check;
	// zero is a second.
	Every time.Duration
	// Backoff is how long to wait before starting a unit that exited again.
	// It doubles each time, up to five minutes; zero is a second.
	Backoff time.Duration
	// Dial is how a readiness check connects to a workload's address; nil
	// connects from this process.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// maxBackoff is the longest wait before a restart. A unit that ran longer
// than this before it exited is restarted as if for the first time.
const maxBackoff = 5 * time.Minute

// Supervisor runs workloads on an Engine. It is an executor.Executor.
type Supervisor struct {
	eng            Engine
	every, backoff time.Duration
	// probe never follows a redirect (it counts as an answer) and never
	// goes through a proxy: the address is the workload's own.
	probe *http.Client
	dial  func(ctx context.Context, network, addr string) (net.Conn, error)
	// leaving is set by Leave: units are then left running.
	leaving atomic.Bool

	mu       sync.Mutex
	work     map[string]*work
	watchers map[int]func(workload.Ref)
	nextID   int
}

var _ executor.Executor = (*Supervisor)(nil)

// New makes a Supervisor that runs nothing yet.
func New(c Config) *Supervisor {
	dial := c.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	s := &Supervisor{
		eng: c.Engine, every: c.Every, backoff: c.Backoff, dial: dial,
		probe: &http.Client{
			Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: dial},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		work:     map[string]*work{},
		watchers: map[int]func(workload.Ref){},
	}
	if s.every <= 0 {
		s.every = time.Second
	}
	if s.backoff <= 0 {
		s.backoff = time.Second
	}
	return s
}

func key(ref workload.Ref) string { return ref.Namespace + "/" + ref.Name }

// Start begins running sp and returns at once; Observe says how far it is.
func (s *Supervisor) Start(_ context.Context, sp workload.Spec) error {
	s.begin(sp, time.Now(), nil)
	return nil
}

// begin runs sp, taking over what was found of it, unless it runs here
// already.
func (s *Supervisor) begin(sp workload.Spec, created time.Time, found *Found) {
	s.mu.Lock()
	if _, ok := s.work[key(sp.Ref)]; ok {
		s.mu.Unlock()
		return
	}
	// The workload outlives the call that started it: Stop and Close end it.
	ctx, cancel := context.WithCancel(context.Background())
	w := &work{
		spec: sp, created: created, cancel: cancel, done: make(chan struct{}),
		failed: map[string]*failure{}, took: map[string]time.Duration{},
	}
	s.work[key(sp.Ref)] = w
	s.mu.Unlock()
	go s.run(ctx, w, found)
	s.changed(sp.Ref)
}

// Adopt takes over the workloads an earlier Supervisor left running on
// the engine: none of their units is started again. Call it before the
// first Start.
func (s *Supervisor) Adopt(ctx context.Context) error {
	all, err := s.eng.Adopt(ctx)
	if err != nil {
		return err
	}
	for _, f := range all {
		s.begin(f.Spec, f.Created, &f)
	}
	return nil
}

// Stop ends the workload's units and forgets it.
func (s *Supervisor) Stop(ctx context.Context, ref workload.Ref) error {
	s.mu.Lock()
	w := s.work[key(ref)]
	s.mu.Unlock()
	if w == nil {
		return nil
	}
	w.cancel()
	select {
	case <-w.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	if s.work[key(ref)] == w {
		delete(s.work, key(ref))
	}
	s.mu.Unlock()
	s.changed(ref)
	return nil
}

// Close stops every workload. Nothing an Engine started outlives it.
func (s *Supervisor) Close() { s.end() }

// Leave forgets every workload and stops none of them: their units run
// on, for the next Supervisor on the engine to adopt. The Supervisor
// cannot be used after it.
func (s *Supervisor) Leave() {
	s.leaving.Store(true)
	s.end()
}

func (s *Supervisor) end() {
	s.mu.Lock()
	all := slices.Collect(maps.Values(s.work))
	s.mu.Unlock()
	for _, w := range all {
		w.cancel()
	}
	for _, w := range all {
		<-w.done
	}
	s.mu.Lock()
	clear(s.work)
	s.mu.Unlock()
}

// Observe is the workload's state, or nil when it was not started here.
func (s *Supervisor) Observe(_ context.Context, ref workload.Ref) (*workload.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.work[key(ref)]
	if w == nil {
		return nil, nil
	}
	return w.state(), nil
}

// List names the workloads started and not stopped.
func (s *Supervisor) List(context.Context) ([]workload.Ref, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []workload.Ref
	for _, w := range s.work {
		out = append(out, w.spec.Ref)
	}
	return out, nil
}

// Watch calls changed each time a workload's state changes, until ctx
// ends. changed runs on the supervisor's own goroutines: it must not wait.
func (s *Supervisor) Watch(ctx context.Context, changed func(workload.Ref)) error {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.watchers[id] = changed
	s.mu.Unlock()
	<-ctx.Done()
	s.mu.Lock()
	delete(s.watchers, id)
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) Addr(ctx context.Context, ref workload.Ref, port int32) (string, error) {
	return s.eng.Addr(ctx, ref, port)
}

func (s *Supervisor) Logs(ctx context.Context, ref workload.Ref, o executor.LogOptions) (io.ReadCloser, error) {
	return s.eng.Logs(ctx, ref, o)
}

// update changes w under the lock, then tells the watchers.
func (s *Supervisor) update(w *work, change func()) {
	s.mu.Lock()
	change()
	s.mu.Unlock()
	s.changed(w.spec.Ref)
}

func (s *Supervisor) changed(ref workload.Ref) {
	s.mu.Lock()
	fns := slices.Collect(maps.Values(s.watchers))
	s.mu.Unlock()
	for _, fn := range fns {
		fn(ref)
	}
}

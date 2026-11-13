// Package supervise runs workloads where nothing else does it: Docker and
// local processes have no kubelet, so what a kubelet does for a Pod is
// done here, once, for both. An Engine starts and stops single units; the
// Supervisor runs a workload's steps in order, keeps its helpers and its
// main unit running, checks readiness, restarts what exits after a growing
// wait, and says what state the workload is in.
//
// What it knows is kept in memory: a workload it started is gone when it
// is closed.
package supervise

import (
	"context"
	"io"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/workload"
)

// Engine runs single units: a container, a process.
type Engine interface {
	// Start starts u, a unit of s or the check of one, and returns once it
	// runs. Its error is shown to people: it never holds a secret value.
	Start(ctx context.Context, s workload.Spec, u workload.Unit) (Unit, error)
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

// Config is what a Supervisor is made of.
type Config struct {
	Engine Engine
	// Every is how long to wait between two tries of a readiness check;
	// zero is a second.
	Every time.Duration
	// Backoff is how long to wait before starting a unit that exited again.
	// It doubles each time, up to five minutes; zero is a second.
	Backoff time.Duration
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

	mu       sync.Mutex
	work     map[string]*work
	watchers map[int]func(workload.Ref)
	nextID   int
}

var _ executor.Executor = (*Supervisor)(nil)

// New makes a Supervisor that runs nothing yet.
func New(c Config) *Supervisor {
	s := &Supervisor{
		eng: c.Engine, every: c.Every, backoff: c.Backoff,
		probe: &http.Client{
			Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
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
	s.mu.Lock()
	if _, ok := s.work[key(sp.Ref)]; ok {
		s.mu.Unlock()
		return nil
	}
	// The workload outlives the call that started it: Stop and Close end it.
	ctx, cancel := context.WithCancel(context.Background())
	w := &work{
		spec: sp, created: time.Now(), cancel: cancel, done: make(chan struct{}),
		failed: map[string]*failure{}, took: map[string]time.Duration{},
	}
	s.work[key(sp.Ref)] = w
	s.mu.Unlock()
	go s.run(ctx, w)
	s.changed(sp.Ref)
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
func (s *Supervisor) Close() {
	s.mu.Lock()
	all := slices.Collect(maps.Values(s.work))
	s.mu.Unlock()
	for _, w := range all {
		w.cancel()
	}
	for _, w := range all {
		<-w.done
	}
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

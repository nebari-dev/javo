package supervise

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/workload"
)

// record is what is known of a run, and what the engine keeps of it
// (Keeper), as JSON. A Spec holds no secret value, so neither does it.
type record struct {
	Spec workload.Spec
	// Started is when Start took the run: its time limit counts from
	// here, as a Kubernetes Job's active deadline counts from the Job's
	// start time, over every attempt and the waits between them. Placed
	// is when its first attempt had something to stand on.
	Started, Placed time.Time
	// Attempts is how many attempts were begun.
	Attempts int

	Done, Succeeded, Stopped, TimedOut bool
	Finished                           time.Time
	// OutOfMemory, Message and Why are about the newest attempt that
	// ended: Why is how it failed, in one line.
	OutOfMemory bool
	Message     string
	Why         string
}

// keepTime is how long keeping a record or reading a message may take
// once the run itself is over.
const keepTime = 30 * time.Second

// MaxMessage is the most of a run's message an engine reads, as much as
// Kubernetes keeps of a termination message.
const MaxMessage = 4096

// RunID is the value an engine gives a unit of s for a variable that
// asks for the id of the run (workload.Env.RunID): a run's handle, the
// same for every attempt, as on Kubernetes; for an app, a new one each
// time.
func RunID(s workload.Spec) string {
	if s.Batch != nil {
		return s.Ref.Name
	}
	return strings.ToLower(rand.Text())
}

// attempted is how one attempt of a run ended.
type attempted struct {
	// failure is nil when the main unit exited with 0; main is set when
	// it is the main unit's.
	failure *failure
	main    bool
	message string
}

// batch runs the run w to its end: attempt after attempt until one
// succeeds, the main unit exits with a code no retry can fix, the retries
// are used up, the time limit passes or Stop ends it. found is what an
// earlier supervisor left of it.
func (s *Supervisor) batch(ctx context.Context, w *work, found *Found) {
	defer close(w.done)
	if w.run.Done {
		return
	}
	b := w.spec.Batch
	limited := ctx
	if b.Timeout > 0 {
		var cancel context.CancelFunc
		limited, cancel = context.WithDeadline(ctx, w.run.Started.Add(b.Timeout))
		defer cancel()
	}
	// The newest attempt is taken over when its main unit is still
	// there. Anything less of it is ended: that attempt is lost.
	lost := false
	if found != nil && found.Units[w.spec.Main.Name] == nil {
		for _, u := range found.Units {
			u.Stop()
		}
		if found.Ground != nil {
			found.Ground.Stop()
		}
		found, lost = nil, w.run.Attempts > 0
	}

	// end records how the run ended.
	end := func(how func(*record)) {
		s.update(w, func() {
			how(w.run)
			w.run.Done, w.run.Finished, w.stuck = true, time.Now(), nil
		})
		s.save(w)
	}
	// cut reports whether the run was ended from outside, and records
	// how. A supervisor that closes or leaves records nothing: the next
	// one finds the run as it was last kept.
	cut := func() bool {
		s.mu.Lock()
		stopped := w.stopped
		s.mu.Unlock()
		switch {
		case stopped:
			end(func(r *record) { r.Stopped = true })
		case ctx.Err() != nil:
		case limited.Err() != nil:
			end(func(r *record) { r.TimedOut = true })
		default:
			return false
		}
		return true
	}

	for retry := 0; ; retry++ {
		var a attempted
		switch {
		case lost:
			a, lost = attempted{failure: &failure{what: "the attempt ended while nothing watched it", exit: -1}}, false
		case found != nil:
			a = s.attempt(limited, w, found)
		default:
			s.update(w, func() { w.run.Attempts++ })
			s.save(w)
			a = s.attempt(limited, w, nil)
		}
		found = nil
		f := a.failure
		s.update(w, func() {
			w.run.Message, w.run.Why = a.message, ""
			if f != nil {
				w.run.Why = f.what
			}
		})
		if f == nil {
			// It ended well before anything could end it.
			end(func(r *record) { r.Succeeded = true })
			return
		}
		if cut() {
			return
		}
		s.mu.Lock()
		last := w.run.Attempts > int(b.Retries)
		s.mu.Unlock()
		if last || (a.main && slices.Contains(b.FailNow, int32(f.exit))) {
			end(func(r *record) { r.OutOfMemory = a.main && f.exit == killed })
			return
		}
		s.save(w)
		if !sleep(limited, s.delay(retry)) && cut() {
			return
		}
	}
}

// save has the engine keep the run as it is now. An engine that cannot
// is not waited for: the run goes on, and is known here.
func (s *Supervisor) save(w *work) {
	s.mu.Lock()
	raw, err := json.Marshal(w.run)
	s.mu.Unlock()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), keepTime)
	defer cancel()
	_ = s.eng.(Keeper).Keep(ctx, w.spec, raw)
}

// attempt runs the run w once: what its units stand on, its steps in
// order and its main unit, each to its end; the steps that stay run
// beside the ones after them. found, unless nil, is an attempt an earlier
// supervisor began, whose main unit is waited for. Everything of the
// attempt has ended when it returns, unless the supervisor leaves.
func (s *Supervisor) attempt(ctx context.Context, w *work, found *Found) (a attempted) {
	interrupted := attempted{failure: &failure{what: "the attempt was ended", exit: -1}}
	var ground Unit
	if found != nil {
		ground = found.Ground
	} else if g, ok := s.eng.(Grounded); ok {
		// Like a Pod no machine has room for: the run waits, and no
		// attempt is used up.
		for try := 0; ground == nil; try++ {
			var err error
			if ground, err = g.Ground(ctx, w.spec); err == nil {
				break
			}
			ground = nil
			s.update(w, func() {
				w.stuck = &failure{what: fmt.Sprintf("what the units run on did not start: %v", err), exit: -1}
			})
			if !sleep(ctx, s.delay(try)) {
				return interrupted
			}
		}
	}
	s.update(w, func() {
		if w.stuck = nil; w.run.Placed.IsZero() {
			w.run.Placed = time.Now()
		}
	})

	units, stop := context.WithCancel(ctx)
	var kept sync.WaitGroup
	if ground != nil {
		go func() {
			ground.Wait()
			stop()
		}()
	}
	// The units end before what they stand on.
	defer func() {
		stop()
		kept.Wait()
		if ground != nil && !s.leaving.Load() {
			ground.Stop()
		}
	}()

	for _, u := range w.spec.Steps {
		switch {
		case !u.Stays && found != nil:
			// An attempt whose main unit runs has its steps behind it.
		case !u.Stays:
			if f := s.once(units, w, u, nil); f != nil {
				return attempted{failure: f}
			}
		default:
			if !s.beside(units, w, u, found, &kept) {
				return interrupted
			}
		}
	}

	main := w.spec.Main
	s.mu.Lock()
	file := strconv.Itoa(w.run.Attempts)
	s.mu.Unlock()
	k := s.eng.(Keeper)
	main.SetEnv(workload.Env{Name: workload.EnvTerminationLog, Value: path.Join(k.MessageDir(w.spec), file)})
	var first Unit
	if found != nil {
		first = found.Units[main.Name]
	}
	a = attempted{failure: s.once(units, w, main, first), main: true}
	if s.leaving.Load() {
		return a
	}
	// The unit may have been stopped: it leaves its message all the same.
	reading, cancel := context.WithTimeout(context.WithoutCancel(ctx), keepTime)
	defer cancel()
	a.message = k.Message(reading, w.spec, file)
	return a
}

// beside starts the staying step u of w, or takes over the one found
// running, and keeps it running, in kept, until ctx ends. It returns once
// the step runs and its check has passed, which is when the next unit
// starts; false when ctx ended first. The check is tried again each time
// the step is started again.
func (s *Supervisor) beside(ctx context.Context, w *work, u workload.Unit, found *Found, kept *sync.WaitGroup) bool {
	var first Unit
	if found != nil {
		first = found.Units[u.Name]
	}
	passed := make(chan struct{}, 1)
	kept.Go(func() {
		s.keep(ctx, w, u, first, func(running context.Context, r Unit) {
			if s.answers(running, w, u, r) {
				select {
				case passed <- struct{}{}:
				default:
				}
			}
		})
	})
	select {
	case <-passed:
		return true
	case <-ctx.Done():
		return false
	}
}

// once runs the unit u of the run w to its end; first, unless nil, is u
// already running. It returns how u failed, or nil when it exited with 0.
// While u's image cannot be fetched the run is stuck, and the fetch is
// tried again after a growing wait: as a Pod waits for its image, no
// attempt is used up.
func (s *Supervisor) once(ctx context.Context, w *work, u workload.Unit, first Unit) *failure {
	r := first
	for try := 0; r == nil; try++ {
		var err error
		if r, err = s.eng.Start(ctx, w.spec, u); err == nil {
			break
		}
		r = nil
		f := s.failureOf(ctx, w, u, 0, err)
		if !f.image || ctx.Err() != nil {
			return f
		}
		s.update(w, func() { w.stuck = f })
		if !sleep(ctx, s.delay(try)) {
			return f
		}
	}
	s.update(w, func() { w.stuck = nil })
	code, ok := s.wait(ctx, r)
	if !ok {
		return &failure{what: fmt.Sprintf("unit %s was ended", u.Name), exit: -1}
	}
	return s.failureOf(ctx, w, u, code, nil)
}

// runState is what the run w is doing, in the shape the Kubernetes
// executor reports a Job in (kube.ObserveRun), so one rule records both.
func (w *work) runState() *workload.State {
	r, b := w.run, w.spec.Batch
	run := &workload.RunState{
		Handle: w.spec.Ref.Name, Manual: b.Slot.IsZero(), Slot: b.Slot,
		Started: r.Started, Done: r.Done, Succeeded: r.Succeeded, Finished: r.Finished,
		Stopped: r.Stopped, TimedOut: r.TimedOut,
		Attempts: r.Attempts, OutOfMemory: r.OutOfMemory, Message: r.Message, Why: r.Why,
	}
	for _, e := range w.spec.Main.Env {
		if e.From != nil || e.RunID {
			continue
		}
		if run.Env == nil {
			run.Env = map[string]string{}
		}
		run.Env[e.Name] = e.Value
	}
	if r.TimedOut {
		// All of it, when no attempt ever had something to stand on.
		run.Timeout, run.Waited = b.Timeout, r.Finished.Sub(r.Started)
		if !r.Placed.IsZero() {
			run.Waited = r.Placed.Sub(r.Started)
		}
	}
	st := &workload.State{Run: run}
	if f := w.stuck; f != nil && !r.Done {
		st.Code, st.Evidence = reasons.SandboxFailed, []string{f.what}
		if f.image {
			st.Code = reasons.ImagePullFailed
		}
	}
	return st
}

package supervise

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/workload"
)

// evidenceLines is how many of a failed unit's last lines are kept.
const evidenceLines = 10

// work is one workload a Supervisor runs. All but spec, cancel and done is
// guarded by the Supervisor's lock.
type work struct {
	spec    workload.Spec
	created time.Time
	cancel  context.CancelFunc
	// done is closed once every unit has ended.
	done chan struct{}

	// at is the unit the startup waits for: a step, then the main unit.
	at string
	// ground is how what the units stand on failed; nil while it is
	// there, and for an engine that has none.
	ground *failure
	// failed is, per unit, how its last run failed; a unit that runs or
	// ended well has no entry.
	failed map[string]*failure
	// took is how long each finished step ran.
	took map[string]time.Duration
	// mainSince is when the main unit started; zero while it does not run.
	// ready is set once its check passed, at answered.
	mainSince, answered time.Time
	ready               bool
	restarts            int

	// run is what is known of a run; nil for an app. stopped is set when
	// Stop ended it, and stuck is what keeps its attempt from going on.
	run     *record
	stopped bool
	stuck   *failure
}

// failure is how a unit's run ended badly.
type failure struct {
	// what says it in one line; output is the last lines it printed.
	what   string
	exit   int
	output []string
	// image is set when the unit's image could not be fetched.
	image bool
}

// run keeps w running until ctx ends: each time what its units stand on
// ends, it starts over after a growing wait. found is what an earlier
// supervisor left running of it, for the first round to take over.
func (s *Supervisor) run(ctx context.Context, w *work, found *Found) {
	defer close(w.done)
	for n := 0; ; n++ {
		began := time.Now()
		s.round(ctx, w, found)
		found = nil
		if ctx.Err() != nil {
			return
		}
		if time.Since(began) > maxBackoff {
			n = 0
		}
		if !sleep(ctx, s.delay(n)) {
			return
		}
	}
}

// round takes w from the ground up to a running main unit and keeps it
// there. It returns when ctx ends or the ground does, with every unit
// stopped (or left running, when the supervisor leaves).
func (s *Supervisor) round(ctx context.Context, w *work, found *Found) {
	var ground Unit
	if found != nil {
		ground = found.Ground
	} else if g, ok := s.eng.(Grounded); ok {
		var err error
		if ground, err = g.Ground(ctx, w.spec); err != nil {
			if ctx.Err() == nil {
				s.update(w, func() {
					w.ground = &failure{what: fmt.Sprintf("what the units run on did not start: %v", err), exit: -1}
				})
			}
			return
		}
	}
	s.update(w, func() { w.ground = nil })

	units, stop := context.WithCancel(ctx)
	var kept sync.WaitGroup
	if ground != nil {
		ended := make(chan int, 1)
		go func() {
			ended <- ground.Wait()
			stop()
		}()
		// The units end before what they stand on.
		defer func() {
			kept.Wait()
			if s.leaving.Load() {
				return
			}
			ground.Stop()
			code := <-ended
			if ctx.Err() != nil {
				return
			}
			s.update(w, func() {
				w.ground = &failure{what: fmt.Sprintf("what the units run on ended with code %d", code), exit: code}
				w.at, w.mainSince, w.ready = "", time.Time{}, false
				clear(w.failed)
			})
		}()
	}
	defer kept.Wait()
	defer stop()
	s.units(units, w, found, &kept)
}

// units runs w's steps in order and then its main unit, which it keeps
// running until ctx ends. Steps that stay are kept running in kept.
func (s *Supervisor) units(ctx context.Context, w *work, found *Found, kept *sync.WaitGroup) {
	// taken is the unit called name that was found running, or nil.
	taken := func(name string) Unit {
		if found == nil {
			return nil
		}
		return found.Units[name]
	}
	for _, u := range w.spec.Steps {
		if !u.Stays {
			// A workload found running has its steps behind it.
			if found != nil {
				continue
			}
			s.update(w, func() { w.at = u.Name })
			if !s.keep(ctx, w, u, nil, nil) {
				return
			}
			continue
		}
		s.update(w, func() { w.at = u.Name })
		if !s.beside(ctx, w, u, found, kept) {
			return
		}
	}

	main := w.spec.Main
	s.update(w, func() { w.at = main.Name })
	adopted := taken(main.Name)
	s.keep(ctx, w, main, adopted, func(running context.Context, r Unit) {
		since := time.Now()
		if r == adopted {
			since = found.MainSince
		}
		s.update(w, func() { w.mainSince = since })
		if !s.answers(running, w, main, r) {
			return
		}
		s.update(w, func() {
			if running.Err() == nil {
				w.ready, w.answered = true, time.Now()
			}
		})
	})
}

// keep runs u, and runs it again after a growing wait each time it exits:
// a step until it exits with 0, a unit that stays and the main unit until
// ctx ends. first, unless nil, is u already running, for the first turn.
// up is called, each time u has started, with the started unit and a
// context that ends when that run does. It reports whether u ended well.
func (s *Supervisor) keep(ctx context.Context, w *work, u workload.Unit, first Unit, up func(context.Context, Unit)) bool {
	forever := u.Stays || u.Name == w.spec.Main.Name
	for n := 0; ; n++ {
		began := time.Now()
		r, err := first, error(nil)
		if first = nil; r == nil {
			r, err = s.eng.Start(ctx, w.spec, u)
		}
		code := 0
		if err == nil {
			if forever {
				// It runs again: what the last run did wrong is over. A step's
				// failure stays until the step ends well.
				s.update(w, func() { delete(w.failed, u.Name) })
			}
			running, ended := context.WithCancel(ctx)
			told := make(chan struct{})
			go func() {
				defer close(told)
				if up != nil {
					up(running, r)
				}
			}()
			var ok bool
			code, ok = s.wait(ctx, r)
			ended()
			<-told
			if !ok {
				return false
			}
		}
		if ctx.Err() != nil {
			return false
		}
		ran := time.Since(began)
		f := s.failureOf(ctx, w, u, code, err)
		s.update(w, func() {
			if delete(w.failed, u.Name); f != nil {
				w.failed[u.Name] = f
			}
			if u.Name == w.spec.Main.Name {
				w.mainSince, w.ready = time.Time{}, false
				w.restarts++
			}
			if f == nil && !forever {
				w.took[u.Name] = ran
			}
		})
		if f == nil && !forever {
			return true
		}
		if ran > maxBackoff {
			n = 0
		}
		if !sleep(ctx, s.delay(n)) {
			return false
		}
	}
}

// failureOf describes a run of u that did not start (err) or exited with
// code; nil when it exited with 0.
func (s *Supervisor) failureOf(ctx context.Context, w *work, u workload.Unit, code int, err error) *failure {
	switch {
	case err != nil:
		var image *ImageError
		return &failure{what: fmt.Sprintf("unit %s did not start: %v", u.Name, err), exit: -1, image: errors.As(err, &image)}
	case code == 0:
		return nil
	}
	return &failure{
		what: fmt.Sprintf("unit %s exited with code %d", u.Name, code), exit: code,
		output: s.tail(ctx, w.spec.Ref, u.Name),
	}
}

// tail is the last lines the unit printed; nil when they cannot be read.
func (s *Supervisor) tail(ctx context.Context, ref workload.Ref, unit string) []string {
	rc, err := s.eng.Logs(ctx, ref, executor.LogOptions{Unit: unit, Tail: evidenceLines})
	if err != nil {
		return nil
	}
	defer rc.Close()
	var lines []string
	sc := bufio.NewScanner(rc)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines[max(len(lines)-evidenceLines, 0):]
}

// delay is the wait before restart number n (from 0).
func (s *Supervisor) delay(n int) time.Duration {
	d := s.backoff
	for ; n > 0 && d < maxBackoff; n-- {
		d *= 2
	}
	return min(d, maxBackoff)
}

// wait returns r's exit code. When ctx ends first it stops r, unless the
// supervisor leaves, and reports false.
func (s *Supervisor) wait(ctx context.Context, r Unit) (int, bool) {
	exited := make(chan int, 1)
	go func() { exited <- r.Wait() }()
	select {
	case code := <-exited:
		return code, true
	case <-ctx.Done():
		if s.leaving.Load() {
			return 0, false
		}
		r.Stop()
		<-exited
		return 0, false
	}
}

// sleep waits for d; false when ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

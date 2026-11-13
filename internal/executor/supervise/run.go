package supervise

import (
	"bufio"
	"context"
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
}

// failure is how a unit's run ended badly.
type failure struct {
	// what says it in one line; output is the last lines it printed.
	what   string
	exit   int
	output []string
}

// run takes w from its first step to a running main unit, and keeps it
// there until ctx ends.
func (s *Supervisor) run(ctx context.Context, w *work) {
	defer close(w.done)
	var kept sync.WaitGroup
	defer kept.Wait()
	for _, u := range w.spec.Steps {
		s.update(w, func() { w.at = u.Name })
		if !u.Stays {
			if !s.keep(ctx, w, u, nil) {
				return
			}
			continue
		}
		// The next unit starts once this one runs and its check passes.
		up := make(chan struct{}, 1)
		kept.Go(func() {
			s.keep(ctx, w, u, func(context.Context) {
				select {
				case up <- struct{}{}:
				default:
				}
			})
		})
		select {
		case <-up:
		case <-ctx.Done():
			return
		}
		if !s.answers(ctx, w, u) {
			return
		}
	}

	main := w.spec.Main
	s.update(w, func() { w.at = main.Name })
	s.keep(ctx, w, main, func(running context.Context) {
		s.update(w, func() { w.mainSince = time.Now() })
		if !s.answers(running, w, main) {
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
// ctx ends. up is called, each time u has started, with a context that
// ends when that run does. It reports whether u ended well.
func (s *Supervisor) keep(ctx context.Context, w *work, u workload.Unit, up func(context.Context)) bool {
	forever := u.Stays || u.Name == w.spec.Main.Name
	for n := 0; ; n++ {
		began := time.Now()
		r, err := s.eng.Start(ctx, w.spec, u)
		code := 0
		if err == nil {
			if forever {
				// It runs again: what the last run did wrong is over. A step's
				// failure stays until the step ends well.
				s.update(w, func() { delete(w.failed, u.Name) })
			}
			running, ended := context.WithCancel(ctx)
			if up != nil {
				go up(running)
			}
			var ok bool
			code, ok = wait(ctx, r)
			ended()
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
		return &failure{what: fmt.Sprintf("unit %s did not start: %v", u.Name, err), exit: -1}
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

// wait returns r's exit code. When ctx ends first it stops r and reports
// false.
func wait(ctx context.Context, r Unit) (int, bool) {
	exited := make(chan int, 1)
	go func() { exited <- r.Wait() }()
	select {
	case code := <-exited:
		return code, true
	case <-ctx.Done():
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

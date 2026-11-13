// Package executortest holds what every executor.Executor must do, as
// tests: Run checks one executor against it. The workloads are small shell
// commands (sh, sleep, echo, cat), so the same suite fits Kubernetes,
// Docker and local processes.
package executortest

import (
	"context"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/workload"
)

// Target is one executor to check.
type Target struct {
	Executor executor.Executor
	// Secrets is where the executor reads bundles from.
	Secrets executor.Secrets
	// Image has sh, sleep, echo and cat; empty for an executor that runs
	// units without an image.
	Image string
}

// patience is how long a workload gets to reach a state.
const patience = 30 * time.Second

// secretValue is a value no test prints: it must never show up in a state
// or an error.
const secretValue = "v3ry-s3cr3t-9471"

// Run checks the executor made by build, a fresh one per test, against
// what every executor must do.
func Run(t *testing.T, build func(t *testing.T) Target) {
	tests := []struct {
		name string
		run  func(*suite)
	}{
		{"StartIsIdempotent", (*suite).startIsIdempotent},
		{"ObserveIsNilBeforeStartAndAfterStop", (*suite).observeNil},
		{"FailingStepFailsWithEvidence", (*suite).failingStep},
		{"MainThatNeverAnswersStaysUnanswered", (*suite).unanswered},
		{"LogsReturnWhatTheUnitPrinted", (*suite).logs},
		{"AddrIsStable", (*suite).addr},
		{"ListNamesWhatRuns", (*suite).list},
		{"WatchFiresOnChange", (*suite).watch},
		{"SecretValueNeverShows", (*suite).secret},
	}
	for i, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			s := &suite{T: t, Target: build(t)}
			s.ref = workload.Ref{Namespace: "alice", Name: fmt.Sprintf("conformance-%d", i), ID: fmt.Sprintf("a-%d", i), Owner: "alice"}
			t.Cleanup(func() {
				// The test's own context has ended by now.
				ctx, cancel := context.WithTimeout(context.Background(), patience)
				defer cancel()
				_ = s.Executor.Stop(ctx, s.ref)
			})
			c.run(s)
		})
	}
}

// suite is one test's executor and the workload it runs.
type suite struct {
	*testing.T
	Target
	ref workload.Ref
}

// spec is a workload whose main unit runs script in sh and whose steps run
// theirs, each to its end. It serves on the port the executor's address
// names: an executor that picks the port itself is told its own choice.
func (s *suite) spec(script string, steps ...string) workload.Spec {
	s.Helper()
	sh := func(name, script string) workload.Unit {
		return workload.Unit{Name: name, Image: s.Image, Command: []string{"sh", "-c", script}}
	}
	_, port, err := net.SplitHostPort(s.address())
	if err != nil {
		s.Fatal(err)
	}
	n, err := strconv.ParseInt(port, 10, 32)
	if err != nil {
		s.Fatal(err)
	}
	sp := workload.Spec{Ref: s.ref, Port: int32(n), Main: sh(workload.MainUnit, script)}
	for i, step := range steps {
		sp.Steps = append(sp.Steps, sh(fmt.Sprintf("step-%d", i), step))
	}
	return sp
}

func (s *suite) address() string {
	s.Helper()
	addr, err := s.Executor.Addr(s.Context(), s.ref, 8080)
	if err != nil {
		s.Fatal(err)
	}
	return addr
}

func (s *suite) start(sp workload.Spec) {
	s.Helper()
	if err := s.Executor.Start(s.Context(), sp); err != nil {
		s.Fatal(err)
	}
}

func (s *suite) observe() *workload.State {
	s.Helper()
	st, err := s.Executor.Observe(s.Context(), s.ref)
	if err != nil {
		s.Fatal(err)
	}
	return st
}

// until waits for ok, which what describes.
func (s *suite) until(what string, ok func() bool) {
	s.Helper()
	for deadline := time.Now().Add(patience); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	s.Fatalf("never %s; state %+v", what, s.observe())
}

func (s *suite) untilPhase(phase string) *workload.State {
	s.Helper()
	var st *workload.State
	s.until("in phase "+phase, func() bool {
		st = s.observe()
		return st != nil && st.Phase == phase
	})
	return st
}

// output is what the workload's unit printed so far, o picking the part.
func (s *suite) output(o executor.LogOptions) string {
	s.Helper()
	rc, err := s.Executor.Logs(s.Context(), s.ref, o)
	if err != nil {
		return ""
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		s.Fatal(err)
	}
	return string(b)
}

func (s *suite) startIsIdempotent() {
	sp := s.spec("echo started; sleep 300")
	s.start(sp)
	s.start(sp)
	s.untilPhase("Running")
	s.start(sp)
	s.until("printing", func() bool { return s.output(executor.LogOptions{}) != "" })
	if got := s.output(executor.LogOptions{}); got != "started\n" {
		s.Fatalf("output %q: the unit ran more than once", got)
	}
}

func (s *suite) observeNil() {
	if st := s.observe(); st != nil {
		s.Fatalf("before Start: %+v", st)
	}
	s.start(s.spec("sleep 300"))
	s.untilPhase("Running")
	if err := s.Executor.Stop(s.Context(), s.ref); err != nil {
		s.Fatal(err)
	}
	s.until("gone", func() bool { return s.observe() == nil })
	if err := s.Executor.Stop(s.Context(), s.ref); err != nil {
		s.Fatalf("stopping what is gone: %v", err)
	}
}

func (s *suite) failingStep() {
	s.start(s.spec("echo main-ran; sleep 300", "echo fine", "echo the-reason >&2; exit 3"))
	st := s.untilPhase("Failed")
	if st.Step == "" || st.Code == "" {
		s.Fatalf("failed with no step or code: %+v", st)
	}
	ev := strings.Join(st.Evidence, "\n")
	if !strings.Contains(ev, "3") || !strings.Contains(ev, "the-reason") {
		s.Fatalf("evidence %q, want the exit code 3 and the step's last line", st.Evidence)
	}
	if got := s.output(executor.LogOptions{}); strings.Contains(got, "main-ran") {
		s.Fatal("the main unit ran after a step failed")
	}
}

func (s *suite) unanswered() {
	sp := s.spec("sleep 300")
	sp.Main.Ready = &workload.Check{HTTPPath: "/"}
	before := time.Now().Add(-time.Second)
	s.start(sp)
	var st *workload.State
	s.until("unanswered", func() bool {
		st = s.observe()
		return st != nil && st.Unanswered != nil
	})
	if st.Phase != "Starting" {
		s.Fatalf("phase %s, want Starting", st.Phase)
	}
	if u := st.Unanswered; u.Since.Before(before) || u.Since.After(time.Now().Add(time.Second)) || u.Check == "" {
		s.Fatalf("unanswered %+v, want the time the main unit started and the check", u)
	}
	// It stays so: nothing ever answers.
	if again := s.observe(); again == nil || again.Unanswered == nil || !again.Unanswered.Since.Equal(st.Unanswered.Since) {
		s.Fatalf("then %+v", again)
	}
}

func (s *suite) logs() {
	s.start(s.spec("echo one; echo two; echo three; sleep 300", "echo from-the-step"))
	s.until("printing three lines", func() bool { return s.output(executor.LogOptions{}) == "one\ntwo\nthree\n" })
	if got := s.output(executor.LogOptions{Tail: 2}); got != "two\nthree\n" {
		s.Fatalf("last two lines %q", got)
	}
	if got := s.output(executor.LogOptions{Unit: "step-0"}); got != "from-the-step\n" {
		s.Fatalf("the step's output %q", got)
	}
}

func (s *suite) addr() {
	before := s.address()
	if before != s.address() {
		s.Fatal("two addresses for a workload that does not run")
	}
	s.start(s.spec("sleep 300"))
	s.untilPhase("Running")
	if after := s.address(); after != before {
		s.Fatalf("address %s before Start, %s after", before, after)
	}
}

func (s *suite) list() {
	names := func() []string {
		refs, err := s.Executor.List(s.Context())
		if err != nil {
			s.Fatal(err)
		}
		var out []string
		for _, r := range refs {
			out = append(out, r.Namespace+"/"+r.Name)
		}
		return out
	}
	me := s.ref.Namespace + "/" + s.ref.Name
	if slices.Contains(names(), me) {
		s.Fatal("listed before Start")
	}
	s.start(s.spec("sleep 300"))
	s.until("listed", func() bool { return slices.Contains(names(), me) })
	if err := s.Executor.Stop(s.Context(), s.ref); err != nil {
		s.Fatal(err)
	}
	s.until("no longer listed", func() bool { return !slices.Contains(names(), me) })
}

func (s *suite) watch() {
	var told atomic.Bool
	go func() {
		_ = s.Executor.Watch(s.Context(), func(r workload.Ref) {
			if r.Namespace == s.ref.Namespace && r.Name == s.ref.Name {
				told.Store(true)
			}
		})
	}()
	// Watch starts listening on its own goroutine, at a moment the test
	// cannot see: changes are made until one is reported.
	sp := s.spec("sleep 300")
	s.until("told of a change", func() bool {
		s.start(sp)
		_ = s.Executor.Stop(s.Context(), s.ref)
		return told.Load()
	})
}

func (s *suite) secret() {
	ctx := s.Context()
	if err := s.Secrets.Put(ctx, s.ref, "tokens", executor.Bundle{Data: map[string][]byte{"token": []byte(secretValue)}}); err != nil {
		s.Fatal(err)
	}
	shown := func(where string, v any) {
		s.Helper()
		if text := fmt.Sprintf("%+v", v); strings.Contains(text, secretValue) {
			s.Fatalf("%s holds the secret value: %s", where, text)
		}
	}
	// The step gets the value (it exits with 9 without it) and fails.
	sp := s.spec("sleep 300", `[ -n "$TOKEN" ] || exit 9; exit 4`)
	sp.Steps[0].Env = []workload.Env{{Name: "TOKEN", From: &workload.SecretRef{Bundle: "tokens", Key: "token"}}}
	s.start(sp)
	st := s.untilPhase("Failed")
	if ev := strings.Join(st.Evidence, "\n"); !strings.Contains(ev, "4") {
		s.Fatalf("evidence %q: the step did not get the value", st.Evidence)
	}
	shown("the state", *st)
	if err := s.Executor.Stop(ctx, s.ref); err != nil {
		s.Fatal(err)
	}

	// A workload that asks for a value the bundle lacks does not run, and
	// what says so names no value.
	sp.Steps[0].Env[0].From.Key = "absent"
	if err := s.Executor.Start(ctx, sp); err != nil {
		shown("the error of Start", err.Error())
		return
	}
	s.until("not running", func() bool {
		st := s.observe()
		shown("the state", st)
		return st != nil && st.Phase != "Running"
	})
}

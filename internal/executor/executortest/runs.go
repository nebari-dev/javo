package executortest

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/workload"
)

// Runs checks the executor of runs made by build, a fresh one per test,
// against what every executor of runs must do. The runs leave their last
// message in the file JAVO_TERMINATION_LOG names, or else in the one
// Kubernetes reads.
func Runs(t *testing.T, build func(t *testing.T) Target) {
	check(t, "run", build, []test{
		{"Succeeds", (*suite).runSucceeds},
		{"FailsNowAndIsNotRetried", (*suite).runFailsNow},
		{"FailsAndIsRetried", (*suite).runIsRetried},
		{"TimesOut", (*suite).runTimesOut},
		{"IsStoppedWhileGoing", (*suite).runIsStopped},
		{"LogsAreReadableWhileGoingAndAfter", (*suite).runLogs},
		{"StaysUntilLetGo", (*suite).runStays},
		{"SurvivesARestart", (*suite).runSurvives},
	})
}

// leave is a script that leaves message as the run's last message.
func leave(message string) string {
	return `printf '%s' '` + message + `' > "${` + workload.EnvTerminationLog + `:-/dev/termination-log}"`
}

// batch is a run whose main unit runs script in sh, tried again retries
// times unless it exits with 12.
func (s *suite) batch(script string, retries int32) workload.Spec {
	sp := workload.Spec{
		Ref:   s.ref,
		Main:  workload.Unit{Name: workload.MainUnit, Image: s.Image, Command: []string{"sh", "-c", script}},
		Batch: &workload.Batch{Retries: retries, FailNow: []int32{12}},
	}
	sp.Main.Env = []workload.Env{{Name: "PLAIN", Value: "as-given"}, {Name: "RUN_ID", RunID: true}}
	if s.Fit != nil {
		s.Fit(&sp)
	}
	return sp
}

// finished waits for the run to be done.
func (s *suite) finished() *workload.RunState {
	s.Helper()
	var st *workload.State
	s.until("done", func() bool {
		st = s.observe()
		return st != nil && st.Run != nil && st.Run.Done
	})
	return st.Run
}

func (s *suite) stop() {
	s.Helper()
	if err := s.Executor.Stop(s.Context(), s.ref); err != nil {
		s.Fatal(err)
	}
}

func (s *suite) listed() bool {
	s.Helper()
	refs, err := s.Executor.List(s.Context())
	if err != nil {
		s.Fatal(err)
	}
	return slices.ContainsFunc(refs, func(r workload.Ref) bool {
		return r.Namespace == s.ref.Namespace && r.Name == s.ref.Name && r.ID == s.ref.ID
	})
}

func (s *suite) runSucceeds() {
	before := time.Now().Add(-time.Second)
	if err := s.Secrets.Put(s.Context(), s.ref, "run-tokens", executor.Bundle{Data: map[string][]byte{"token": []byte(secretValue)}}); err != nil {
		s.Fatal(err)
	}
	// It fails with 8 without the value of its bundle, and with 9 when
	// the id it is given is not its handle.
	sp := s.batch(`[ -n "$TOKEN" ] || exit 8; [ "$RUN_ID" = "`+s.ref.Name+`" ] || exit 9; `+leave(`{"reason":"Succeeded"}`), 2)
	sp.Main.Env = append(sp.Main.Env, workload.Env{Name: "TOKEN", From: &workload.SecretRef{Bundle: "run-tokens", Key: "token"}})
	s.start(sp)
	r := s.finished()
	if !r.Succeeded || r.Stopped || r.TimedOut || r.OutOfMemory || r.Attempts != 1 {
		s.Fatalf("run %+v, want one attempt that succeeded", r)
	}
	if r.Handle != s.ref.Name || !r.Manual || !r.Slot.IsZero() {
		s.Fatalf("run %+v, want the handle %s and no slot", r, s.ref.Name)
	}
	if r.Started.Before(before) || r.Finished.Before(r.Started) || r.Finished.After(time.Now().Add(time.Second)) {
		s.Fatalf("started %s, finished %s", r.Started, r.Finished)
	}
	if _, shown := r.Env["TOKEN"]; r.Env["PLAIN"] != "as-given" || shown {
		s.Fatalf("variables %v, want the plain one it was started with and none from a bundle", r.Env)
	}
	if text := fmt.Sprintf("%+v %+v", *s.observe(), *r); strings.Contains(text, secretValue) {
		s.Fatalf("the state holds the secret value: %s", text)
	}
	if r.Message != `{"reason":"Succeeded"}` {
		s.Fatalf("message %q", r.Message)
	}
}

func (s *suite) runFailsNow() {
	s.start(s.batch(`echo tried; `+leave("cannot be fixed")+`; exit 12`, 3))
	r := s.finished()
	if r.Succeeded || r.Stopped || r.TimedOut || r.Attempts != 1 || r.Message != "cannot be fixed" {
		s.Fatalf("run %+v, want one failed attempt and its message", r)
	}
}

func (s *suite) runIsRetried() {
	sp := s.batch(`exit 3`, 2)
	sp.Batch.Slot = time.Now().Truncate(time.Minute).UTC()
	s.start(sp)
	r := s.finished()
	if r.Succeeded || r.Stopped || r.TimedOut || r.Attempts != 3 {
		s.Fatalf("run %+v, want three failed attempts", r)
	}
	if r.Manual || !r.Slot.Equal(sp.Batch.Slot) {
		s.Fatalf("run %+v, want the slot %s", r, sp.Batch.Slot)
	}
}

func (s *suite) runTimesOut() {
	sp := s.batch(`sleep 300`, 0)
	sp.Batch.Timeout = time.Second
	s.start(sp)
	r := s.finished()
	if !r.TimedOut || r.Succeeded || r.Stopped || r.Timeout != time.Second {
		s.Fatalf("run %+v, want it stopped at its time limit of a second", r)
	}
	if took := r.Finished.Sub(r.Started); took < time.Second {
		s.Fatalf("it ended after %s", took)
	}
}

func (s *suite) runIsStopped() {
	s.start(s.batch(`echo going; sleep 300`, 3))
	s.until("printing", func() bool { return s.output(executor.LogOptions{}) == "going\n" })
	if st := s.observe(); st == nil || st.Run == nil || st.Run.Done {
		s.Fatalf("state %+v, want a run that is going", st)
	}
	s.stop()
	r := s.finished()
	if !r.Stopped || r.Succeeded || r.TimedOut || r.Finished.IsZero() {
		s.Fatalf("run %+v, want it stopped", r)
	}
	// It ended, and stays: it is not started again.
	if again := s.observe(); again == nil || !again.Run.Stopped || again.Run.Attempts != r.Attempts {
		s.Fatalf("then %+v", again)
	}
	s.stop()
	s.until("gone", func() bool { return s.observe() == nil })
}

func (s *suite) runLogs() {
	rc, err := s.Executor.Logs(s.Context(), s.ref, executor.LogOptions{})
	if err != nil || rc != nil {
		s.Fatalf("the log of a run that was not started: %v, %v; want none and no error", rc, err)
	}
	pause := cmp.Or(s.Pause, 3*time.Second)
	s.start(s.batch(fmt.Sprintf(`echo one; sleep %g; echo two`, pause.Seconds()), 0))
	s.until("printing its first line", func() bool { return s.output(executor.LogOptions{}) == "one\n" })
	if st := s.observe(); st == nil || st.Run == nil || st.Run.Done {
		s.Fatalf("state %+v, want a run that is going", st)
	}
	if r := s.finished(); !r.Succeeded {
		s.Fatalf("run %+v", r)
	}
	if got := s.output(executor.LogOptions{}); got != "one\ntwo\n" {
		s.Fatalf("output after the run ended %q", got)
	}
	if got := s.output(executor.LogOptions{Tail: 1}); got != "two\n" {
		s.Fatalf("last line %q", got)
	}
}

func (s *suite) runStays() {
	if s.listed() {
		s.Fatal("listed before Start")
	}
	sp := s.batch(`echo ran`, 0)
	s.start(sp)
	r := s.finished()
	if !s.listed() {
		s.Fatal("a run that is done is not listed")
	}
	// Started again, it is the run that ended.
	s.start(sp)
	if again := s.observe(); again == nil || !again.Run.Done || !again.Run.Finished.Equal(r.Finished) {
		s.Fatalf("after a second Start: %+v, want the run that ended at %s", again, r.Finished)
	}
	if got := s.output(executor.LogOptions{}); got != "ran\n" {
		s.Fatalf("output %q: the run ran more than once", got)
	}
	s.stop()
	s.until("gone", func() bool { return s.observe() == nil && !s.listed() })
	s.stop()
}

func (s *suite) runSurvives() {
	s.start(s.batch(`echo ran; `+leave("the last words")+`; exit 12`, 0))
	before := s.finished()
	s.Executor = s.Again()
	st := s.observe()
	if st == nil || st.Run == nil {
		s.Fatalf("after a restart: %+v, want the run", st)
	}
	r := st.Run
	if !r.Done || r.Succeeded || r.Stopped || r.Attempts != 1 || r.Message != "the last words" ||
		!r.Started.Equal(before.Started) || !r.Finished.Equal(before.Finished) || r.Handle != before.Handle || r.Env["PLAIN"] != "as-given" {
		s.Fatalf("after a restart %+v, before it %+v", r, before)
	}
	if !s.listed() {
		s.Fatal("not listed after a restart")
	}
	if got := s.output(executor.LogOptions{}); !strings.Contains(got, "ran") {
		s.Fatalf("output after a restart %q", got)
	}
	s.stop()
	s.until("gone", func() bool { return s.observe() == nil && !s.listed() })
}

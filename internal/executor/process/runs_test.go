//go:build linux || darwin

package process_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/executortest"
	"github.com/nebari-dev/javo/internal/executor/process"
	"github.com/nebari-dev/javo/internal/workload"
)

// runs is an executor of runs, and the executor of apps it stands on, on
// the state directory dir. Both are closed when the test ends.
func runs(t *testing.T, dir string, secrets *executortest.Secrets) (*process.Runs, *process.Executor) {
	t.Helper()
	x, err := process.New(process.Options{Dir: dir, Secrets: secrets, Grace: time.Second, Every: quick, Backoff: quick})
	if err != nil {
		t.Fatal(err)
	}
	r, err := x.Runs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		r.Close()
		x.Close()
	})
	return r, x
}

// The process executor of runs does what every executor of runs must.
func TestRunsConformance(t *testing.T) {
	executortest.Runs(t, func(t *testing.T) executortest.Target {
		dir, secrets := filepath.Join(t.TempDir(), "state"), &executortest.Secrets{}
		r, x := runs(t, dir, secrets)
		return executortest.Target{Executor: r, Secrets: secrets, Again: func() executor.Executor {
			r.Close()
			x.Close()
			r, x = runs(t, dir, secrets)
			return r
		}}
	})
}

// job is a run of ref whose main unit runs script.
func job(script string, retries int32, args ...string) workload.Spec {
	s := app(script, args...)
	s.Batch = &workload.Batch{Retries: retries}
	return s
}

func finished(t *testing.T, r *process.Runs) *workload.RunState {
	t.Helper()
	var st *workload.State
	until(t, "done", func() bool {
		var err error
		if st, err = r.Observe(t.Context(), ref); err != nil {
			t.Fatal(err)
		}
		return st != nil && st.Run.Done
	})
	return st.Run
}

// A run is refused as an app is: for an image nothing here runs, and by
// the executor of apps; and an app is refused by the executor of runs.
func TestRunsRefuseWhatAppsRefuse(t *testing.T) {
	r, x := runs(t, filepath.Join(t.TempDir(), "state"), &executortest.Secrets{})
	s := job("true", 0)
	s.Main.Image = "registry.example.com/python:3"
	var unsupported *process.UnsupportedError
	if err := r.Start(t.Context(), s); !errors.As(err, &unsupported) || !strings.Contains(err.Error(), "needs the image") {
		t.Fatalf("a run that needs an image: %v", err)
	}
	if st, _ := r.Observe(t.Context(), ref); st != nil {
		t.Fatalf("the refused run is known: %+v", st)
	}
	if err := r.Start(t.Context(), app("true")); err == nil {
		t.Fatal("the executor of runs started an app")
	}
	if err := x.Start(t.Context(), job("true", 0)); !errors.Is(err, process.ErrBatch) {
		t.Fatalf("the executor of apps and a run: %v", err)
	}
	if _, err := r.Addr(t.Context(), ref, 8080); err == nil {
		t.Fatal("a run has an address")
	}
}

// A run that was going when Javo ended has lost that attempt: its
// processes do not outlive Javo. The next Javo tries it again while it
// has retries left, in the same folder.
func TestRunsARunGoingAtARestartIsTriedAgain(t *testing.T) {
	dir, secrets := filepath.Join(t.TempDir(), "state"), &executortest.Secrets{}
	r, x := runs(t, dir, secrets)
	// The first attempt leaves a mark and waits; the second finds it.
	mark := filepath.Join(t.TempDir(), "mark")
	if err := r.Start(t.Context(), job(`[ -e "$1" ] && exit 0; touch "$1"; echo waiting; sleep 300`, 1, mark)); err != nil {
		t.Fatal(err)
	}
	until(t, "waiting", func() bool { _, err := os.Stat(mark); return err == nil })
	r.Close()
	x.Close()

	r, _ = runs(t, dir, secrets)
	if got := finished(t, r); !got.Succeeded || got.Attempts != 2 || got.Stopped {
		t.Fatalf("run %+v, want it to succeed at its second attempt", got)
	}
}

// Letting go of a run removes its folder: its log, its record and its
// message.
func TestRunsLettingGoRemovesTheFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	r, _ := runs(t, dir, &executortest.Secrets{})
	if err := r.Start(t.Context(), job("echo ran", 0)); err != nil {
		t.Fatal(err)
	}
	finished(t, r)
	folder := filepath.Join(dir, "w", ref.Namespace, ref.Name)
	if _, err := os.Stat(filepath.Join(folder, "run.json")); err != nil {
		t.Fatalf("the run's record: %v", err)
	}
	if err := r.Stop(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Fatalf("the run's folder stayed: %v", err)
	}
}

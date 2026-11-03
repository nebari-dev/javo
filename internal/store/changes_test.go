package store_test

import (
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// drain discards announcements until the feed has been quiet for a moment:
// Postgres delivers them asynchronously, after the write returned.
func drain(s *store.Store) {
	for {
		select {
		case <-s.Changes():
		case <-time.After(150 * time.Millisecond):
			return
		}
	}
}

func next(t *testing.T, s *store.Store) store.Change {
	t.Helper()
	select {
	case c := <-s.Changes():
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("no change announced")
		return store.Change{}
	}
}

// requireNoChange fails if the writes before it announced anything. Postgres
// delivers announcements later and in commit order, so it makes one write that
// is announced and requires that to be the next change.
func requireNoChange(t *testing.T, s *store.Store, what string) {
	t.Helper()
	barrier := createApp(t, s, "alice", "barrier")
	if c := next(t, s); c.Table != "apps" || c.ID != barrier.ID {
		t.Fatalf("%s announced %+v", what, c)
	}
}

func TestChangeAfterCommit(t *testing.T) {
	t.Parallel()
	s, _ := startedStore(t)
	app := createApp(t, s, "alice", "lab")
	c := next(t, s)
	if c.Table != "apps" || c.ID != app.ID {
		t.Fatalf("change = %+v, want apps/%s", c, app.ID)
	}
	// The announced row is already visible.
	if _, err := s.Apps().Get(ctx, c.ID); err != nil {
		t.Fatalf("announced before commit: %v", err)
	}
	if _, err := s.Apps().UpdateSpec(ctx, alice, app.ID, 1, func(a *model.App) error { a.Paused = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != "apps" || c.ID != app.ID {
		t.Fatalf("update change = %+v", c)
	}
}

// Status writes are the controller's own: they do not wake the controller.
func TestStatusWriteIsNotAnnounced(t *testing.T) {
	t.Parallel()
	s, _ := startedStore(t)
	app := createApp(t, s, "alice", "lab")
	drain(s)
	if err := s.Apps().SetStatus(ctx, app.ID, 1, model.AppStatus{Phase: "Running"}); err != nil {
		t.Fatal(err)
	}
	requireNoChange(t, s, "a status write")
}

func TestRunRequestsAreAnnounced(t *testing.T) {
	t.Parallel()
	s, _ := startedStore(t)
	j := createJob(t, s, "alice", "nightly")
	drain(s)
	run, err := s.Jobs().RequestRun(ctx, alice, j.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != "job_runs" || c.ID != run.ID {
		t.Fatalf("change = %+v", c)
	}
	if err := s.Jobs().RequestCancel(ctx, alice, run.ID); err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != "job_runs" || c.ID != run.ID {
		t.Fatalf("cancel change = %+v", c)
	}
}

// A prune announces its job, so the jobs reconciler (which maps a job_runs
// change to the run's job by run id) is woken even though the runs are gone.
func TestPruneRunsAnnouncesTheJob(t *testing.T) {
	t.Parallel()
	s, _ := startedStore(t)
	j := createJob(t, s, "alice", "nightly")
	for range 2 {
		if _, err := s.Jobs().RecordRun(ctx, model.Run{JobID: j.ID, Trigger: "schedule", Outcome: model.RunOutcomeSucceeded, FinishTime: t0}); err != nil {
			t.Fatal(err)
		}
	}
	drain(s)
	if n, err := s.Jobs().PruneRuns(ctx, model.Actor{System: model.SystemController}, j.ID, 1, nil); err != nil || n != 1 {
		t.Fatalf("PruneRuns = %d, %v", n, err)
	}
	if c := next(t, s); c.Table != "jobs" || c.ID != j.ID {
		t.Fatalf("change = %+v, want jobs/%s", c, j.ID)
	}
}

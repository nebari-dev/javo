package store_test

import (
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// The idle timeout is kept, and an app the idle check stopped says so until
// it runs again.
func TestAppIdleStopRoundTrip(t *testing.T) {
	s := storetest.New(t)
	in := newApp("alice", "lab")
	in.IdleStopMinutes = 60
	created, err := s.Apps().Create(ctx, alice, in)
	if err != nil {
		t.Fatal(err)
	}
	if created.IdleStopMinutes != 60 {
		t.Fatalf("idle stop = %d", created.IdleStopMinutes)
	}
	stopped, err := s.Apps().StopIdle(ctx, created.ID, created.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !stopped.Paused || stopped.StoppedIdleAfter != 60 {
		t.Fatalf("after StopIdle: paused %v, stopped idle after %d", stopped.Paused, stopped.StoppedIdleAfter)
	}
	started, err := s.Apps().UpdateSpec(ctx, alice, created.ID, stopped.Version, func(a *model.App) error {
		a.Paused = false
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.StoppedIdleAfter != 0 {
		t.Fatalf("a started app still says it was stopped for being idle (%d)", started.StoppedIdleAfter)
	}
}

// StopIdle goes through the versioned write: an app changed since it was
// read is not stopped.
func TestStopIdleRefusesAChangedApp(t *testing.T) {
	s := storetest.New(t)
	app := createApp(t, s, "alice", "lab")
	if _, err := s.Apps().StopIdle(ctx, app.ID, app.Version+1); err == nil {
		t.Fatal("stopped an app at a version it is not at")
	}
}

func TestAppIdleStopMayNotBeNegative(t *testing.T) {
	s := storetest.New(t)
	in := newApp("alice", "lab")
	in.IdleStopMinutes = -1
	if _, err := s.Apps().Create(ctx, alice, in); err == nil {
		t.Fatal("created an app with a negative idle stop")
	}
}

// Activity only ever moves forward, so gateway replicas flushing in any
// order leave the latest time, and it never changes the app's version.
func TestNoteActivityKeepsTheLatest(t *testing.T) {
	s := storetest.New(t)
	app := createApp(t, s, "alice", "lab")
	later := time.Now().Add(time.Minute).Truncate(time.Millisecond)
	earlier := later.Add(-30 * time.Second)
	if err := s.Apps().NoteActivity(ctx, map[string]time.Time{app.ID: later, "no-such-app": later}); err != nil {
		t.Fatal(err)
	}
	if err := s.Apps().NoteActivity(ctx, map[string]time.Time{app.ID: earlier}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Apps().Get(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastActiveAt.Equal(later) {
		t.Fatalf("last active %v, want %v", got.LastActiveAt, later)
	}
	if got.Version != app.Version {
		t.Fatalf("version %d moved from %d", got.Version, app.Version)
	}
}

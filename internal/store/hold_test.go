package store_test

import (
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// HoldAll holds every live app under the restore's name; a deleted one is
// left alone.
func TestHoldAllHoldsLiveApps(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	a := mustCreate(t, s, newApp("alice", "lab"))
	p := newApp("alice", "paused")
	p.Paused = true
	b := mustCreate(t, s, p)
	gone := mustCreate(t, s, newApp("bob", "gone"))
	if err := s.Apps().MarkDeleted(ctx, alice, gone.ID); err != nil {
		t.Fatal(err)
	}
	n, err := s.Apps().HoldAll(ctx, model.Actor{System: model.SystemCLI}, "r1")
	if err != nil || n != 2 {
		t.Fatalf("HoldAll = %d, %v; want 2", n, err)
	}
	for _, id := range []string{a.ID, b.ID} {
		if got := mustApp(t, s, id); got.RestoreHold != "r1" || !got.ReleaseAt.IsZero() {
			t.Errorf("%s: hold %q release %v", got.Name, got.RestoreHold, got.ReleaseAt)
		}
	}
	if got := mustApp(t, s, gone.ID); got.RestoreHold != "" {
		t.Errorf("a deleted app was held: %q", got.RestoreHold)
	}
	held, err := s.Apps().ListHeld(ctx, time.Now())
	if err != nil || len(held) != 2 {
		t.Fatalf("ListHeld = %d, %v", len(held), err)
	}
}

// Release with Start gives apps that were running a start time spaced by
// the rate, in name order, and apps that were stopped the present time (they
// start nothing). Each release raises the version, so the controller hears of it.
func TestReleaseStaggersAppsThatWereRunning(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	var ids []string
	for _, name := range []string{"c", "a", "b"} {
		ids = append(ids, mustCreate(t, s, newApp("alice", name)).ID)
	}
	p := newApp("alice", "stopped")
	p.Paused = true
	stopped := mustCreate(t, s, p)
	if _, err := s.Apps().HoldAll(ctx, alice, "r1"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	n, err := s.Apps().Release(ctx, alice, "r1", store.ReleaseStart, 2, now)
	if err != nil || n != 4 {
		t.Fatalf("Release = %d, %v", n, err)
	}
	want := map[string]time.Time{"a": now, "b": now.Add(30 * time.Second), "c": now.Add(time.Minute), "stopped": now}
	for _, id := range append(ids, stopped.ID) {
		got := mustApp(t, s, id)
		if !got.ReleaseAt.Equal(want[got.Name]) || got.Version != 3 {
			t.Errorf("%s: release at %v version %d, want %v version 3 (held, then released)", got.Name, got.ReleaseAt, got.Version, want[got.Name])
		}
	}
	// Released apps are not held again by a second release.
	if n, err := s.Apps().Release(ctx, alice, "r1", store.ReleaseStart, 2, now); err != nil || n != 0 {
		t.Errorf("second Release = %d, %v", n, err)
	}
}

// Release with Stopped stops every app of the restore and lets them all go
// at once.
func TestReleaseStoppedStopsEverything(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	a := mustCreate(t, s, newApp("alice", "lab"))
	if _, err := s.Apps().HoldAll(ctx, alice, "r1"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if _, err := s.Apps().Release(ctx, alice, "r1", store.ReleaseStopped, 10, now); err != nil {
		t.Fatal(err)
	}
	if got := mustApp(t, s, a.ID); !got.Paused || !got.ReleaseAt.Equal(now) || got.Held(now) {
		t.Errorf("after release stopped: %+v", got)
	}
}

func mustApp(t *testing.T, s *store.Store, id string) model.App {
	t.Helper()
	out, err := s.Apps().Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func mustCreate(t *testing.T, s *store.Store, a *model.App) model.App {
	t.Helper()
	out, err := s.Apps().Create(ctx, alice, a)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

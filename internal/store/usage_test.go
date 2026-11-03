package store_test

import (
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// Since returns the intervals that overlap the window: still running, or
// stopped after its start. One that ended before the window is left out.
func TestUsageSinceKeepsOverlappingIntervals(t *testing.T) {
	s := storetest.New(t)
	u := s.Usage()
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, i := range []store.UsageInterval{
		{AppID: "a-old", Owner: "alice", StartedAt: base, StoppedAt: base.Add(time.Hour)},
		{AppID: "a-across", Owner: "alice", StartedAt: base.Add(2 * time.Hour), StoppedAt: base.Add(5 * time.Hour)},
		{AppID: "b-running", Owner: "bob", StartedAt: base.Add(6 * time.Hour)},
	} {
		if err := u.Start(ctx, i); err != nil {
			t.Fatal(err)
		}
		if !i.StoppedAt.IsZero() {
			if err := u.Stop(ctx, i.AppID, i.StoppedAt); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := u.Since(ctx, base.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, i := range got {
		ids[i.AppID] = true
	}
	if len(got) != 2 || !ids["a-across"] || !ids["b-running"] {
		t.Errorf("Since = %+v, want a-across and b-running", got)
	}
}

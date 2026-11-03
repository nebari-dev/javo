package model

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// Released apps start at most perMinute in any minute: 25 apps at 10 a
// minute take until the third minute, and the first minute holds 10 plus the
// one at its very start.
func TestReleaseTimesStartAtMostNPerMinute(t *testing.T) {
	times := ReleaseTimes(25, 10, t0)
	if len(times) != 25 {
		t.Fatalf("len = %d", len(times))
	}
	within := func(d time.Duration) int {
		n := 0
		for _, at := range times {
			if !at.After(t0.Add(d)) {
				n++
			}
		}
		return n
	}
	if n := within(time.Minute); n != 10 && n != 11 {
		t.Errorf("released in the first minute = %d, want 10 or 11", n)
	}
	if n := within(3 * time.Minute); n != 25 {
		t.Errorf("released within 3 minutes = %d, want 25", n)
	}
	// A rate of 0 or less is read as 1 a minute, never all at once.
	if got := ReleaseTimes(2, 0, t0); !got[1].Equal(t0.Add(time.Minute)) {
		t.Errorf("rate 0: %v", got)
	}
}

func TestHeld(t *testing.T) {
	for _, c := range []struct {
		name string
		app  App
		want bool
	}{
		{"not restored", App{}, false},
		{"restored, not released", App{RestoreHold: "r1"}, true},
		{"release later", App{RestoreHold: "r1", ReleaseAt: t0.Add(time.Second)}, true},
		{"released", App{RestoreHold: "r1", ReleaseAt: t0}, false},
	} {
		if got := c.app.Held(t0); got != c.want {
			t.Errorf("%s: Held = %v, want %v", c.name, got, c.want)
		}
	}
}

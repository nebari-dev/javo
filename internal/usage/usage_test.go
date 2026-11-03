package usage

import (
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/store"
)

var (
	now   = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	since = now.Add(-24 * time.Hour)
)

func iv(app, owner string, start, stop time.Duration) store.UsageInterval {
	i := store.UsageInterval{AppID: app, Owner: owner, StartedAt: now.Add(start)}
	if stop != 0 {
		i.StoppedAt = now.Add(stop)
	}
	return i
}

func TestAggregateByPersonCountsOnlyTheWindow(t *testing.T) {
	in := []store.UsageInterval{
		// Started before the window: only the 2 hours inside count.
		iv("plots", "alice", -26*time.Hour, -22*time.Hour),
		// Still running: counts up to now.
		iv("notes", "alice", -90*time.Minute, 0),
		iv("api", "bob", -3*time.Hour, -2*time.Hour),
	}
	people := map[string]Person{"alice": {DisplayName: "Alice"}, "bob": {DisplayName: "Bob"}}
	rows, totals := Aggregate(in, people, ByPerson, since, now)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	a := rows[0]
	if a.Key != "alice" || a.DisplayName != "Alice" || a.Hours != 3.5 || a.Apps != 2 || a.RunningNow != 1 {
		t.Errorf("alice = %+v", a)
	}
	if b := rows[1]; b.Key != "bob" || b.Hours != 1 || b.RunningNow != 0 {
		t.Errorf("bob = %+v", b)
	}
	if totals.People != 2 || totals.RunningNow != 1 || totals.Hours != 4.5 {
		t.Errorf("totals = %+v", totals)
	}
}

// By group, a person's hours count toward each of their groups; people in no
// group are under the empty key.
func TestAggregateByGroup(t *testing.T) {
	in := []store.UsageInterval{
		iv("plots", "alice", -2*time.Hour, -time.Hour),
		iv("api", "bob", -2*time.Hour, -time.Hour),
		iv("x", "carol", -2*time.Hour, -time.Hour),
	}
	people := map[string]Person{
		"alice": {Groups: []string{"data", "ops"}},
		"bob":   {Groups: []string{"data"}},
	}
	rows, _ := Aggregate(in, people, ByGroup, since, now)
	got := map[string]Row{}
	for _, r := range rows {
		got[r.Key] = r
	}
	if got["data"].Hours != 2 || got["data"].People != 2 || got["ops"].Hours != 1 || got[""].Hours != 1 {
		t.Errorf("rows = %+v", rows)
	}
}

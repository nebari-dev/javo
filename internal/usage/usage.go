// Package usage sums how long apps ran, per person or per group, from the
// usage intervals the controller records. It is pure: the caller reads the
// intervals and the people.
package usage

import (
	"cmp"
	"slices"
	"time"

	"github.com/nebari-dev/javo/internal/store"
)

// GroupBy is what a Row adds up.
type GroupBy string

const (
	ByPerson GroupBy = "person"
	ByGroup  GroupBy = "group"
)

// Person is what Aggregate needs to know about an owner.
type Person struct {
	DisplayName string
	Groups      []string
}

// Row is the use of one person or one group in the window.
type Row struct {
	// Key is the person's identity name or the group; "" is people in no group.
	Key         string
	DisplayName string
	// Hours is app-hours run inside the window: two apps running for an
	// hour are two hours.
	Hours float64
	// Apps is how many apps ran in the window; People how many owners.
	Apps, People int
	// RunningNow is how many of those apps are running now.
	RunningNow int
}

// Totals are the whole install's numbers for the window.
type Totals struct {
	Hours      float64
	People     int
	RunningNow int
}

// Aggregate sums the part of each interval inside [since, now] by person
// or by group (a person's hours count toward each of their groups). Rows
// come most hours first, then by key.
func Aggregate(in []store.UsageInterval, people map[string]Person, by GroupBy, since, now time.Time) ([]Row, Totals) {
	type acc struct {
		row     Row
		apps    map[string]bool
		owners  map[string]bool
		running map[string]bool
	}
	accs := map[string]*acc{}
	var totals Totals
	owners, running := map[string]bool{}, map[string]bool{}
	for _, i := range in {
		start, stop := i.StartedAt, now
		if start.Before(since) {
			start = since
		}
		if !i.StoppedAt.IsZero() && i.StoppedAt.Before(now) {
			stop = i.StoppedAt
		}
		if !stop.After(start) {
			continue
		}
		h := stop.Sub(start).Hours()
		totals.Hours += h
		owners[i.Owner] = true
		if i.StoppedAt.IsZero() {
			running[i.AppID] = true
		}
		p := people[i.Owner]
		keys := []string{i.Owner}
		if by == ByGroup {
			keys = p.Groups
			if len(keys) == 0 {
				keys = []string{""}
			}
		}
		for _, k := range keys {
			a := accs[k]
			if a == nil {
				a = &acc{row: Row{Key: k, DisplayName: k}, apps: map[string]bool{}, owners: map[string]bool{}, running: map[string]bool{}}
				if by == ByPerson && p.DisplayName != "" {
					a.row.DisplayName = p.DisplayName
				}
				accs[k] = a
			}
			a.row.Hours += h
			a.apps[i.AppID] = true
			a.owners[i.Owner] = true
			if i.StoppedAt.IsZero() {
				a.running[i.AppID] = true
			}
		}
	}
	rows := make([]Row, 0, len(accs))
	for _, a := range accs {
		a.row.Apps, a.row.People, a.row.RunningNow = len(a.apps), len(a.owners), len(a.running)
		rows = append(rows, a.row)
	}
	slices.SortFunc(rows, func(x, y Row) int {
		if c := cmp.Compare(y.Hours, x.Hours); c != 0 {
			return c
		}
		return cmp.Compare(x.Key, y.Key)
	})
	totals.People, totals.RunningNow = len(owners), len(running)
	return rows, totals
}

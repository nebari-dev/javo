// Package schedule previews cron schedules with the parser the Kubernetes
// CronJob controller uses, so the preview matches what will actually run.
package schedule

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/nebari-dev/javo/internal/reasons"
)

var (
	// ErrCronInvalid wraps a cron expression the parser rejects.
	ErrCronInvalid = errors.New("invalid cron expression")
	// ErrTimeZoneInvalid wraps a time zone name Go cannot load.
	ErrTimeZoneInvalid = errors.New("invalid time zone")
)

// Warning is a daylight saving effect on the schedule within the scanned year.
type Warning struct {
	Code reasons.MessageCode // MsgDSTSkipped or MsgDSTRepeated
	Date string              // YYYY-MM-DD in the schedule's zone
	// Time and Until are the first and last affected run (HH:MM wall
	// clock); Runs is how many runs that is.
	Time, Until string
	Runs        int
}

// Preview is the next run times and the daylight saving warnings.
type Preview struct {
	Next     []time.Time
	Warnings []Warning
}

// Next returns the next n run times of a standard 5-field cron in an IANA
// zone, after from, and the daylight saving changes in the following year that
// make the schedule skip or repeat a run.
func Next(spec, zone string, from time.Time, n int) (Preview, error) {
	sched, loc, err := parse(spec, zone)
	if err != nil {
		return Preview{}, err
	}
	var p Preview
	t := from.In(loc)
	for range n {
		t = sched.Next(t)
		if t.IsZero() {
			break
		}
		p.Next = append(p.Next, t.UTC())
	}
	p.Warnings = daylightSaving(sched, loc, from)
	return p, nil
}

// parse checks the cron and zone the way Kubernetes does. It rejects the zone
// "Local" and any schedule naming a zone itself (TZ= or CRON_TZ=) when
// timeZone is set.
func parse(spec, zone string) (cron.Schedule, *time.Location, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "" || strings.EqualFold(zone, "Local") {
		return nil, nil, fmt.Errorf("%w: %q", ErrTimeZoneInvalid, zone)
	}
	if strings.Contains(spec, "TZ") {
		return nil, nil, fmt.Errorf("%w: %q must not contain a time zone, set it separately", ErrCronInvalid, spec)
	}
	sched, err := parseStandard(spec)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrCronInvalid, err)
	}
	return sched, loc, nil
}

// lookbacks are the windows Previous scans, smallest first, so a frequent
// schedule costs few steps and a leap-day schedule is still found.
var lookbacks = []time.Duration{2 * time.Hour, 48 * time.Hour, 40 * 24 * time.Hour, 400 * 24 * time.Hour, 9 * 365 * 24 * time.Hour}

// Previous returns the latest run time of a cron at or before at, or the zero
// time when there is none within nine years.
func Previous(spec, zone string, at time.Time) (time.Time, error) {
	sched, loc, err := parse(spec, zone)
	if err != nil {
		return time.Time{}, err
	}
	at = at.In(loc)
	for _, back := range lookbacks {
		var last time.Time
		// Next is strictly after its argument, so start one second early to
		// include a slot exactly at the window start.
		for t := sched.Next(at.Add(-back - time.Second)); !t.IsZero() && !t.After(at); t = sched.Next(t) {
			last = t
		}
		if !last.IsZero() {
			return last.UTC(), nil
		}
	}
	return time.Time{}, nil
}

// parseStandard is cron.ParseStandard, turning the panics robfig/cron v3.0.1
// raises on some malformed input into errors, as Kubernetes does.
func parseStandard(spec string) (sched cron.Schedule, err error) {
	defer func() {
		if r := recover(); r != nil {
			sched, err = nil, fmt.Errorf("unparseable schedule: %v", r)
		}
	}()
	return cron.ParseStandard(spec)
}

// daylightSaving finds the clock changes in the year after from. At a change
// forward, wall times in the skipped hour never happen; at a change back,
// wall times in the repeated hour happen twice. The schedule is checked in
// the fixed offset under which those wall times are real instants.
//
// A schedule that runs in every hour of the day is left out: its runs keep
// their real spacing across the change (every minute stays a minute apart),
// so nothing is lost or doubled that a person would notice.
func daylightSaving(sched cron.Schedule, loc *time.Location, from time.Time) []Warning {
	if everyHour(sched) {
		return nil
	}
	var out []Warning
	end := from.AddDate(1, 0, 0)
	t := from.In(loc)
	for {
		_, change := t.ZoneBounds()
		if change.IsZero() || !change.Before(end) {
			return out
		}
		_, offBefore := change.Add(-time.Second).In(loc).Zone()
		after := change.In(loc)
		_, offAfter := after.Zone()
		date := after.Format(time.DateOnly)
		switch {
		case offAfter > offBefore:
			// The skipped wall times, as instants in the offset before the change.
			gap := time.Duration(offAfter-offBefore) * time.Second
			if w, ok := affected(sched, change, gap, time.FixedZone("", offBefore)); ok {
				w.Code, w.Date = reasons.MsgDSTSkipped, date
				out = append(out, w)
			}
		case offAfter < offBefore:
			// The repeated wall times, as instants in the offset after the change.
			overlap := time.Duration(offBefore-offAfter) * time.Second
			if w, ok := affected(sched, change, overlap, time.FixedZone("", offAfter)); ok {
				w.Code, w.Date = reasons.MsgDSTRepeated, date
				out = append(out, w)
			}
		}
		t = after
	}
}

// affected lists the runs of sched within d after change, read in the fixed
// zone: the first and last wall time and how many.
func affected(sched cron.Schedule, change time.Time, d time.Duration, fixed *time.Location) (Warning, bool) {
	var w Warning
	for r := sched.Next(change.Add(-time.Second).In(fixed)); r.Before(change.Add(d)); r = sched.Next(r) {
		if w.Runs == 0 {
			w.Time = r.Format("15:04")
		}
		w.Until = r.Format("15:04")
		w.Runs++
	}
	return w, w.Runs > 0
}

// everyHour is whether the schedule's hour field allows all 24 hours.
func everyHour(sched cron.Schedule) bool {
	s, ok := sched.(*cron.SpecSchedule)
	const day = 1<<24 - 1
	return ok && s.Hour&day == day
}

// Validate checks a cron and zone the way Next does, without computing runs.
// The error wraps ErrCronInvalid or ErrTimeZoneInvalid.
func Validate(spec, zone string) error {
	_, _, err := parse(spec, zone)
	return err
}

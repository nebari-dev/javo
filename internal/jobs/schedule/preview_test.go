package schedule

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/reasons"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNextWeekdays(t *testing.T) {
	// Friday 2026-10-30 12:00Z; Berlin is on CET (UTC+1) from 2026-10-25.
	p, err := Next("0 6 * * 1-5", "Europe/Berlin", at(t, "2026-10-30T12:00:00Z"), 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Time{at(t, "2026-11-02T05:00:00Z"), at(t, "2026-11-03T05:00:00Z"), at(t, "2026-11-04T05:00:00Z")}
	if !slices.EqualFunc(p.Next, want, time.Time.Equal) {
		t.Errorf("Next = %v, want %v", p.Next, want)
	}
	if len(p.Warnings) != 0 {
		t.Errorf("a 06:00 schedule is outside both DST changes, got warnings %v", p.Warnings)
	}
}

func TestNextRejectsBadInput(t *testing.T) {
	from := at(t, "2026-10-30T12:00:00Z")
	if _, err := Next("0 25 * * *", "Europe/Berlin", from, 1); !errors.Is(err, ErrCronInvalid) {
		t.Errorf("bad cron: err = %v, want ErrCronInvalid", err)
	}
	if _, err := Next("0 6 * * *", "Mars/Base", from, 1); !errors.Is(err, ErrTimeZoneInvalid) {
		t.Errorf("bad zone: err = %v, want ErrTimeZoneInvalid", err)
	}
}

// Observed with robfig/cron v3.0.1 (the parser the CronJob controller uses)
// for "30 1 * * *" in Europe/London: on 2027-03-28 the clock jumps 01:00 to
// 02:00 and no run fires that day (03-27 01:30Z, then 03-29 00:30Z); on
// 2027-10-31 the clock repeats 01:00 to 02:00 and the schedule fires twice
// (00:30Z and 01:30Z).
func TestNextDaylightSavingWarnings(t *testing.T) {
	spring, err := Next("30 1 * * *", "Europe/London", at(t, "2027-03-27T00:00:00Z"), 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Time{at(t, "2027-03-27T01:30:00Z"), at(t, "2027-03-29T00:30:00Z"), at(t, "2027-03-30T00:30:00Z")}
	if !slices.EqualFunc(spring.Next, want, time.Time.Equal) {
		t.Errorf("spring Next = %v, want %v", spring.Next, want)
	}
	if !slices.Contains(spring.Warnings, Warning{Code: reasons.MsgDSTSkipped, Date: "2027-03-28", Time: "01:30", Until: "01:30", Runs: 1}) {
		t.Errorf("spring warnings = %v, want DSTSkipped on 2027-03-28", spring.Warnings)
	}

	autumn, err := Next("30 1 * * *", "Europe/London", at(t, "2027-10-30T00:00:00Z"), 4)
	if err != nil {
		t.Fatal(err)
	}
	want = []time.Time{at(t, "2027-10-30T00:30:00Z"), at(t, "2027-10-31T00:30:00Z"), at(t, "2027-10-31T01:30:00Z"), at(t, "2027-11-01T01:30:00Z")}
	if !slices.EqualFunc(autumn.Next, want, time.Time.Equal) {
		t.Errorf("autumn Next = %v, want %v", autumn.Next, want)
	}
	if !slices.Contains(autumn.Warnings, Warning{Code: reasons.MsgDSTRepeated, Date: "2027-10-31", Time: "01:30", Until: "01:30", Runs: 1}) {
		t.Errorf("autumn warnings = %v, want DSTRepeated on 2027-10-31", autumn.Warnings)
	}
}

// Several runs inside the changed hour are named by the first and last.
func TestNextDaylightSavingWarningsNameEveryRunInTheHour(t *testing.T) {
	p, err := Next("15,45 1 * * *", "Europe/London", at(t, "2027-03-01T00:00:00Z"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.Warnings, Warning{Code: reasons.MsgDSTSkipped, Date: "2027-03-28", Time: "01:15", Until: "01:45", Runs: 2}) {
		t.Errorf("warnings = %+v, want DSTSkipped of 01:15 to 01:45 (2 runs)", p.Warnings)
	}
}

// A schedule that runs in every hour keeps its real spacing across a clock
// change (every minute stays a minute apart, hourly stays an hour apart), so
// there is nothing to warn about.
func TestNextNoWarningsForSchedulesThatRunEveryHour(t *testing.T) {
	for _, spec := range []string{"* * * * *", "*/5 * * * *", "30 * * * *", "0 * * * 0"} {
		p, err := Next(spec, "Europe/London", at(t, "2027-03-01T00:00:00Z"), 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Warnings) != 0 {
			t.Errorf("%s: warnings = %+v, want none", spec, p.Warnings)
		}
	}
}

func TestNextNoWarningsInZoneWithoutDST(t *testing.T) {
	p, err := Next("30 1 * * *", "Asia/Kolkata", at(t, "2027-03-27T00:00:00Z"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", p.Warnings)
	}
}

func TestNextRejectsInputKubernetesRejects(t *testing.T) {
	from := at(t, "2026-10-30T12:00:00Z")
	for _, spec := range []string{"TZ=0", "TZ=", "CRON_TZ=", "CRON_TZ=UTC", "CRON_TZ=Asia/Tokyo 0 6 * * *"} {
		if _, err := Next(spec, "Europe/Berlin", from, 1); !errors.Is(err, ErrCronInvalid) {
			t.Errorf("Next(%q): err = %v, want ErrCronInvalid", spec, err)
		}
	}
	for _, zone := range []string{"Local", "local", "LOCAL"} {
		if _, err := Next("0 6 * * *", zone, from, 1); !errors.Is(err, ErrTimeZoneInvalid) {
			t.Errorf("zone %q: err = %v, want ErrTimeZoneInvalid", zone, err)
		}
	}
}

func TestPreviousIsTheLatestSlotAtOrBefore(t *testing.T) {
	// Sunday 2026-11-01 12:00Z: the last weekday 06:00 Berlin (05:00Z) slot was Friday.
	got, err := Previous("0 6 * * 1-5", "Europe/Berlin", at(t, "2026-11-01T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if want := at(t, "2026-10-30T05:00:00Z"); !got.Equal(want) {
		t.Errorf("Previous = %v, want %v", got, want)
	}
	// A time exactly on a slot is that slot.
	got, err = Previous("0 6 * * 1-5", "Europe/Berlin", at(t, "2026-11-02T05:00:00Z"))
	if err != nil || !got.Equal(at(t, "2026-11-02T05:00:00Z")) {
		t.Errorf("Previous on a slot = %v, %v", got, err)
	}
	// A rare schedule is found across a long gap.
	got, err = Previous("0 0 29 2 *", "UTC", at(t, "2026-10-30T00:00:00Z"))
	if err != nil || !got.Equal(at(t, "2024-02-29T00:00:00Z")) {
		t.Errorf("Previous of a leap-day schedule = %v, %v", got, err)
	}
}

func TestPreviousRejectsBadInput(t *testing.T) {
	if _, err := Previous("not cron", "UTC", at(t, "2026-10-30T00:00:00Z")); !errors.Is(err, ErrCronInvalid) {
		t.Errorf("bad cron err = %v", err)
	}
	if _, err := Previous("0 6 * * *", "Mars/Base", at(t, "2026-10-30T00:00:00Z")); !errors.Is(err, ErrTimeZoneInvalid) {
		t.Errorf("bad zone err = %v", err)
	}
}

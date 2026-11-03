package dbtime_test

import (
	"database/sql/driver"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/store/dbtime"
)

func TestValueIsUnixMillis(t *testing.T) {
	at := time.Date(2026, 10, 31, 12, 0, 0, 123_456_789, time.UTC)
	v, err := dbtime.From(at).Value()
	if err != nil {
		t.Fatal(err)
	}
	if want := at.UnixMilli(); v != driver.Value(want) {
		t.Fatalf("Value() = %v, want %d", v, want)
	}
}

func TestScanRestoresMillisecondInstantInUTC(t *testing.T) {
	at := time.Date(2026, 10, 31, 12, 0, 0, 123_000_000, time.FixedZone("x", 3600))
	var got dbtime.Time
	if err := got.Scan(at.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if !got.Time().Equal(at) {
		t.Fatalf("got %v, want %v", got.Time(), at)
	}
	if got.Time().Location() != time.UTC {
		t.Fatalf("location = %v, want UTC", got.Time().Location())
	}
}

func TestScanRejectsNonIntegers(t *testing.T) {
	var got dbtime.Time
	if err := got.Scan("2026-10-31"); err == nil {
		t.Fatal("Scan(string) succeeded, want an error")
	}
}

func TestZeroTimeRoundTrips(t *testing.T) {
	v, _ := dbtime.Time{}.Value()
	var got dbtime.Time
	if err := got.Scan(v); err != nil {
		t.Fatal(err)
	}
	if !got.Time().IsZero() {
		t.Fatalf("got %v, want the zero time", got.Time())
	}
}

func TestNullTime(t *testing.T) {
	var n dbtime.NullTime
	if err := n.Scan(nil); err != nil || n.Valid {
		t.Fatalf("Scan(nil): err=%v valid=%v", err, n.Valid)
	}
	if v, err := n.Value(); err != nil || v != nil {
		t.Fatalf("Value() of null = %v, %v; want nil", v, err)
	}
	at := time.UnixMilli(1_700_000_000_000)
	if err := n.Scan(at.UnixMilli()); err != nil || !n.Valid || !n.Time.Time().Equal(at) {
		t.Fatalf("Scan(millis): err=%v valid=%v time=%v", err, n.Valid, n.Time.Time())
	}
	if v, _ := n.Value(); v != driver.Value(at.UnixMilli()) {
		t.Fatalf("Value() = %v", v)
	}
}

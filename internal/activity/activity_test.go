package activity_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/activity"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTracker() (*activity.Tracker, *clock) {
	c := &clock{t: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)}
	return activity.NewTracker(c.now), c
}

// recorder keeps what a flush wrote.
type recorder struct {
	got  []map[string]time.Time
	fail error
}

func (r *recorder) NoteActivity(_ context.Context, seen map[string]time.Time) error {
	if r.fail != nil {
		return r.fail
	}
	r.got = append(r.got, seen)
	return nil
}

func (r *recorder) last() map[string]time.Time { return r.got[len(r.got)-1] }

// A request is written once, at the next flush; a quiet app writes nothing.
func TestRequestsAreFlushedOnce(t *testing.T) {
	tr, c := newTracker()
	rec := &recorder{}
	tr.Touch("app-1")
	c.t = c.t.Add(10 * time.Second)
	tr.Touch("app-1")
	if err := tr.Flush(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := rec.last()["app-1"]; !got.Equal(c.t) {
		t.Fatalf("flushed %v, want the latest request at %v", got, c.t)
	}
	if err := tr.Flush(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 1 {
		t.Fatalf("a flush with nothing new wrote %v", rec.last())
	}
}

// An open websocket counts as use at every flush until it closes, and its
// close is the last use.
func TestOpenConnectionsKeepAnAppActive(t *testing.T) {
	tr, c := newTracker()
	rec := &recorder{}
	done := tr.Open("lab")
	for range 3 {
		c.t = c.t.Add(time.Minute)
		if err := tr.Flush(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
		if got := rec.last()["lab"]; !got.Equal(c.t) {
			t.Fatalf("open connection flushed %v, want %v", got, c.t)
		}
	}
	c.t = c.t.Add(time.Second)
	closedAt := c.t
	done()
	done() // closing twice counts once
	c.t = c.t.Add(time.Minute)
	if err := tr.Flush(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := rec.last()["lab"]; !got.Equal(closedAt) {
		t.Fatalf("closed connection flushed %v, want its close at %v", got, closedAt)
	}
	n := len(rec.got)
	if err := tr.Flush(context.Background(), rec); err != nil || len(rec.got) != n {
		t.Fatalf("a closed connection kept being flushed: %v", err)
	}
}

// A failed write is retried at the next flush, so no use is lost.
func TestFailedFlushKeepsActivity(t *testing.T) {
	tr, c := newTracker()
	tr.Touch("app-1")
	at := c.t
	if err := tr.Flush(context.Background(), &recorder{fail: errors.New("database down")}); err == nil {
		t.Fatal("want the write error")
	}
	rec := &recorder{}
	if err := tr.Flush(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := rec.last()["app-1"]; !got.Equal(at) {
		t.Fatalf("after a failed flush got %v, want %v", got, at)
	}
}

// An app reporting a last use in the future counts as used now, so its
// owner cannot keep it from ever going idle.
func TestWatchedAppsFutureLastUseCountsAsNow(t *testing.T) {
	tr, c := newTracker()
	rec := &recorder{}
	future := c.t.Add(100 * 365 * 24 * time.Hour)
	done := tr.Watch("lab", func(context.Context) (time.Time, error) { return future, nil })
	defer done()
	if err := tr.Flush(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := rec.last()["lab"]; !got.Equal(c.t) {
		t.Fatalf("future report flushed %v, want now %v", got, c.t)
	}
}

// A connection to an app that keeps its own account of use (Jupyter) is not
// use by itself: while it is open each flush writes what the app reports,
// and a report that fails counts as use, as an open connection did.
func TestWatchedConnectionsWriteTheAppsOwnLastUse(t *testing.T) {
	tr, c := newTracker()
	rec := &recorder{}
	reported := c.t
	var fail error
	probe := func(context.Context) (time.Time, error) { return reported, fail }
	done := tr.Watch("lab", probe)
	c.t = c.t.Add(10 * time.Minute)
	if err := tr.Flush(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := rec.last()["lab"]; !got.Equal(reported) {
		t.Fatalf("watched connection flushed %v, want the app's own %v", got, reported)
	}
	fail = errors.New("connection refused")
	c.t = c.t.Add(time.Minute)
	if err := tr.Flush(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := rec.last()["lab"]; !got.Equal(c.t) {
		t.Fatalf("failed report flushed %v, want now %v", got, c.t)
	}
	fail = nil
	done()
	n := len(rec.got)
	c.t = c.t.Add(time.Minute)
	if err := tr.Flush(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != n {
		t.Fatalf("a closed watched connection was flushed: %v", rec.last())
	}
}

package loop

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// once is a source that asks for the row id a single time.
func once(id string) Source {
	return SourceFunc(func(_ context.Context, enqueue func(string)) error {
		enqueue(id)
		return nil
	})
}

// A reconciler on a Loop is called for what its sources ask for, again
// after the delay it returns, again after an error and again after it
// panicked, with no manager and no cluster anywhere; what was added beside it runs too, and all of
// it ends with the context.
func TestALoopDrivesAReconcilerFromItsSources(t *testing.T) {
	var l Loop
	var calls atomic.Int32
	seen := make(chan string, 16)
	err := l.Reconcile("rows", ReconcilerFunc(func(_ context.Context, id string) (time.Duration, error) {
		seen <- id
		switch calls.Add(1) {
		case 1:
			return 10 * time.Millisecond, nil
		case 2:
			return 0, errors.New("try again")
		case 3:
			panic("a bug in the reconciler")
		}
		return 0, nil
	}), once("row-1"))
	if err != nil {
		t.Fatal(err)
	}
	beside := make(chan struct{})
	if err := l.Add(PartFunc(func(ctx context.Context) error {
		close(beside)
		<-ctx.Done()
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Start(ctx) }()
	for i := range 4 {
		select {
		case id := <-seen:
			if id != "row-1" {
				t.Fatalf("call %d was for %q", i, id)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("call %d never came", i+1)
		}
	}
	<-beside
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not end with its context")
	}
}

// A part that fails ends the whole Loop with its error: a controller that
// half runs is worse than one that stops.
func TestALoopEndsWhenAPartFails(t *testing.T) {
	var l Loop
	if err := l.Reconcile("rows", ReconcilerFunc(func(context.Context, string) (time.Duration, error) {
		return 0, nil
	})); err != nil {
		t.Fatal(err)
	}
	broken := errors.New("the feed broke")
	_ = l.Add(PartFunc(func(context.Context) error { return broken }))
	done := make(chan error, 1)
	go func() { done <- l.Start(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, broken) {
			t.Fatalf("Start = %v, want the part's error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start went on after a part failed")
	}
}

// A row is reconciled by one call at a time, and asking for it again
// while it is being reconciled is one more call afterwards, however many
// times it was asked for.
func TestALoopReconcilesARowOnceAtATime(t *testing.T) {
	var l Loop
	var running, calls atomic.Int32
	started, release := make(chan struct{}, 8), make(chan struct{})
	ask := make(chan func(string), 1)
	err := l.Reconcile("rows", ReconcilerFunc(func(context.Context, string) (time.Duration, error) {
		if running.Add(1) > 1 {
			t.Error("two calls for one row at once")
		}
		calls.Add(1)
		started <- struct{}{}
		<-release
		running.Add(-1)
		return 0, nil
	}), SourceFunc(func(_ context.Context, enqueue func(string)) error {
		ask <- enqueue
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Start(ctx) }()
	enqueue := <-ask
	enqueue("row-1")
	<-started
	for range 5 {
		enqueue("row-1")
	}
	close(release)
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the row asked for during its call was not reconciled again")
	}
	select {
	case <-started:
		t.Fatal("five asks during one call made more than one call")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start = %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("%d calls, want 2", n)
	}
}

// A source that cannot start ends the Loop with its error.
func TestALoopEndsWhenASourceCannotStart(t *testing.T) {
	var l Loop
	broken := errors.New("no feed")
	err := l.Reconcile("rows", ReconcilerFunc(func(context.Context, string) (time.Duration, error) { return 0, nil }),
		SourceFunc(func(context.Context, func(string)) error { return broken }))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Start(context.Background()); !errors.Is(err, broken) {
		t.Fatalf("Start = %v, want the source's error", err)
	}
}

package loop

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// once is a source that asks for the row id a single time.
func once(id string) source.TypedSource[reconcile.Request] {
	return source.Func(func(_ context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
		q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: id}})
		return nil
	})
}

// A reconciler on a Loop is called for what its sources ask for, again
// after the delay it returns, and again after an error, with no manager
// and no cluster anywhere; what was added beside it runs too, and all of
// it ends with the context.
func TestALoopDrivesAReconcilerFromItsSources(t *testing.T) {
	var l Loop
	var calls atomic.Int32
	seen := make(chan string, 16)
	err := l.Reconcile("rows", reconcile.Func(func(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
		seen <- req.Name
		switch calls.Add(1) {
		case 1:
			return reconcile.Result{RequeueAfter: 10 * time.Millisecond}, nil
		case 2:
			return reconcile.Result{}, errors.New("try again")
		}
		return reconcile.Result{}, nil
	}), once("row-1"))
	if err != nil {
		t.Fatal(err)
	}
	beside := make(chan struct{})
	if err := l.Add(manager.RunnableFunc(func(ctx context.Context) error {
		close(beside)
		<-ctx.Done()
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Start(ctx) }()
	for i := range 3 {
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
	if err := l.Reconcile("rows", reconcile.Func(func(context.Context, reconcile.Request) (reconcile.Result, error) {
		return reconcile.Result{}, nil
	})); err != nil {
		t.Fatal(err)
	}
	broken := errors.New("the feed broke")
	_ = l.Add(manager.RunnableFunc(func(context.Context) error { return broken }))
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

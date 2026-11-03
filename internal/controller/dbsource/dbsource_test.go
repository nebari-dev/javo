package dbsource_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func req(id string) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Name: id}}
}

func start(t *testing.T, src source.TypedSource[reconcile.Request]) workqueue.TypedRateLimitingInterface[reconcile.Request] {
	t.Helper()
	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); q.ShutDown() })
	if err := src.Start(ctx, q); err != nil {
		t.Fatal(err)
	}
	return q
}

func got(t *testing.T, q workqueue.TypedRateLimitingInterface[reconcile.Request], n int) map[reconcile.Request]bool {
	t.Helper()
	out := map[reconcile.Request]bool{}
	deadline := time.After(2 * time.Second)
	for len(out) < n {
		done := make(chan reconcile.Request, 1)
		go func() {
			r, shutdown := q.Get()
			if !shutdown {
				q.Done(r)
				done <- r
			}
		}()
		select {
		case r := <-done:
			out[r] = true
		case <-deadline:
			t.Fatalf("got %v, want %d requests", out, n)
		}
	}
	return out
}

func none(t *testing.T, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	if q.Len() != 0 {
		t.Fatalf("queue has %d items, want none", q.Len())
	}
}

func noAll(context.Context) ([]reconcile.Request, error) { return nil, nil }

func TestChangeForTheTableIsEnqueued(t *testing.T) {
	feed := make(chan store.Change, 4)
	q := start(t, dbsource.For(feed, "apps", time.Hour, noAll))
	feed <- store.Change{Table: "jobs", ID: "j1"}
	feed <- store.Change{Table: "apps", ID: "a1"}
	if r := got(t, q, 1); !r[req("a1")] {
		t.Fatalf("got %v", r)
	}
	none(t, q)
}

// afterStartup returns an all func that lists nothing on the start-up call
// (the first), then returns reqs, and counts its calls.
func afterStartup(calls *atomic.Int32, reqs ...reconcile.Request) func(context.Context) ([]reconcile.Request, error) {
	return func(context.Context) ([]reconcile.Request, error) {
		if calls.Add(1) == 1 {
			return nil, nil
		}
		return reqs, nil
	}
}

func TestResyncEnqueuesAll(t *testing.T) {
	feed := make(chan store.Change, 4)
	var calls atomic.Int32
	q := start(t, dbsource.For(feed, "apps", time.Hour, afterStartup(&calls, req("a1"), req("a2"))))
	none(t, q) // start-up listing returned nothing
	feed <- store.Resync
	if r := got(t, q, 2); !r[req("a1")] || !r[req("a2")] {
		t.Fatalf("got %v", r)
	}
}

// With no feed traffic at all, the ticker alone enqueues again after the
// start-up listing.
func TestPeriodicResyncEnqueuesAll(t *testing.T) {
	feed := make(chan store.Change)
	var calls atomic.Int32
	all := func(context.Context) ([]reconcile.Request, error) {
		calls.Add(1)
		return []reconcile.Request{req("a1")}, nil
	}
	q := start(t, dbsource.For(feed, "apps", 50*time.Millisecond, all))
	got(t, q, 1) // start-up listing
	if r := got(t, q, 1); !r[req("a1")] {
		t.Fatalf("got %v", r)
	}
	if n := calls.Load(); n < 2 {
		t.Fatalf("all called %d times, want at least 2", n)
	}
}

// A failing listing is skipped, not fatal: the start-up listing fails, nothing
// is enqueued, and the next Resync lists again.
func TestResyncListErrorIsSurvived(t *testing.T) {
	feed := make(chan store.Change, 4)
	var calls atomic.Int32
	all := func(context.Context) ([]reconcile.Request, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("db down")
		}
		return []reconcile.Request{req("a1")}, nil
	}
	q := start(t, dbsource.For(feed, "apps", time.Hour, all))
	none(t, q)
	if n := calls.Load(); n != 1 {
		t.Fatalf("all called %d times before any Resync, want 1", n)
	}
	feed <- store.Resync
	if r := got(t, q, 1); !r[req("a1")] {
		t.Fatalf("got %v", r)
	}
}

// Two controllers watching different tables both get what concerns them from
// one feed; neither steals the other's changes.
func TestFeedFansOutToEverySubscriber(t *testing.T) {
	in := make(chan store.Change, 4)
	f := dbsource.NewFeed(in)
	apps, jobs := f.Subscribe(), f.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go f.Run(ctx)
	qa := start(t, dbsource.For(apps, "apps", time.Hour, noAll))
	qj := start(t, dbsource.For(jobs, "jobs", time.Hour, noAll))
	in <- store.Change{Table: "apps", ID: "a1"}
	in <- store.Change{Table: "jobs", ID: "j1"}
	if r := got(t, qa, 1); !r[req("a1")] {
		t.Fatalf("apps got %v", r)
	}
	if r := got(t, qj, 1); !r[req("j1")] {
		t.Fatalf("jobs got %v", r)
	}
}

// A gateway write in the same process reaches the controller's source.
func TestLocalFeedFanOut(t *testing.T) {
	s := storetest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := s.StartFeed(ctx); err != nil {
		t.Fatal(err)
	}
	f := dbsource.NewFeed(s.Changes())
	sub := f.Subscribe()
	go f.Run(ctx)
	q := start(t, dbsource.For(sub, "apps", time.Hour, noAll))
	app, err := s.Apps().Create(ctx, model.Actor{Person: "alice"}, &model.App{
		Namespace: "javo-apps", Name: "lab", Owner: "alice", Template: "jupyterlab", DisplayName: "lab", Size: "small",
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := got(t, q, 1); !r[req(app.ID)] {
		t.Fatalf("got %v, want %s", r, app.ID)
	}
}

// Rows changed while no controller was running are listed once at start, not
// at the first periodic resync.
func TestStartListsEverythingOnce(t *testing.T) {
	feed := make(chan store.Change)
	all := func(context.Context) ([]reconcile.Request, error) { return []reconcile.Request{req("a1")}, nil }
	q := start(t, dbsource.For(feed, "apps", time.Hour, all))
	if r := got(t, q, 1); !r[req("a1")] {
		t.Fatalf("got %v", r)
	}
}

// A change to a table other rows depend on (templates, people) enqueues the
// requests expand names, not the changed row.
func TestFanEnqueuesWhatTheChangeAffects(t *testing.T) {
	feed := make(chan store.Change, 4)
	expand := func(_ context.Context, id string) ([]reconcile.Request, error) {
		if id != "lab" {
			return nil, nil
		}
		return []reconcile.Request{req("a1"), req("a2")}, nil
	}
	q := start(t, dbsource.Fan(feed, "templates", expand))
	feed <- store.Change{Table: "apps", ID: "lab"}
	feed <- store.Change{Table: "templates", ID: "other"}
	feed <- store.Change{Table: "templates", ID: "lab"}
	if r := got(t, q, 2); !r[req("a1")] || !r[req("a2")] {
		t.Fatalf("got %v", r)
	}
	none(t, q)
}

func TestFanSurvivesAFailingExpand(t *testing.T) {
	feed := make(chan store.Change, 4)
	var calls atomic.Int32
	expand := func(context.Context, string) ([]reconcile.Request, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("db down")
		}
		return []reconcile.Request{req("a1")}, nil
	}
	q := start(t, dbsource.Fan(feed, "templates", expand))
	feed <- store.Change{Table: "templates", ID: "lab"}
	feed <- store.Change{Table: "templates", ID: "lab"}
	if r := got(t, q, 1); !r[req("a1")] {
		t.Fatalf("got %v", r)
	}
}

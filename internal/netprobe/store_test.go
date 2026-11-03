package netprobe

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/store/storetest"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Facts: storetest.New(t).Facts()}
}

func TestStoreWithoutResultIsUnknown(t *testing.T) {
	got, err := newStore(t).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Unknown {
		t.Fatalf("state %q before any probe, want Unknown", got.State)
	}
}

func TestStorePutReplacesPreviousResult(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	for _, r := range []Result{
		{State: NotEnforced, CheckedAt: at, Evidence: []string{"listener 10.1.2.3 reachable"}},
		{State: Enforced, CheckedAt: at.Add(time.Hour)},
	} {
		if err := st.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
		got, err := st.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != r.State || !got.CheckedAt.Equal(r.CheckedAt) || len(got.Evidence) != len(r.Evidence) {
			t.Fatalf("read back %+v, stored %+v", got, r)
		}
	}
}

// The result is one cluster fact under Key; other facts are not touched.
func TestStorePutKeepsOtherFacts(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if err := st.Facts.Set(ctx, "other", json.RawMessage(`{"kept":true}`), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.Put(ctx, Result{State: Enforced}); err != nil {
		t.Fatal(err)
	}
	other, err := st.Facts.Get(ctx, "other")
	if err != nil || string(other.Value) != `{"kept":true}` {
		t.Fatalf("fact other = %s, %v", other.Value, err)
	}
	if _, err := st.Facts.Get(ctx, Key); err != nil {
		t.Fatalf("fact %q: %v", Key, err)
	}
}

// fakeProber answers from results in order, then repeats the last one.
type fakeProber struct {
	calls   chan struct{}
	results []Result
	n       *int
}

func (f fakeProber) Probe(context.Context) Result {
	f.calls <- struct{}{}
	r := Result{State: Enforced, CheckedAt: time.Now().UTC()}
	if f.n != nil {
		if *f.n < len(f.results) {
			r = f.results[*f.n]
		} else {
			r = f.results[len(f.results)-1]
		}
		*f.n++
	}
	return r
}

// An Unknown result must not wait out the whole interval: a slow image pull
// or API hiccup would otherwise block login-less apps for hours.
func TestLoopReprobesSoonAfterUnknown(t *testing.T) {
	st := newStore(t)
	n := 0
	fp := fakeProber{calls: make(chan struct{}, 8), n: &n, results: []Result{{State: Unknown}, {State: Enforced}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = (&Loop{Prober: fp, Store: st, Every: time.Hour, RetryMin: time.Millisecond}).Start(ctx)
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-fp.calls:
		case <-time.After(2 * time.Second):
			t.Fatalf("probe %d never ran; Unknown must retry before Every", i+1)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := st.Get(ctx); got.State == Enforced {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("store never turned Enforced after the retry")
}

// A decisive result that could not be stored must be retried soon: the store
// would otherwise read Unknown (or a stale state) for a whole interval.
func TestLoopRetriesSoonAfterAFailedPut(t *testing.T) {
	st := &failFirstPut{Store: newStore(t)}
	fp := fakeProber{calls: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = (&Loop{Prober: fp, Store: st, Every: time.Hour, RetryMin: time.Millisecond}).Start(ctx)
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-fp.calls:
		case <-time.After(2 * time.Second):
			t.Fatalf("probe %d never ran; a failed Put must retry before Every", i+1)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := st.Get(ctx); got.State == Enforced {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("store never turned Enforced after the failed Put")
}

// failFirstPut fails the first Put, as a database that is briefly unreachable.
type failFirstPut struct {
	*Store
	failed bool
}

func (f *failFirstPut) Put(ctx context.Context, r Result) error {
	if !f.failed {
		f.failed = true
		return errors.New("database hiccup")
	}
	return f.Store.Put(ctx, r)
}

// Retries back off (RetryMin, 2x, ...) and never exceed Every.
func TestRetryDelayBacksOffUpToEvery(t *testing.T) {
	l := &Loop{Every: 100 * time.Second, RetryMin: 30 * time.Second}
	for i, want := range []time.Duration{30 * time.Second, time.Minute, 100 * time.Second, 100 * time.Second} {
		if got := l.retryDelay(i); got != want {
			t.Errorf("retry %d: %s, want %s", i, got, want)
		}
	}
}

func TestLoopProbesAtStartThenEveryInterval(t *testing.T) {
	st := newStore(t)
	fp := fakeProber{calls: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (&Loop{Prober: fp, Store: st, Every: 5 * time.Millisecond}).Start(ctx) }()
	for i := 0; i < 3; i++ {
		select {
		case <-fp.calls:
		case <-time.After(2 * time.Second):
			t.Fatalf("probe %d never ran", i+1)
		}
	}
	got, err := st.Get(ctx)
	if err != nil || got.State != Enforced {
		t.Fatalf("stored %+v, %v", got, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start returned %v on cancel", err)
	}
}

// Package activity keeps when each app was last used through the gateway's
// proxy, in memory, and writes it to the database now and then (never per
// request). Use is a request or an open connection such as a websocket. An
// app that keeps its own account of use (a Jupyter server) is asked instead
// while a connection to it is open (Watch): a JupyterLab tab holds
// websockets for as long as it is open, used or not.
package activity

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// FlushEvery is how often a gateway writes use to the database. The idle
// check allows this much extra, since the last use may not be written yet.
const FlushEvery = 30 * time.Second

// Writer stores when apps (by id) were last used; *store.Apps satisfies it.
// It must never move an app's time back, so gateway replicas may write in
// any order.
type Writer interface {
	NoteActivity(ctx context.Context, seen map[string]time.Time) error
}

// Tracker records use per app id. The zero value is not usable; call
// NewTracker.
type Tracker struct {
	now func() time.Time

	mu   sync.Mutex
	seen map[string]time.Time // used since the last flush
	open map[string]int       // open connections
	// watched are apps with open connections that are not use by
	// themselves: the count and the latest probe.
	watched map[string]*watch
}

// Probe asks an app when it was last used, by its own account.
type Probe func(ctx context.Context) (time.Time, error)

type watch struct {
	n     int
	probe Probe
}

// ProbeTimeout bounds each Probe a flush makes.
const ProbeTimeout = 5 * time.Second

// NewTracker returns an empty tracker on the clock now (nil is time.Now).
func NewTracker(now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}
	return &Tracker{now: now, seen: map[string]time.Time{}, open: map[string]int{}, watched: map[string]*watch{}}
}

// Touch records a request to the app now.
func (t *Tracker) Touch(app string) {
	t.mu.Lock()
	t.seen[app] = t.now()
	t.mu.Unlock()
}

// Open records a connection to the app that stays open (an upgraded
// websocket). The app counts as used at every flush until the returned func
// is called, which records the close as its last use. Calling it again does
// nothing.
func (t *Tracker) Open(app string) (closed func()) {
	t.mu.Lock()
	t.open[app]++
	t.seen[app] = t.now()
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			if t.open[app]--; t.open[app] <= 0 {
				delete(t.open, app)
			}
			t.seen[app] = t.now()
			t.mu.Unlock()
		})
	}
}

// Watch records a connection to the app that stays open but is not use by
// itself. Until the returned func is called, every flush writes what probe
// reports, or the flush time when probe fails. Calling it again does
// nothing.
func (t *Tracker) Watch(app string, probe Probe) (closed func()) {
	t.mu.Lock()
	w := t.watched[app]
	if w == nil {
		w = &watch{}
		t.watched[app] = w
	}
	w.n++
	w.probe = probe
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			if w.n--; w.n <= 0 && t.watched[app] == w {
				delete(t.watched, app)
			}
			t.mu.Unlock()
		})
	}
}

// probeAll asks every watched app at once; an app that does not answer
// counts as used at now.
func probeAll(ctx context.Context, probes map[string]Probe, now time.Time) map[string]time.Time {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		got = make(map[string]time.Time, len(probes))
	)
	for app, probe := range probes {
		wg.Go(func() {
			pctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
			defer cancel()
			at, err := probe(pctx)
			if err != nil {
				slog.Debug("app activity probe", "app", app, "err", err)
				at = now
			}
			mu.Lock()
			got[app] = at
			mu.Unlock()
		})
	}
	wg.Wait()
	return got
}

// Flush writes what was used since the last flush, apps with an open
// connection at the current time and watched apps at what they report, to w. When the write fails the times are
// kept for the next flush.
func (t *Tracker) Flush(ctx context.Context, w Writer) error {
	t.mu.Lock()
	batch := t.seen
	t.seen = map[string]time.Time{}
	now := t.now()
	for app := range t.open {
		batch[app] = now
	}
	probes := make(map[string]Probe, len(t.watched))
	for app, w := range t.watched {
		probes[app] = w.probe
	}
	t.mu.Unlock()
	for app, at := range probeAll(ctx, probes, now) {
		if at.After(batch[app]) {
			batch[app] = at
		}
	}
	if len(batch) == 0 {
		return nil
	}
	err := w.NoteActivity(ctx, batch)
	if err != nil {
		t.mu.Lock()
		for app, at := range batch {
			if at.After(t.seen[app]) {
				t.seen[app] = at
			}
		}
		t.mu.Unlock()
	}
	return err
}

// Run flushes to w every interval until ctx ends, then once more.
func (t *Tracker) Run(ctx context.Context, w Writer, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			// The request context is gone; give the last write its own.
			last, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := t.Flush(last, w); err != nil {
				slog.Warn("flush app activity", "err", err)
			}
			cancel()
			return
		case <-tick.C:
			if err := t.Flush(ctx, w); err != nil {
				slog.Warn("flush app activity", "err", err)
			}
		}
	}
}

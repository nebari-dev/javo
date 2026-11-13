// Package dbsource turns the store's change feed into sources of rows to
// reconcile (loop.Source), one per table. It has no work queue of its own.
package dbsource

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/controller/loop"
	"github.com/nebari-dev/javo/internal/store"
)

// DefaultResync is how often every object is enqueued regardless of
// announcements, which covers a missed or dropped one.
const DefaultResync = 5 * time.Minute

// IDs is the ids of the rows keep accepts (all when keep is nil).
func IDs[T any](rows []T, id func(*T) string, keep func(*T) bool) []string {
	var ids []string
	for i := range rows {
		if keep == nil || keep(&rows[i]) {
			ids = append(ids, id(&rows[i]))
		}
	}
	return ids
}

// subscriberBuffer is how many changes wait for one subscriber. A full buffer
// drops the change; the source's periodic resync covers it.
const subscriberBuffer = 1024

// Feed shares one change channel between several sources. A bare channel
// would split the changes among them instead of giving each its own copy.
type Feed struct {
	in   <-chan store.Change
	mu   sync.Mutex
	subs []chan store.Change
}

// NewFeed wraps the store's Changes channel.
func NewFeed(in <-chan store.Change) *Feed { return &Feed{in: in} }

// Subscribe returns a channel that receives every change from Run on. Call it
// before Run.
func (f *Feed) Subscribe() <-chan store.Change {
	ch := make(chan store.Change, subscriberBuffer)
	f.mu.Lock()
	f.subs = append(f.subs, ch)
	f.mu.Unlock()
	return ch
}

// Run copies each change to every subscriber until ctx ends.
func (f *Feed) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-f.in:
			f.mu.Lock()
			for _, ch := range f.subs {
				select {
				case ch <- c:
				default:
					slog.Warn("change feed subscriber full, relying on resync", "table", c.Table, "id", c.ID)
				}
			}
			f.mu.Unlock()
		}
	}
}

// For returns a source that asks for the row a change to table names by
// its id, and for every row all lists on a Resync change and every `every`
// (DefaultResync when zero or negative). A failing all is logged and
// retried at the next resync.
func For(feed <-chan store.Change, table string, every time.Duration, all func(context.Context) ([]string, error)) loop.Source {
	if every <= 0 {
		every = DefaultResync
	}
	return &tableSource{feed: feed, table: table, every: every, all: all, expand: itself}
}

// itself is what a change to a row of a source's own table asks for: that
// row.
func itself(_ context.Context, id string) ([]string, error) { return []string{id}, nil }

// Fan returns a source for a table other rows depend on (templates, people):
// each change to it asks for the rows expand names for the changed row's
// id. It lists nothing on its own; the source of the dependent table already
// resyncs those rows. A failing expand is logged and the change dropped, which
// that resync covers.
func Fan(feed <-chan store.Change, table string, expand func(ctx context.Context, id string) ([]string, error)) loop.Source {
	return &tableSource{feed: feed, table: table, expand: expand}
}

type tableSource struct {
	feed  <-chan store.Change
	table string
	every time.Duration
	// all lists every row on a resync; nil means the source never resyncs.
	all    func(context.Context) ([]string, error)
	expand func(context.Context, string) ([]string, error)
}

func (s *tableSource) String() string { return "database changes for " + s.table }

func (s *tableSource) Start(ctx context.Context, enqueue func(id string)) error {
	go s.pump(ctx, enqueue)
	return nil
}

func (s *tableSource) pump(ctx context.Context, enqueue func(id string)) {
	// List everything once up front: rows changed while no controller was
	// running, or announcements dropped before this one took over, are only
	// in the table.
	s.resync(ctx, enqueue)
	var ticks <-chan time.Time
	if s.all != nil {
		tick := time.NewTicker(s.every)
		defer tick.Stop()
		ticks = tick.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-s.feed:
			switch c.Table {
			case s.table:
				s.change(ctx, c.ID, enqueue)
			case store.TableResync:
				s.resync(ctx, enqueue)
			}
		case <-ticks:
			s.resync(ctx, enqueue)
		}
	}
}

func (s *tableSource) change(ctx context.Context, id string, enqueue func(id string)) {
	ids, err := s.expand(ctx, id)
	if err != nil {
		slog.Warn("change: expanding failed, relying on the next resync", "table", s.table, "id", id, "error", err)
		return
	}
	for _, id := range ids {
		enqueue(id)
	}
}

func (s *tableSource) resync(ctx context.Context, enqueue func(id string)) {
	if s.all == nil {
		return
	}
	ids, err := s.all(ctx)
	if err != nil {
		slog.Warn("resync: listing failed, retrying at the next resync", "table", s.table, "error", err)
		return
	}
	for _, id := range ids {
		enqueue(id)
	}
}

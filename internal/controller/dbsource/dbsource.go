// Package dbsource turns the store's change feed into controller-runtime
// sources, one per table. It is built on source.TypedChannel and has no work
// queue of its own.
package dbsource

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/nebari-dev/javo/internal/store"
)

// DefaultResync is how often every object is enqueued regardless of
// announcements, which covers a missed or dropped one.
const DefaultResync = 5 * time.Minute

// Request is the request for the row with this id (the requests of this
// package name rows by id); none for an empty id, such as an object that
// lacks the row label.
func Request(id string) []reconcile.Request {
	if id == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: id}}}
}

// Requests is the requests for the rows keep accepts (all when keep is nil).
func Requests[T any](rows []T, id func(*T) string, keep func(*T) bool) []reconcile.Request {
	var reqs []reconcile.Request
	for i := range rows {
		if keep == nil || keep(&rows[i]) {
			reqs = append(reqs, Request(id(&rows[i]))...)
		}
	}
	return reqs
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

// For returns a source that enqueues a request named by the row id for each
// change to table, and every request all lists on a Resync change and every
// `every` (DefaultResync when zero or negative). A failing all is logged and
// retried at the next resync.
func For(feed <-chan store.Change, table string, every time.Duration, all func(context.Context) ([]reconcile.Request, error)) source.TypedSource[reconcile.Request] {
	if every <= 0 {
		every = DefaultResync
	}
	return newSource(feed, table, every, all, func(_ context.Context, id string) ([]reconcile.Request, error) {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: id}}}, nil
	})
}

// Fan returns a source for a table other rows depend on (templates, people):
// each change to it enqueues the requests expand names for the changed row's
// id. It lists nothing on its own; the source of the dependent table already
// resyncs those rows. A failing expand is logged and the change dropped, which
// that resync covers.
func Fan(feed <-chan store.Change, table string, expand func(ctx context.Context, id string) ([]reconcile.Request, error)) source.TypedSource[reconcile.Request] {
	return newSource(feed, table, 0, nil, expand)
}

func newSource(feed <-chan store.Change, table string, every time.Duration, all func(context.Context) ([]reconcile.Request, error),
	expand func(context.Context, string) ([]reconcile.Request, error)) source.TypedSource[reconcile.Request] {
	events := make(chan event.TypedGenericEvent[reconcile.Request])
	enqueue := handler.TypedFuncs[reconcile.Request, reconcile.Request]{
		GenericFunc: func(_ context.Context, e event.TypedGenericEvent[reconcile.Request], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			q.Add(e.Object)
		},
	}
	return &tableSource{
		inner: source.TypedChannel[reconcile.Request, reconcile.Request](events, enqueue),
		feed:  feed, events: events, table: table, every: every, all: all, expand: expand,
	}
}

type tableSource struct {
	inner  source.TypedSource[reconcile.Request]
	feed   <-chan store.Change
	events chan event.TypedGenericEvent[reconcile.Request]
	table  string
	every  time.Duration
	// all lists every request on a resync; nil means the source never resyncs.
	all    func(context.Context) ([]reconcile.Request, error)
	expand func(context.Context, string) ([]reconcile.Request, error)
}

func (s *tableSource) String() string { return "database changes for " + s.table }

func (s *tableSource) Start(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	if err := s.inner.Start(ctx, q); err != nil {
		return err
	}
	go s.pump(ctx)
	return nil
}

func (s *tableSource) pump(ctx context.Context) {
	// List everything once up front: rows changed while no controller was
	// running, or announcements dropped before this one took over, are only
	// in the table.
	s.resync(ctx)
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
				s.change(ctx, c.ID)
			case store.TableResync:
				s.resync(ctx)
			}
		case <-ticks:
			s.resync(ctx)
		}
	}
}

func (s *tableSource) change(ctx context.Context, id string) {
	reqs, err := s.expand(ctx, id)
	if err != nil {
		slog.Warn("change: expanding failed, relying on the next resync", "table", s.table, "id", id, "error", err)
		return
	}
	for _, r := range reqs {
		s.send(ctx, r)
	}
}

func (s *tableSource) resync(ctx context.Context) {
	if s.all == nil {
		return
	}
	reqs, err := s.all(ctx)
	if err != nil {
		slog.Warn("resync: listing failed, retrying at the next resync", "table", s.table, "error", err)
		return
	}
	for _, r := range reqs {
		s.send(ctx, r)
	}
}

func (s *tableSource) send(ctx context.Context, r reconcile.Request) {
	select {
	case s.events <- event.TypedGenericEvent[reconcile.Request]{Object: r}:
	case <-ctx.Done():
	}
}

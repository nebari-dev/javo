package storage

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/cache"

	"github.com/nebari-dev/javo/internal/files"
)

// How the gateway keeps folder listings so a picker never waits twice.
const (
	// listingFresh is how long a listing is answered as current without
	// asking the home again: a picker that goes back up a folder does not
	// trigger a refresh.
	listingFresh = 5 * time.Second
	// listingKeep is how long a listing may be shown, marked stale, while a
	// fresh one is fetched. A home folder rarely changes beyond recognition
	// in half an hour, and the fresh listing replaces it within seconds.
	listingKeep = 30 * time.Minute
	// listingSlots bounds the cache. Only listings of up to listingMaxEntries
	// entries are kept, so it holds a few tens of MB at most.
	listingSlots      = 256
	listingMaxEntries = 1000
	// listingRetry is how often a background refresh asks again while the
	// file pod starts, and listingGiveUp when it stops asking.
	listingRetry  = 300 * time.Millisecond
	listingGiveUp = time.Minute
	// listingWait is how long a request for a folder nothing is cached for
	// waits for a starting file pod, so the listing comes back on the same
	// request as soon as the pod is ready.
	listingWait = 2 * time.Second
	// WarmEvery is the least time between two warm-ups of one user's home:
	// each costs a few API server calls, and pages ask on every visit.
	WarmEvery = 10 * time.Second
)

type listingKey struct{ user, ns, path string }

type cachedListing struct {
	listing files.Listing
	at      time.Time
}

// Listings answers folder listings from a short per-user cache: a
// repeat open shows the last listing at once, marked stale, while a fresh
// one is fetched in the background. It passes every other call to the
// homes it wraps.
type Listings struct {
	Homes
	now func() time.Time

	mu      sync.Mutex
	entries *cache.LRUExpireCache // listingKey -> cachedListing
	// inflight holds a channel per refresh under way, closed when it ends.
	inflight map[listingKey]chan struct{}
	// gen counts each user's invalidations, so a refresh that started
	// before one does not store what it read.
	gen map[string]uint64
	// warmed is when each user's home was last warmed up.
	warmed map[string]time.Time
}

type clockFunc func() time.Time

func (f clockFunc) Now() time.Time { return f() }

// NewListings caches the listings of h on the clock now.
func NewListings(h Homes, now func() time.Time) *Listings {
	return &Listings{
		Homes: h, now: now,
		entries:  cache.NewLRUExpireCacheWithClock(listingSlots, clockFunc(now)),
		inflight: map[listingKey]chan struct{}{},
		gen:      map[string]uint64{},
		warmed:   map[string]time.Time{},
	}
}

// Lookup returns the listing of rel in user's home in namespace ns. A
// listing older than listingFresh comes back with stale set and its age,
// and a refresh starts. With nothing cached it asks the home, so the error
// is the home's (files.ErrHomeStarting while the file pod starts, and
// then a background refresh fills the cache as soon as it is ready).
func (h *Listings) Lookup(ctx context.Context, user, ns, rel string) (l files.Listing, age time.Duration, stale bool, err error) {
	k := listingKey{user, ns, rel}
	h.mu.Lock()
	v, ok := h.entries.Get(k)
	h.mu.Unlock()
	if ok {
		c := v.(cachedListing)
		age = h.now().Sub(c.at)
		if age < listingFresh {
			return c.listing, age, false, nil
		}
		h.refresh(k)
		return c.listing, age, true, nil
	}
	gen := h.generation(user)
	l, err = h.Homes.List(files.WithNamespace(ctx, ns), user, rel)
	if errors.Is(err, files.ErrHomeStarting) {
		// Keep asking in the background and wait a little for the answer;
		// if the pod takes longer the listing is cached for the next ask.
		select {
		case <-h.refresh(k):
		case <-time.After(listingWait):
		case <-ctx.Done():
		}
		if v, ok := h.entries.Get(k); ok {
			c := v.(cachedListing)
			return c.listing, h.now().Sub(c.at), false, nil
		}
	}
	if err != nil {
		return files.Listing{}, 0, false, err
	}
	h.store(k, gen, l)
	return l, 0, false, nil
}

func (h *Listings) generation(user string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.gen[user]
}

func (h *Listings) store(k listingKey, gen uint64, l files.Listing) {
	if len(l.Entries) > listingMaxEntries {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.gen[k.user] == gen {
		h.entries.Add(k, cachedListing{listing: l, at: h.now()}, listingKeep)
	}
}

// refresh lists k again in the background unless that is already under
// way, and returns a channel closed when that refresh ends. While the file pod starts it asks again every listingRetry; a
// listing that cannot be read any more (the folder is gone, the home is
// busy) is dropped so the next request reports why.
func (h *Listings) refresh(k listingKey) <-chan struct{} {
	h.mu.Lock()
	if done, ok := h.inflight[k]; ok {
		h.mu.Unlock()
		return done
	}
	done := make(chan struct{})
	h.inflight[k] = done
	gen := h.gen[k.user]
	h.mu.Unlock()

	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.inflight, k)
			h.mu.Unlock()
			close(done)
		}()
		ctx, cancel := context.WithTimeout(files.WithNamespace(context.Background(), k.ns), listingGiveUp)
		defer cancel()
		for {
			l, err := h.Homes.List(ctx, k.user, k.path)
			switch {
			case err == nil:
				h.store(k, gen, l)
				return
			case errors.Is(err, files.ErrHomeStarting):
				select {
				case <-ctx.Done():
					return
				case <-time.After(listingRetry):
				}
			default:
				if !errors.Is(err, files.ErrNotFound) && !errors.Is(err, files.ErrHomeBusy) {
					slog.Warn("refresh home folder listing", "user", k.user, "path", k.path, "err", err)
				}
				h.mu.Lock()
				h.entries.Remove(k)
				h.mu.Unlock()
				return
			}
		}
	}()
	return done
}

// Warm gets user's home folder listed ahead of the picker: it starts the
// file pod if needed and caches the listing once it is ready. It is a hint,
// so it does nothing when the user warmed up less than WarmEvery ago or a
// current listing is cached.
func (h *Listings) Warm(user, ns string) {
	now := h.now()
	h.mu.Lock()
	if last, ok := h.warmed[user]; ok && now.Sub(last) < WarmEvery {
		h.mu.Unlock()
		return
	}
	if len(h.warmed) >= listingSlots {
		for u, t := range h.warmed {
			if now.Sub(t) >= WarmEvery {
				delete(h.warmed, u)
			}
		}
	}
	h.warmed[user] = now
	k := listingKey{user, ns, ""}
	v, ok := h.entries.Get(k)
	h.mu.Unlock()
	if ok && now.Sub(v.(cachedListing).at) < listingFresh {
		return
	}
	h.refresh(k)
}

// forget drops every cached listing of user and stops refreshes under way
// from storing theirs: their home is about to change. Their next warm-up is
// not held back, as the file pod is about to be deleted.
func (h *Listings) forget(user string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gen[user]++
	delete(h.warmed, user)
	h.entries.RemoveAll(func(k any) bool { return k.(listingKey).user == user })
}

// Release forgets the user's listings, then frees their home for an app:
// the app may write to it.
func (h *Listings) Release(ctx context.Context, user string) error {
	h.forget(user)
	return h.Homes.Release(ctx, user)
}

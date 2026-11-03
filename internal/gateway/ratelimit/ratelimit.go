// Package ratelimit holds the gateway's token buckets (sign-in attempts,
// webhook deliveries) and finds the client address they are keyed on.
package ratelimit

import (
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Login attempt limits. Every attempt counts, right or wrong, which also
// bounds the password-hashing work one client can cause. Guesses at one
// username from one address hit the tight limit, so a guesser locks out
// only its own address; the looser per-username cap bounds guesses spread
// over many addresses.
var (
	perIPLimit     = Spec{Every: 2 * time.Second, Burst: 20}
	perUserIPLimit = Spec{Every: 12 * time.Second, Burst: 5}
	perUserLimit   = Spec{Every: 3 * time.Second, Burst: 30}
)

const (
	limiterIdle  = 15 * time.Minute // an entry idle this long is full again
	limiterSweep = time.Minute
)

// Spec is a bucket's refill interval and size.
type Spec struct {
	Every time.Duration
	Burst int
}

type limitEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

// Bucket is one limit applied to one key.
type Bucket struct {
	Key  string
	Spec Spec
}

// Buckets is a set of token buckets by key; entries idle for limiterIdle are
// dropped. Every limiter of the gateway is one of these.
type Buckets struct {
	mu        sync.Mutex
	entries   map[string]*limitEntry
	lastSweep time.Time
	now       func() time.Time
}

// NewBuckets returns an empty set on the real clock.
func NewBuckets() *Buckets {
	return &Buckets{entries: map[string]*limitEntry{}, now: time.Now}
}

// Take takes one token from each bucket. If any is empty it takes nothing
// and returns how long to wait.
func (b *Buckets) Take(wanted ...Bucket) (time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if now.Sub(b.lastSweep) >= limiterSweep {
		for k, e := range b.entries {
			if now.Sub(e.seen) >= limiterIdle {
				delete(b.entries, k)
			}
		}
		b.lastSweep = now
	}
	rs := make([]*rate.Reservation, len(wanted))
	var wait time.Duration
	for i, w := range wanted {
		rs[i] = b.reserve(w.Key, w.Spec, now)
		wait = max(wait, rs[i].DelayFrom(now))
	}
	if wait > 0 {
		for _, r := range rs {
			r.CancelAt(now)
		}
		return wait, false
	}
	return 0, true
}

func (b *Buckets) reserve(key string, spec Spec, now time.Time) *rate.Reservation {
	e, ok := b.entries[key]
	if !ok {
		e = &limitEntry{lim: rate.NewLimiter(rate.Every(spec.Every), spec.Burst)}
		b.entries[key] = e
	}
	e.seen = now
	return e.lim.ReserveN(now, 1)
}

// Login is a token bucket per client IP, per (username, client IP)
// and per username.
type Login struct{ *Buckets }

// NewLogin returns an empty login limiter.
func NewLogin() *Login { return &Login{NewBuckets()} }

// Allow takes one attempt from each of the three buckets.
func (l *Login) Allow(ip, username string) (time.Duration, bool) {
	return l.Take(
		Bucket{"ip:" + ip, perIPLimit},
		Bucket{"userip:" + username + "\x00" + ip, perUserIPLimit},
		Bucket{"user:" + username, perUserLimit},
	)
}

// ClientIP is the address limits are keyed on. When the peer is a
// trusted proxy it is the rightmost X-Forwarded-For entry that is not a
// trusted proxy: entries left of it were written by the client and can be
// forged. A malformed entry, which a trusted proxy would not write, falls
// back to the peer.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	addr := peer.Addr().Unmap()
	if !isTrusted(addr, trusted) {
		return addr.String()
	}
	var hops []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(line, ",")...)
	}
	client := addr
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return addr.String()
		}
		client = hop.Unmap()
		if !isTrusted(client, trusted) {
			break
		}
	}
	return client.String()
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

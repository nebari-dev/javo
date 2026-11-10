// Package ratelimit holds the gateway's token buckets (sign-in attempts,
// webhook deliveries) and finds the client address they are keyed on.
package ratelimit

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Login attempt limits per address. Every attempt counts, right or wrong,
// which also bounds the password-hashing work one client can cause. Guesses
// at one username from one address hit the tight limit, so a guesser locks
// out only its own address. An IPv6 client is one /64.
var (
	perIPLimit     = Spec{Every: 2 * time.Second, Burst: 20}
	perUserIPLimit = Spec{Every: 12 * time.Second, Burst: 5}
)

// Failed sign-ins at one username, from any address, slow its next attempts
// instead of refusing them: after freeFailures, each attempt waits
// firstDelay, doubling with every further failure up to maxDelay. A refusal
// would let anyone keep the person out by guessing wrong from a few
// addresses; a capped wait still lets the right password in.
const (
	freeFailures = 5
	firstDelay   = 500 * time.Millisecond
	maxDelay     = 8 * time.Second
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

// Login limits sign-in attempts: token buckets per client address and per
// (username, client address), and a growing wait per username after failures.
type Login struct {
	*Buckets
	sleep func(ctx context.Context, d time.Duration) error

	mu     sync.Mutex
	failed map[string]failures
}

// failures is the recent failed sign-ins at one username.
type failures struct {
	n    int
	last time.Time
}

// NewLogin returns an empty login limiter.
func NewLogin() *Login {
	return &Login{Buckets: NewBuckets(), sleep: sleepCtx, failed: map[string]failures{}}
}

// Allow takes one attempt from the per-address buckets.
func (l *Login) Allow(ip, username string) (time.Duration, bool) {
	addr := addrKey(ip)
	return l.Take(
		Bucket{"ip:" + addr, perIPLimit},
		Bucket{"userip:" + username + "\x00" + addr, perUserIPLimit},
	)
}

// Delay is how long the next attempt at username waits before its password
// is checked.
func (l *Login) Delay(username string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.current(username)
	if f.n < freeFailures {
		return 0
	}
	if k := f.n - freeFailures; k < 8 {
		return min(firstDelay<<k, maxDelay)
	}
	return maxDelay
}

// Wait sleeps for Delay(username), or until ctx ends.
func (l *Login) Wait(ctx context.Context, username string) error {
	if d := l.Delay(username); d > 0 {
		return l.sleep(ctx, d)
	}
	return nil
}

// Failed records a wrong password for username.
func (l *Login) Failed(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	f := l.current(username)
	l.failed[username] = failures{n: f.n + 1, last: now}
	if len(l.failed) > 1024 {
		for k, f := range l.failed {
			if now.Sub(f.last) >= limiterIdle {
				delete(l.failed, k)
			}
		}
	}
}

// Succeeded ends the wait for username.
func (l *Login) Succeeded(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failed, username)
}

// current is username's failures; none after limiterIdle without one.
func (l *Login) current(username string) failures {
	f := l.failed[username]
	if l.now().Sub(f.last) >= limiterIdle {
		return failures{}
	}
	return f
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// addrKey is the key of a client address: an IPv6 address by its /64, which
// one client usually holds whole.
func addrKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Is6() || a.Is4In6() {
		return ip
	}
	p, err := a.Prefix(64)
	if err != nil {
		return ip
	}
	return p.String()
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

// TrustedPeer reports whether r came straight from a trusted proxy, whose
// X-Forwarded headers are believed.
func TrustedPeer(r *http.Request, trusted []netip.Prefix) bool {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	return err == nil && isTrusted(peer.Addr().Unmap(), trusted)
}

// SharedPeer reports whether r came straight from a private or loopback
// address while no proxy is trusted: most likely an ingress or load balancer
// that every client comes through, so its address tells no client apart and a
// bucket keyed on it would be one bucket for everybody.
func SharedPeer(r *http.Request, trusted []netip.Prefix) bool {
	if len(trusted) > 0 {
		return false
	}
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	a := peer.Addr().Unmap()
	return a.IsPrivate() || a.IsLoopback()
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

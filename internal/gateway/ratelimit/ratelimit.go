// Package ratelimit holds the gateway's token buckets (sign-in attempts,
// webhook deliveries) and finds the client address they are keyed on.
package ratelimit

import (
	"hash/maphash"
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

// Wrong passwords at one username, from any address: after LoginFailures
// of them every attempt at it is refused, the right password included, until
// loginWindow has passed since the first. A refusal is not a failure, so the
// username opens again when the window ends however often it was tried
// meanwhile, and a right password empties the count. (OWASP ASVS 2.2.1.)
//
// Usernames are counted in loginSlots slots by a keyed hash, so someone
// cycling usernames cannot grow the table past that; two usernames in one
// slot share its count.
const (
	LoginFailures = 10
	loginWindow   = 15 * time.Minute
	loginSlots    = 1 << 16
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
// (username, client address), and a count of wrong passwords per username.
type Login struct {
	*Buckets
	seed maphash.Seed

	mu     sync.Mutex
	failed map[uint64]*failures
}

// failures is the recent wrong passwords in one slot of usernames.
type failures struct {
	n     int
	first time.Time
	// checking is the attempts admitted and not settled yet. Each holds a
	// place as if it will fail, so attempts made together cannot overrun
	// the count.
	checking int
}

// NewLogin returns an empty login limiter.
func NewLogin() *Login {
	return &Login{Buckets: NewBuckets(), seed: maphash.MakeSeed(), failed: map[uint64]*failures{}}
}

// Allow takes one attempt from the per-address buckets.
func (l *Login) Allow(ip, username string) (time.Duration, bool) {
	addr := addrKey(ip)
	return l.Take(
		Bucket{"ip:" + addr, perIPLimit},
		Bucket{"userip:" + username + "\x00" + addr, perUserIPLimit},
	)
}

// Outcome is how an admitted sign-in attempt ended.
type Outcome int

const (
	// Unchecked: the password was never compared (the request ended, the
	// server was busy), so the attempt counts for nothing.
	Unchecked Outcome = iota
	Wrong
	Right
)

// Attempt admits one sign-in at username, or says how long until one is
// admitted. The caller reports the outcome through settle; only its first
// call counts.
func (l *Login) Attempt(username string) (settle func(Outcome), wait time.Duration, ok bool) {
	slot := maphash.String(l.seed, username) % loginSlots
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	f := l.failed[slot]
	if f == nil {
		f = &failures{}
		l.failed[slot] = f
	}
	if f.n > 0 && now.Sub(f.first) >= loginWindow {
		f.n = 0
	}
	if f.n >= LoginFailures {
		return nil, f.first.Add(loginWindow).Sub(now), false
	}
	if f.n+f.checking >= LoginFailures {
		return nil, time.Second, false
	}
	f.checking++
	settled := false
	return func(o Outcome) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if settled {
			return
		}
		settled = true
		f.checking--
		switch o {
		case Wrong:
			if f.n == 0 {
				f.first = l.now()
			}
			f.n++
		case Right:
			f.n = 0
		}
		if f.n == 0 && f.checking == 0 {
			delete(l.failed, slot)
		}
	}, 0, true
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

package authn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/core/registry"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
)

// defaultOutageGrace is how long a session outlives a provider outage when
// the config names no grace period.
const defaultOutageGrace = 15 * time.Minute

// outageRetry is how long, after a provider failed to answer for a session,
// that session's requests reuse the failure instead of asking again. Without
// it, requests queued behind one revalidation would each wait out the
// provider's timeout in turn.
const outageRetry = 30 * time.Second

// outageMemo remembers, per session, until when a provider outage is
// reused rather than asked about again.
type outageMemo struct {
	mu sync.Mutex
	m  map[string]time.Time
}

// active reports whether an outage recorded for key is still being reused.
func (o *outageMemo) active(key string, now time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return now.Before(o.m[key])
}

// set records an outage for key until until, dropping expired entries; until
// zero forgets key.
func (o *outageMemo) set(key string, now, until time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for k, t := range o.m {
		if !now.Before(t) {
			delete(o.m, k)
		}
	}
	if until.IsZero() {
		delete(o.m, key)
		return
	}
	if o.m == nil {
		o.m = map[string]time.Time{}
	}
	o.m[key] = until
}

// keyedLock serialises work per key. Entries exist only while held or waited on.
type keyedLock struct {
	mu sync.Mutex
	m  map[string]*keyedEntry
}

type keyedEntry struct {
	mu   sync.Mutex
	refs int
}

// lock blocks until key is free and returns the function that frees it.
func (k *keyedLock) lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*keyedEntry{}
	}
	e := k.m[key]
	if e == nil {
		e = &keyedEntry{}
		k.m[key] = e
	}
	e.refs++
	k.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		if e.refs--; e.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}

// ErrSessionEnded is what Revalidator.Check returns after it ended the
// session: the provider says the person is gone, or the provider is not
// configured any more, or an outage outlasted the grace period.
var ErrSessionEnded = errors.New("session ended")

// Revalidator asks a ui session's provider whether the person is still valid.
// The UI API and the app proxy share one, so a session is never revalidated
// twice at once, whichever of them sees it first.
type Revalidator struct {
	providers *registry.Registry[authprovider.Provider]
	sessions  session.Store
	grace     time.Duration
	now       func() time.Time
	locks     keyedLock
	outages   outageMemo
}

// NewRevalidator returns a Revalidator. A zero grace means 15 minutes; a nil
// now means time.Now.
func NewRevalidator(providers *registry.Registry[authprovider.Provider], sessions session.Store, grace time.Duration, now func() time.Time) *Revalidator {
	if now == nil {
		now = time.Now
	}
	return &Revalidator{providers: providers, sessions: sessions, grace: grace, now: now}
}

// Due is true when the provider must be asked about sess now. A session with
// no deadline is due, so a provider that forgot to set one cannot keep a
// deleted account signed in.
func (v *Revalidator) Due(sess session.Session) bool {
	return sess.RevalidateAt.IsZero() || !v.now().Before(sess.RevalidateAt)
}

// Check revalidates the ui session sess, whose ID is id, and returns the
// session to use. When the provider cannot be reached but the session is
// within its grace period it returns the session as it is and, as
// unreachableUntil, when that grace ends. ErrSessionEnded means the session
// was deleted.
func (v *Revalidator) Check(ctx context.Context, id string, sess session.Session) (fresh session.Session, unreachableUntil time.Time, err error) {
	return v.check(ctx, id, sess, v.Due)
}

// idTokenMargin is how long an ID token must still be good for to be handed
// to another service as it is.
const idTokenMargin = 30 * time.Second

// FreshIDToken returns the ui session sess, whose ID is id, with an ID token
// good for at least idTokenMargin: one about to expire is renewed with the
// provider first (a revalidation, stored like any other). A session with no
// ID token (a local account) comes back as it is, and so does one whose
// provider cannot be reached: the service it is meant for then refuses it.
func (v *Revalidator) FreshIDToken(ctx context.Context, id string, sess session.Session) (session.Session, error) {
	stale := func(s session.Session) bool {
		exp, ok := jwtExpiry(s.IDToken)
		return ok && exp.Before(v.now().Add(idTokenMargin))
	}
	if !stale(sess) {
		return sess, nil
	}
	fresh, _, err := v.check(ctx, id, sess, stale)
	return fresh, err
}

// jwtExpiry reads the exp claim of a JWT without checking its signature:
// only to decide whether to renew it. ok is false for anything else.
func jwtExpiry(raw string) (exp time.Time, ok bool) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var c struct {
		Exp *int64 `json:"exp"`
	}
	if json.Unmarshal(b, &c) != nil || c.Exp == nil {
		return time.Time{}, false
	}
	return time.Unix(*c.Exp, 0), true
}

// check is Check, asking the provider only while due says the session needs it.
func (v *Revalidator) check(ctx context.Context, id string, sess session.Session, due func(session.Session) bool) (fresh session.Session, unreachableUntil time.Time, err error) {
	end := func() (session.Session, time.Time, error) {
		if err := v.sessions.DeleteByID(ctx, id); err != nil {
			slog.Warn("delete revoked session", "err", err)
		}
		return sess, time.Time{}, ErrSessionEnded
	}
	p, ok := v.providers.Get(sess.Provider)
	if !ok { // the provider was switched off: its sessions end
		return end()
	}
	// One revalidation per session at a time: parallel calls would otherwise
	// all spend the same refresh token, and a provider that rotates tokens
	// rejects every one after the first. Those that waited see the session the
	// first one stored and need not ask the provider again.
	defer v.locks.lock(id)()
	if cur, err := v.sessions.GetByID(ctx, id); err == nil {
		sess = cur
		if !due(sess) {
			return sess, time.Time{}, nil
		}
	}
	unavailable := func() (session.Session, time.Time, error) {
		grace := v.grace
		if grace <= 0 {
			grace = defaultOutageGrace
		}
		base := sess.RevalidateAt
		if base.IsZero() {
			base = v.now()
		}
		until := base.Add(grace)
		if !v.now().Before(until) {
			return end()
		}
		return sess, until, nil
	}
	if v.outages.active(id, v.now()) {
		return unavailable()
	}
	_, fresh, err = p.Revalidate(ctx, sess)
	if !errors.Is(err, authprovider.ErrUnavailable) {
		v.outages.set(id, v.now(), time.Time{})
	}
	switch {
	case err == nil:
		if err := v.sessions.UpdateByID(ctx, id, fresh); err != nil && !errors.Is(err, session.ErrSessionChanged) {
			slog.Error("store revalidated session", "err", err)
			return sess, time.Time{}, err
		}
		return fresh, time.Time{}, nil
	case errors.Is(err, authprovider.ErrRevoked):
		return end()
	case errors.Is(err, authprovider.ErrUnavailable):
		v.outages.set(id, v.now(), v.now().Add(outageRetry))
		return unavailable()
	default:
		slog.Error("revalidate session", "provider", sess.Provider, "err", err)
		return sess, time.Time{}, err
	}
}

// Package nebi keeps the Nebi tokens of apps that get one, renewed from
// their owner's session.
package nebi

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
)

// TokenSource trades a person's OIDC ID token for their Nebi token and
// says when it expires (envnebi.Source).
type TokenSource interface {
	Token(ctx context.Context, idToken string) (string, time.Time, error)
}

// Tokens keeps the owner's Nebi token of each app that gets one
// (model.NebiTokenFor) fresh while the owner uses the app: a request of the
// owner's own session (creating, starting or restarting the app, or the
// app proxy) asks Keep, which
// trades the session's ID token for a new Nebi token once less than Renew of
// the stored one is left. The token is sealed with the public half of the
// secrets keyset before it is stored (store.NebiTokens), so the gateway
// cannot read it back and the database never holds it in clear; the
// controller copies it into the app's Secret. A nil Tokens (Nebi off)
// does nothing.
//
// Only the owner's session mints: someone the app is shared with acts in
// it with the token the owner's use left there, never with their own, and
// their use never renews it. So the token lasts at most its lifetime (24 h
// in Nebi) after the owner last used the app or signed out.
type Tokens struct {
	Store  *store.Store
	Source TokenSource
	// Renew is how much of a stored token's life must be left for it to be
	// kept; zero means 12 hours.
	Renew time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu    sync.Mutex
	tried map[string]time.Time
}

// nebiRetry bounds how often one app's token is looked at: every request of
// a page would otherwise read the database.
const nebiRetry = time.Minute

func (n *Tokens) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

// due reports whether app's token may be looked at now, and records that it
// was.
func (n *Tokens) due(appID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	if last, ok := n.tried[appID]; ok && now.Sub(last) < nebiRetry && !now.Before(last) {
		return false
	}
	if n.tried == nil {
		n.tried = map[string]time.Time{}
	}
	if len(n.tried) >= 4096 {
		for k, t := range n.tried {
			if now.Sub(t) >= nebiRetry {
				delete(n.tried, k)
			}
		}
	}
	n.tried[appID] = now
	return true
}

// Keep renews app's Nebi token when person who, using it, is its owner and
// the stored token is missing or past half its life. idToken returns the
// ID token of that person's session (renewed first when about to expire).
// Failures are logged, never with a token; the app keeps the token it has.
func (n *Tokens) Keep(ctx context.Context, app *model.App, tpl *model.Template, who string, idToken func(context.Context) (string, error)) {
	if n.wanted(app, tpl, who) {
		n.renew(ctx, app, idToken)
	}
}

// Touch is Keep for a request being served: it decides at once and renews,
// when needed, in the background, so the request never waits on Nebi.
func (n *Tokens) Touch(ctx context.Context, app *model.App, tpl *model.Template, who string, idToken func(context.Context) (string, error)) {
	if !n.wanted(app, tpl, who) {
		return
	}
	a := *app
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		n.renew(ctx, &a, idToken)
	}()
}

// wanted reports whether app's token is to be looked at for who now.
func (n *Tokens) wanted(app *model.App, tpl *model.Template, who string) bool {
	return n != nil && n.Source != nil && app != nil && who != "" && who == app.Owner && model.NebiTokenFor(app, tpl) && n.due(app.ID)
}

func (n *Tokens) renew(ctx context.Context, app *model.App, idToken func(context.Context) (string, error)) {
	renew := n.Renew
	if renew == 0 {
		renew = 12 * time.Hour
	}
	_, exp, err := n.Store.NebiTokens().Get(ctx, app.ID)
	switch {
	case err == nil && exp.Sub(n.now()) > renew:
		return
	case err != nil && !errors.Is(err, store.ErrNotFound):
		slog.Warn("nebi token: read", "app", app.ID, "err", err)
		return
	}
	id, err := idToken(ctx)
	if err != nil || id == "" {
		// A local account or an API token has no ID token: nothing to trade.
		if err != nil {
			slog.Warn("nebi token: the owner's ID token", "app", app.ID, "err", err)
		}
		return
	}
	tok, exp, err := n.Source.Token(ctx, id)
	if err != nil {
		slog.Warn("nebi token: Nebi refused", "app", app.ID, "err", err)
		return
	}
	ks, found, err := n.Store.SecretKeys().Get(ctx)
	if err != nil || !found {
		slog.Warn("nebi token: no secrets keyset to seal it with", "app", app.ID, "err", err)
		return
	}
	sealer, err := secrets.NewSealer(ks.Public)
	if err != nil {
		slog.Warn("nebi token: read the secrets keyset", "app", app.ID, "err", err)
		return
	}
	sealed, err := sealer.Seal([]byte(tok), secrets.NebiTokenAD(app.ID, app.Owner))
	if err != nil {
		slog.Warn("nebi token: seal", "app", app.ID, "err", err)
		return
	}
	if err := n.Store.NebiTokens().Put(ctx, app.ID, sealed, exp); err != nil && !errors.Is(err, store.ErrNotFound) {
		slog.Warn("nebi token: store", "app", app.ID, "err", err)
	}
}

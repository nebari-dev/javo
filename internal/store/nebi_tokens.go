package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// NebiTokens is the app_nebi_tokens table: how the owner's Nebi token, which
// only the gateway can get (it holds the owner's OIDC session), reaches the
// controller, which writes it into the app's Secret (the gateway has no
// Kubernetes Secret rights). The gateway stores it sealed with the public
// half of the secrets keyset, bound to the app (internal/secrets), so the
// table never holds it in clear and the gateway cannot read it back.
type NebiTokens struct{ s *Store }

// NebiTokens returns the app_nebi_tokens table.
func (s *Store) NebiTokens() *NebiTokens { return &NebiTokens{s} }

// Put stores the app's sealed token and when it expires, replacing any
// earlier one, and announces the app so the controller copies it. A missing
// or deleted app is ErrNotFound. A renewal is bookkeeping done while the
// owner uses the app, so it writes no audit row.
func (n *NebiTokens) Put(ctx context.Context, appID string, sealed []byte, expires time.Time) error {
	return n.s.mutate(ctx, model.Actor{}, func(q *sqlcgen.Queries) (effect, error) {
		app, err := liveApp(ctx, q, appID)
		if err != nil {
			return effect{}, err
		}
		err = q.UpsertNebiToken(ctx, sqlcgen.UpsertNebiTokenParams{
			AppID: appID, Sealed: sealed, ExpiresAt: dbtime.From(expires), SetAt: dbtime.From(now()),
		})
		return effect{changes: []Change{{Table: appKind.table, ID: app.ID}}}, err
	})
}

// Get returns the app's sealed token and its expiry; ErrNotFound when it has
// none.
func (n *NebiTokens) Get(ctx context.Context, appID string) ([]byte, time.Time, error) {
	row, err := n.s.q.GetNebiToken(ctx, appID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, ErrNotFound
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	return row.Sealed, row.ExpiresAt.Time(), nil
}

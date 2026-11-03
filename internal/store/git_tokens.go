package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// GitTokens is the app_git_tokens table: how a repository token pasted in the
// browser reaches the controller. The gateway has no Kubernetes rights, so it
// stores the token here; the controller copies it into a Secret and then
// clears the text (HandedOff), so afterwards the Secret is the only copy and
// no backup of the database holds it. A row means the app has a token. No
// audit row, change or error text carries the token.
type GitTokens struct{ s *Store }

// GitTokens returns the app_git_tokens table.
func (s *Store) GitTokens() *GitTokens { return &GitTokens{s} }

// Set stores a newly pasted token for the app, replacing any earlier one, and
// announces the app so the controller copies it. A missing or deleted app is
// ErrNotFound. It is audited.
func (g *GitTokens) Set(ctx context.Context, actor model.Actor, appID, token string) error {
	if token == "" {
		return errors.New("store: git token is empty")
	}
	return g.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		app, err := liveApp(ctx, q, appID)
		if err != nil {
			return effect{}, err
		}
		err = q.UpsertGitToken(ctx, sqlcgen.UpsertGitTokenParams{AppID: appID, Token: token, SetAt: dbtime.From(now())})
		return appKind.effect("app.git_token_set", app), err
	})
}

// Remove takes the app's token away and announces the app so the controller
// removes the Secret. The row stays, empty, with a higher generation, so a
// later paste never reuses a generation a hand-off could still hold. An app
// with no token is ErrNotFound. It is audited.
func (g *GitTokens) Remove(ctx context.Context, actor model.Actor, appID string) error {
	return g.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		app, err := readApp(ctx, q, appID)
		if err != nil {
			return effect{}, err
		}
		n, err := q.RemoveGitToken(ctx, appID)
		if err == nil && n == 0 {
			err = ErrNotFound
		}
		return appKind.effect("app.git_token_remove", app), err
	})
}

// Pending returns the token text the controller has not yet copied and its
// generation (which paste it is). has says the app has a token at all: after
// HandedOff it is true with an empty token; after Remove it is false.
func (g *GitTokens) Pending(ctx context.Context, appID string) (token string, generation int64, has bool, err error) {
	row, err := g.s.q.GetGitToken(ctx, appID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	return row.Token, row.Generation, !row.Removed, nil
}

// HandedOff clears the token text after the controller copied the paste of
// this generation to the Secret. A newer paste (a higher generation) is left
// alone. It is the controller's own bookkeeping: not audited, not announced.
func (g *GitTokens) HandedOff(ctx context.Context, appID string, generation int64) error {
	_, err := g.s.q.ClearGitToken(ctx, sqlcgen.ClearGitTokenParams{AppID: appID, Generation: generation})
	return err
}

// liveApp reads the app; one marked deleted is ErrNotFound.
func liveApp(ctx context.Context, q *sqlcgen.Queries, id string) (model.App, error) {
	app, err := readApp(ctx, q, id)
	if err == nil && !app.DeletedAt.IsZero() {
		err = ErrNotFound
	}
	return app, err
}

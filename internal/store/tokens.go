package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// APIToken is one row of the api_tokens table. ID is the short id shown in
// lists and carried in the token text; Hash is the hex SHA-256 of the whole
// token, so the token itself is never stored; Prefix is the part of the token
// text that is safe to show. Owner is the person's name. Zero times mean
// never used and never expires.
type APIToken struct {
	ID         string
	Owner      string
	Name       string
	Scope      string
	Hash       string
	Prefix     string
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
	// App is set on a token made for one app; it ends with the app.
	App *TokenApp
}

// TokenApp is the app a token was made for. Create needs only ObjectName;
// reads fill the rest.
type TokenApp struct {
	ID         string
	Namespace  string
	ObjectName string
	// Name is the name people see.
	Name string
}

// Tokens is the api_tokens table. Creating and revoking tokens is audited;
// recording a use and the expiry sweep are not (high-volume bookkeeping).
type Tokens struct{ s *Store }

// Tokens returns the api_tokens table.
func (s *Store) Tokens() *Tokens { return &Tokens{s} }

// Create stores t for the person t.Owner and, when t.App is set, the app with
// that object name. An unknown person or app is ErrNotFound. It is audited.
func (t *Tokens) Create(ctx context.Context, actor model.Actor, in APIToken) (APIToken, error) {
	err := t.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		err := insertToken(ctx, q, in)
		if errors.Is(err, sql.ErrNoRows) {
			return effect{}, ErrNotFound
		}
		return effect{audit: &auditRow{action: "token.create", kind: "token", id: in.ID, name: in.Name, owner: in.Owner}}, err
	})
	if err != nil {
		return APIToken{}, err
	}
	return t.Get(ctx, in.ID)
}

// insertToken writes t for the person t.Owner and, when t.App is set, the app
// with that object name. An unknown person or app is sql.ErrNoRows.
func insertToken(ctx context.Context, q *sqlcgen.Queries, t APIToken) error {
	person, err := q.GetPersonByName(ctx, t.Owner)
	if err != nil {
		return fmt.Errorf("owner %s: %w", t.Owner, err)
	}
	var appID sql.NullString
	if t.App != nil {
		app, err := q.GetAppByObjectName(ctx, t.App.ObjectName)
		if err != nil {
			return fmt.Errorf("app %s: %w", t.App.ObjectName, err)
		}
		appID = sql.NullString{String: app.ID, Valid: true}
	}
	return q.InsertAPIToken(ctx, sqlcgen.InsertAPITokenParams{
		ID: t.ID, PersonID: person.ID, AppID: appID, Name: t.Name, Scope: t.Scope,
		Hash: t.Hash, Prefix: t.Prefix, CreatedAt: dbtime.From(t.CreatedAt), ExpiresAt: nullTime(t.ExpiresAt),
	})
}

// Get returns the token with this id, or ErrNotFound.
func (t *Tokens) Get(ctx context.Context, id string) (APIToken, error) {
	row, err := t.s.q.GetAPIToken(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, ErrNotFound
	}
	return tokenOf(row), err
}

// List returns the person's tokens, oldest first.
func (t *Tokens) List(ctx context.Context, owner string) ([]APIToken, error) {
	rows, err := t.s.q.ListAPITokensForPerson(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := make([]APIToken, 0, len(rows))
	for _, r := range rows {
		out = append(out, tokenOf(sqlcgen.GetAPITokenRow(r)))
	}
	return out, nil
}

// Revoke deletes the owner's token id and returns it; the token of someone
// else, or none, is ErrNotFound. It is audited.
func (t *Tokens) Revoke(ctx context.Context, actor model.Actor, owner, id string) (APIToken, error) {
	var tok APIToken
	err := t.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		row, err := q.GetAPIToken(ctx, id)
		if errors.Is(err, sql.ErrNoRows) || err == nil && row.Person != owner {
			return effect{}, ErrNotFound
		}
		if err != nil {
			return effect{}, err
		}
		if n, err := q.DeleteAPIToken(ctx, id); err != nil || n == 0 {
			if err == nil {
				err = ErrNotFound
			}
			return effect{}, err
		}
		tok = tokenOf(row)
		return effect{audit: &auditRow{action: "token.revoke", kind: "token", id: id, name: tok.Name, owner: owner}}, nil
	})
	return tok, err
}

// RevokeForApp deletes the tokens made for the app, except those of scope
// keep ("" deletes them all), and returns how many. It is audited when it
// deleted any.
func (t *Tokens) RevokeForApp(ctx context.Context, actor model.Actor, appID, keep string) (int64, error) {
	return t.deleteMany(ctx, actor, "token.revoke_for_app", "app", appID, func(q *sqlcgen.Queries) (int64, error) {
		return q.DeleteAPITokensForApp(ctx, sqlcgen.DeleteAPITokensForAppParams{AppID: sql.NullString{String: appID, Valid: true}, Scope: keep})
	})
}

// ListForApps returns every token made for an app, oldest first.
func (t *Tokens) ListForApps(ctx context.Context) ([]APIToken, error) {
	rows, err := t.s.q.ListAppAPITokens(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]APIToken, 0, len(rows))
	for _, r := range rows {
		out = append(out, tokenOf(sqlcgen.GetAPITokenRow(r)))
	}
	return out, nil
}

// DeleteFor deletes every token of the person, for example when their local
// account is removed, and returns how many. It is audited when it deleted any.
func (t *Tokens) DeleteFor(ctx context.Context, actor model.Actor, owner string) (int64, error) {
	return t.deleteMany(ctx, actor, "token.delete_for", "person", owner, func(q *sqlcgen.Queries) (int64, error) {
		return q.DeleteAPITokensForPerson(ctx, owner)
	})
}

func (t *Tokens) deleteMany(ctx context.Context, actor model.Actor, action, kind, name string, del func(*sqlcgen.Queries) (int64, error)) (int64, error) {
	var n int64
	err := t.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		var err error
		if n, err = del(q); err != nil || n == 0 {
			return effect{}, err
		}
		return effect{audit: &auditRow{action: action, kind: kind, name: name, detail: map[string]int64{"tokens": n}}}, nil
	})
	return n, err
}

// Sweep deletes tokens that expired at or before now and tokens of apps marked
// deleted, and returns how many.
func (t *Tokens) Sweep(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := t.s.inTx(ctx, func(q *sqlcgen.Queries) (err error) {
		n, err = q.SweepAPITokens(ctx, nullTime(now))
		return err
	})
	return n, err
}

// Touch records that the token was used at at, unless a use was already
// recorded less than every before at.
func (t *Tokens) Touch(ctx context.Context, id string, at time.Time, every time.Duration) error {
	return t.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		return q.TouchAPIToken(ctx, sqlcgen.TouchAPITokenParams{
			LastUsedAt: nullTime(at), ID: id, LastUsedAt_2: nullTime(at.Add(-every)),
		})
	})
}

func tokenOf(r sqlcgen.GetAPITokenRow) APIToken {
	tok := APIToken{
		ID: r.ID, Owner: r.Person, Name: r.Name, Scope: r.Scope, Hash: r.Hash, Prefix: r.Prefix,
		CreatedAt: r.CreatedAt.Time(), LastUsedAt: timeOf(r.LastUsedAt), ExpiresAt: timeOf(r.ExpiresAt),
	}
	if r.AppID.Valid {
		tok.App = &TokenApp{ID: r.AppID.String, Namespace: r.AppNamespace.String, ObjectName: r.AppObjectName.String, Name: r.AppName.String}
	}
	return tok
}

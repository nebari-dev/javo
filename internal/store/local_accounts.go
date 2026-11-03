package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// LocalAccount is a username and password hash, for installs without an
// identity provider. Hash is the argon2id string; the store never reads it.
type LocalAccount struct {
	Username string
	Hash     string
	Groups   []string
	// PersonID is the id of the person row this account signs in as; empty
	// until the first login.
	PersonID string
}

// LocalAccounts is the local_accounts table.
type LocalAccounts struct{ s *Store }

// LocalAccounts returns the local_accounts table.
func (s *Store) LocalAccounts() *LocalAccounts { return &LocalAccounts{s} }

// Put creates or replaces an account. The audit row names the account, never
// the hash.
func (l *LocalAccounts) Put(ctx context.Context, actor model.Actor, a LocalAccount) error {
	groups, err := marshal(a.Groups, "[]")
	if err != nil {
		return err
	}
	return l.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		err := q.UpsertLocalAccount(ctx, sqlcgen.UpsertLocalAccountParams{
			Username: a.Username, Hash: a.Hash, Groups: groups,
			PersonID: sql.NullString{String: a.PersonID, Valid: a.PersonID != ""},
		})
		return effect{audit: &auditRow{action: "account.put", kind: "account", name: a.Username}}, err
	})
}

// Get returns the account or ErrNotFound.
func (l *LocalAccounts) Get(ctx context.Context, username string) (LocalAccount, error) {
	row, err := l.s.q.GetLocalAccount(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		return LocalAccount{}, ErrNotFound
	}
	if err != nil {
		return LocalAccount{}, err
	}
	return accountOf(row)
}

// List returns every account ordered by username.
func (l *LocalAccounts) List(ctx context.Context) ([]LocalAccount, error) {
	rows, err := l.s.q.ListLocalAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]LocalAccount, len(rows))
	for i, r := range rows {
		if out[i], err = accountOf(r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Delete removes an account, or returns ErrNotFound.
func (l *LocalAccounts) Delete(ctx context.Context, actor model.Actor, username string) error {
	return l.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		n, err := q.DeleteLocalAccount(ctx, username)
		if err == nil && n == 0 {
			err = ErrNotFound
		}
		return effect{audit: &auditRow{action: "account.delete", kind: "account", name: username}}, err
	})
}

func accountOf(r sqlcgen.LocalAccount) (LocalAccount, error) {
	a := LocalAccount{Username: r.Username, Hash: r.Hash, PersonID: r.PersonID.String}
	return a, unmarshal(r.Groups, &a.Groups)
}

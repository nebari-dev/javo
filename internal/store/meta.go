package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// Meta returns a fact about the database itself, or ErrNotFound.
func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	v, err := s.q.GetMeta(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SetMeta records a fact about the database itself (not application data, so
// it writes no audit row).
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		return q.SetMeta(ctx, sqlcgen.SetMetaParams{Key: key, Value: value, UpdatedAt: dbtime.From(now())})
	})
}

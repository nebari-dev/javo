package store

import (
	"context"
	"database/sql"
)

// DB gives the engine-parity tests the raw handle; no non-test code outside
// this package can reach it.
func (s *Store) DB() *sql.DB { return s.db }

// MigrateTo brings the schema up to version only, for tests of what a later
// migration does to rows written before it.
func (s *Store) MigrateTo(ctx context.Context, version int64) error {
	p, err := s.provider()
	if err != nil {
		return err
	}
	_, err = p.UpTo(ctx, version)
	return err
}

// ForceNextIDs makes the next calls of newID return ids in order (then fall
// back to real ones), for tests that need a collision.
func ForceNextIDs(t interface{ Cleanup(func()) }, ids ...string) {
	orig := newID
	newID = func() string {
		if len(ids) == 0 {
			return orig()
		}
		id := ids[0]
		ids = ids[1:]
		return id
	}
	t.Cleanup(func() { newID = orig })
}

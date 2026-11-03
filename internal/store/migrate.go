package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func (s *Store) provider() (*goose.Provider, error) {
	fsys, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	opts := []goose.ProviderOption{}
	if s.locker != nil {
		opts = append(opts, goose.WithSessionLocker(s.locker))
	}
	return goose.NewProvider(s.dialect, s.db, fsys, opts...)
}

// Migrate brings the schema to the version this binary embeds. Concurrent
// callers (Postgres replicas starting together) serialize on an advisory lock:
// one applies, the rest find nothing to do. Running it again is a no-op.
func (s *Store) Migrate(ctx context.Context) error {
	s.migrateMu.Lock()
	defer s.migrateMu.Unlock()
	p, err := s.provider()
	if err != nil {
		return fmt.Errorf("store: migrations: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
}

// Ready reports whether the schema is at this binary's version. A database
// that is behind (not yet migrated) or ahead (written by a newer binary) is
// not ready.
func (s *Store) Ready(ctx context.Context) error {
	s.migrateMu.Lock()
	defer s.migrateMu.Unlock()
	p, err := s.provider()
	if err != nil {
		return fmt.Errorf("store: migrations: %w", err)
	}
	current, target, err := p.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("store: schema version: %w", err)
	}
	if current != target {
		return fmt.Errorf("store: schema at version %d, binary expects %d", current, target)
	}
	return nil
}

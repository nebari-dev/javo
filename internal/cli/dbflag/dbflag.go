// Package dbflag holds the flags that say where javo's database is, and opens
// it for a command.
package dbflag

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"

	"github.com/nebari-dev/javo/internal/store"
)

// defaultPath is where the SQLite file lives on the volume the pod mounts.
const defaultPath = "/var/lib/javo/javo.db"

// Opener opens the database a command works on; tests pass one that returns
// a temporary database.
type Opener func(ctx context.Context) (*store.Store, error)

// Flags say where the database is. The Postgres address holds a password, so
// it is read from a file (a mounted Secret), never from a flag.
type Flags struct {
	Engine, Path, DSNFile string
}

// Register adds --database-engine, --database-path and --database-dsn-file to f.
func (d *Flags) Register(f *pflag.FlagSet) {
	f.StringVar(&d.Engine, "database-engine", store.EngineSQLite, "database engine: sqlite or postgres")
	f.StringVar(&d.Path, "database-path", defaultPath, "SQLite database file")
	f.StringVar(&d.DSNFile, "database-dsn-file", "", "file holding the Postgres connection string")
}

// Config is the store configuration the flags name.
func (d *Flags) Config() (store.Config, error) {
	switch d.Engine {
	case store.EngineSQLite:
		return store.Config{Engine: d.Engine, Path: d.Path}, nil
	case store.EnginePostgres:
		if d.DSNFile == "" {
			return store.Config{}, errors.New("--database-dsn-file is required with --database-engine=postgres")
		}
		dsn, err := os.ReadFile(d.DSNFile)
		if err != nil {
			return store.Config{}, fmt.Errorf("read database dsn: %w", err)
		}
		return store.Config{Engine: d.Engine, DSN: strings.TrimSpace(string(dsn))}, nil
	}
	return store.Config{}, fmt.Errorf("--database-engine %q is not sqlite or postgres", d.Engine)
}

// Open connects and brings the schema up to date; migrations are idempotent
// and serialised, so every process may call it at start.
func (d *Flags) Open(ctx context.Context) (*store.Store, error) {
	cfg, err := d.Config()
	if err != nil {
		return nil, err
	}
	s, err := store.Open(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := s.Migrate(ctx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return s, nil
}

// OrRegister returns open, or, when it is nil, registers the database flags on
// f and returns the opener they name.
func OrRegister(open Opener, f *pflag.FlagSet) Opener {
	if open != nil {
		return open
	}
	var d Flags
	d.Register(f)
	return d.Open
}

// With opens the database, runs fn and closes it.
func With(ctx context.Context, open Opener, fn func(*store.Store) error) error {
	db, err := open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	return fn(db)
}

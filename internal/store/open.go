// Package store is the only code that touches SQL. It opens SQLite (the
// default, one replica) or Postgres (HA) through database/sql, migrates the
// schema, and exposes typed methods built on sqlc-generated queries. One
// schema and one query set serve both engines; the engine is looked at only
// in this file.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// The supported engines.
const (
	EngineSQLite   = "sqlite"
	EnginePostgres = "postgres"
)

var (
	// ErrNotFound is returned when a row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict is returned when a write was made against an out of date
	// version of its row (or a row that has since finished, for a cancel).
	ErrConflict = errors.New("store: version conflict")
	// ErrExists is returned when a create names something that is taken.
	ErrExists = errors.New("store: already exists")
	// ErrNotPreview is returned when a preview method is given a row that is
	// not a preview (it has no parent).
	ErrNotPreview = errors.New("store: not a preview")
)

// Config says where the data lives. SQLite uses Path (a file on a volume one
// process owns); Postgres uses DSN. ReadOnly refuses every write at the
// database, for backups.
type Config struct {
	Engine   string
	Path     string
	DSN      string
	ReadOnly bool
}

// Store is a handle on one database. It is safe for concurrent use.
type Store struct {
	db      *sql.DB
	q       *sqlcgen.Queries
	dialect goose.Dialect
	locker  lock.SessionLocker // nil for SQLite: one process owns the file

	// migrateMu keeps this process from running two migrations at once; the
	// locker covers other processes.
	migrateMu sync.Mutex

	// changes carries announcements of committed writes (changes.go).
	changes chan Change

	// notify is chosen once in Open for the engine (notify_local.go).
	notify notifier

	// backup is chosen once in Open for the engine (backup.go).
	backup backuper

	// limits are the per-person caps (SetLimits).
	limits Limits
}

// StartFeed starts delivering announcements on Changes until ctx ends. With
// SQLite writers announce themselves and there is nothing to start. With
// Postgres it connects and LISTENs before returning, so a write made after
// StartFeed returns is seen, then keeps the connection alive in the
// background.
func (s *Store) StartFeed(ctx context.Context) error { return s.notify.start(ctx) }

// Open connects to the database named by cfg. It does not migrate.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	s := &Store{changes: make(chan Change, changeBuffer)}
	var err error
	switch cfg.Engine {
	case EngineSQLite:
		s.dialect = goose.DialectSQLite3
		s.notify = localNotifier{emit: s.emit}
		s.backup = s.vacuumInto
		s.db, err = openSQLite(cfg)
	case EnginePostgres:
		s.dialect = goose.DialectPostgres
		s.notify = &pgNotifier{dsn: cfg.DSN, emit: s.emit}
		s.backup = backupPostgres
		s.locker, err = lock.NewPostgresSessionLocker()
		if err == nil {
			s.db, err = openPostgres(cfg)
		}
	default:
		return nil, fmt.Errorf("store: unknown engine %q (want %q or %q)", cfg.Engine, EngineSQLite, EnginePostgres)
	}
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", cfg.Engine, err)
	}
	if err := s.db.PingContext(ctx); err != nil {
		_ = s.db.Close()
		return nil, fmt.Errorf("store: connect to %s: %w", cfg.Engine, err)
	}
	s.q = sqlcgen.New(s.db)
	return s, nil
}

// OpenDB opens a bare connection pool on cfg with the same settings the store
// uses, without migrating or pinging. It is for storetest, which edits rows
// the store's methods never allow; nothing else may use it.
func OpenDB(cfg Config) (*sql.DB, error) {
	switch cfg.Engine {
	case EngineSQLite:
		return openSQLite(cfg)
	case EnginePostgres:
		return openPostgres(cfg)
	}
	return nil, fmt.Errorf("store: unknown engine %q (want %q or %q)", cfg.Engine, EngineSQLite, EnginePostgres)
}

// openSQLite uses WAL (readers never block the writer), a busy timeout so
// concurrent writers queue instead of failing with SQLITE_BUSY, foreign keys,
// and IMMEDIATE write transactions (they take the write lock when they begin,
// so a transaction never fails halfway on a lock upgrade).
func openSQLite(cfg Config) (*sql.DB, error) {
	if cfg.Path == "" {
		return nil, errors.New("sqlite needs a path")
	}
	q := url.Values{}
	q.Set("_busy_timeout", "5000")
	q.Set("_txlock", "immediate")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if cfg.ReadOnly {
		q.Set("mode", "ro")
	} else {
		q.Add("_pragma", "journal_mode(WAL)")
	}
	u := url.URL{Scheme: "file", Opaque: cfg.Path, RawQuery: q.Encode()}
	return sql.Open("sqlite", u.String())
}

func openPostgres(cfg Config) (*sql.DB, error) {
	pc, err := pgx.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, err
	}
	if cfg.ReadOnly {
		pc.RuntimeParams["default_transaction_read_only"] = "on"
	}
	return stdlib.OpenDB(*pc), nil
}

// Close releases the connections.
func (s *Store) Close() error { return s.db.Close() }

// inTx runs fn in one transaction and commits when it returns nil. Every
// state-changing method goes through it.
func (s *Store) inTx(ctx context.Context, fn func(*sqlcgen.Queries) error) error {
	return s.inTxAnnounce(ctx, func(q *sqlcgen.Queries) ([]Change, error) { return nil, fn(q) })
}

// inTxAnnounce is inTx for a write that also announces changes: fn returns
// them, they go out with pg_notify inside the transaction (Postgres) and are
// published after the commit (SQLite).
func (s *Store) inTxAnnounce(ctx context.Context, fn func(*sqlcgen.Queries) ([]Change, error)) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	changes, err := fn(s.q.WithTx(tx))
	if err != nil {
		return err
	}
	if err := s.notify.inTx(ctx, tx, changes); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notify.afterCommit(changes)
	return nil
}

var now = time.Now

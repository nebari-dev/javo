// Package storetest opens a fresh, empty database for the engine named by
// JAVO_TEST_DB ("sqlite", the default, or "postgres"). The Postgres server is
// JAVO_TEST_PG_DSN (make test-db starts one); when it is unreachable the test
// fails, it never skips.
package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver for the admin connection

	"github.com/nebari-dev/javo/internal/store"
)

const (
	envEngine = "JAVO_TEST_DB"
	envPGDSN  = "JAVO_TEST_PG_DSN"
)

// Engine returns the engine under test.
func Engine() string {
	if e := os.Getenv(envEngine); e != "" {
		return e
	}
	return "sqlite"
}

// FreshConfig returns the config of a new empty database. For SQLite it is a
// file in dir; for Postgres it is a new database on the server at adminDSN.
// cleanup removes it.
func FreshConfig(ctx context.Context, engine, adminDSN, dir string) (cfg store.Config, cleanup func(), err error) {
	switch engine {
	case "sqlite":
		return store.Config{Engine: engine, Path: filepath.Join(dir, "javo.db")}, func() {}, nil
	case "postgres":
		return freshPostgres(ctx, adminDSN)
	}
	return store.Config{}, nil, fmt.Errorf("storetest: unknown engine %q in %s", engine, envEngine)
}

func freshPostgres(ctx context.Context, adminDSN string) (store.Config, func(), error) {
	return createPostgres(ctx, adminDSN, "")
}

// createPostgres makes a new database on the server at adminDSN, a copy of
// template when one is named (CREATE DATABASE ... TEMPLATE).
func createPostgres(ctx context.Context, adminDSN, template string) (store.Config, func(), error) {
	if adminDSN == "" {
		return store.Config{}, nil, fmt.Errorf("storetest: %s=postgres needs %s (run make test-db)", envEngine, envPGDSN)
	}
	u, err := url.Parse(adminDSN)
	if err != nil {
		return store.Config{}, nil, fmt.Errorf("storetest: parse %s: %w", envPGDSN, err)
	}
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		return store.Config{}, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		admin.Close()
		return store.Config{}, nil, err
	}
	name := "javo_test_" + hex.EncodeToString(b[:])
	create := "CREATE DATABASE " + name
	if template != "" {
		create += " TEMPLATE " + template
	}
	_, err = admin.ExecContext(ctx, create)
	// The admin connection is not held for the test: parallel tests would
	// otherwise each keep one open and run the server out of connections.
	admin.Close()
	if err != nil {
		return store.Config{}, nil, fmt.Errorf("storetest: postgres at %s unreachable: %w", u.Host, err)
	}
	u.Path = "/" + name
	cleanup := func() {
		admin, err := sql.Open("pgx", adminDSN)
		if err != nil {
			return
		}
		defer admin.Close()
		_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	}
	return store.Config{Engine: "postgres", DSN: u.String()}, cleanup, nil
}

// Config is FreshConfig for the engine under test; the database is removed
// when the test ends. It fails the test when the engine is unavailable.
func Config(t testing.TB) store.Config {
	t.Helper()
	cfg, cleanup, err := FreshConfig(context.Background(), Engine(), os.Getenv(envPGDSN), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return cfg
}

// configs remembers where each store opened here lives, for Exec.
var configs sync.Map // *store.Store -> store.Config

// Open opens (without migrating) a store on cfg and closes it with the test.
func Open(t testing.TB, cfg store.Config) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	configs.Store(s, cfg)
	t.Cleanup(func() { configs.Delete(s); _ = s.Close() })
	return s
}

// ConfigOf returns the config of the database s was opened on here.
func ConfigOf(t testing.TB, s *store.Store) store.Config {
	t.Helper()
	v, ok := configs.Load(s)
	if !ok {
		t.Fatal("storetest: store was not opened by storetest")
	}
	return v.(store.Config)
}

// Reopen opens another handle on the database s lives on, closed with the
// test; for code that opens its own store and closes it when done.
func Reopen(t testing.TB, s *store.Store) *store.Store {
	t.Helper()
	return Open(t, ConfigOf(t, s))
}

// Exec runs one statement straight on the database s was opened on, to set up
// what the store's own methods never allow (a login from two days ago, a
// deleted person). Write placeholders as ?; they become $1, $2 on Postgres.
func Exec(t testing.TB, s *store.Store, query string, args ...any) {
	t.Helper()
	cfg := ConfigOf(t, s)
	db, err := store.OpenDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Engine == store.EnginePostgres {
		for i := 1; strings.Contains(query, "?"); i++ {
			query = strings.Replace(query, "?", "$"+strconv.Itoa(i), 1)
		}
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

// New returns a migrated store on a fresh database. The schema is migrated
// once per test binary into a template database (a file on SQLite) and each
// New is a copy of it, which is many times faster than migrating every one.
func New(t testing.TB) *store.Store {
	t.Helper()
	if Engine() != store.EnginePostgres {
		img, err := sqliteTemplate()
		if err != nil {
			t.Fatal(err)
		}
		cfg := Config(t)
		if err := os.WriteFile(cfg.Path, img, 0o644); err != nil {
			t.Fatal(err)
		}
		return Open(t, cfg)
	}
	adminDSN := os.Getenv(envPGDSN)
	tpl, err := migratedTemplate(adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg, cleanup, err := createPostgres(context.Background(), adminDSN, tpl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return Open(t, cfg)
}

// sqliteTemplate migrates a SQLite database once per process and returns its
// file, which New copies: copying a file is much faster than migrating.
var sqliteTemplate = sync.OnceValues(func() ([]byte, error) {
	dir, err := os.MkdirTemp("", "storetest-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	cfg := store.Config{Engine: store.EngineSQLite, Path: filepath.Join(dir, "javo.db")}
	s, err := store.Open(context.Background(), cfg)
	if err != nil {
		return nil, err
	}
	err = s.Migrate(context.Background())
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("storetest: migrate the template database: %w", err)
	}
	// Closing the last connection folds the write-ahead log into the file.
	if _, err := os.Stat(cfg.Path + "-wal"); err == nil {
		return nil, errors.New("storetest: the template database kept its write-ahead log")
	}
	return os.ReadFile(cfg.Path)
})

var template struct {
	once sync.Once
	name string
	err  error
}

// migratedTemplate makes, once per process, a migrated database for New to
// copy, and returns its name. No connection to it stays open, since Postgres
// copies only a database nobody is connected to.
func migratedTemplate(adminDSN string) (string, error) {
	template.once.Do(func() {
		ctx := context.Background()
		cfg, _, err := createPostgres(ctx, adminDSN, "")
		if err != nil {
			template.err = err
			return
		}
		s, err := store.Open(ctx, cfg)
		if err != nil {
			template.err = err
			return
		}
		err = s.Migrate(ctx)
		if cerr := s.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			template.err = fmt.Errorf("storetest: migrate the template database: %w", err)
			return
		}
		u, err := url.Parse(cfg.DSN)
		if err != nil {
			template.err = err
			return
		}
		template.name = strings.TrimPrefix(u.Path, "/")
	})
	return template.name, template.err
}

package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func requirePostgres(t *testing.T) {
	t.Helper()
	if storetest.Engine() != store.EnginePostgres {
		t.Skip("Postgres-only behaviour; make test-db runs it with JAVO_TEST_DB=postgres")
	}
}

func startedStore(t *testing.T) (*store.Store, store.Config) {
	t.Helper()
	cfg := storetest.Config(t)
	s := storetest.Open(t, cfg)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := s.StartFeed(ctx); err != nil {
		t.Fatal(err)
	}
	return s, cfg
}

// A write on one connection reaches the listener only once it has committed,
// and a rolled-back write is never announced.
func TestPostgresNotifyAfterCommit(t *testing.T) {
	t.Parallel()
	requirePostgres(t)
	s, _ := startedStore(t)
	app := createApp(t, s, "alice", "lab")
	c := next(t, s)
	if c.Table != "apps" || c.ID != app.ID {
		t.Fatalf("change = %+v, want apps/%s", c, app.ID)
	}
	if _, err := s.Apps().Get(ctx, c.ID); err != nil {
		t.Fatalf("announced before commit: %v", err)
	}
	// A failed write (name taken) announces nothing.
	if _, err := s.Apps().Create(ctx, alice, newApp("alice", "lab")); err == nil {
		t.Fatal("duplicate create succeeded")
	}
	select {
	case c := <-s.Changes():
		t.Fatalf("rolled back write announced %+v", c)
	case <-time.After(300 * time.Millisecond):
	}
}

// A second Store on the same database (another replica) hears the first one's
// writes, and each write arrives once.
func TestPostgresNotifyReachesOtherReplica(t *testing.T) {
	t.Parallel()
	requirePostgres(t)
	writer, cfg := startedStore(t)
	reader := storetest.Open(t, cfg)
	rctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := reader.StartFeed(rctx); err != nil {
		t.Fatal(err)
	}
	app := createApp(t, writer, "alice", "lab")
	if c := next(t, reader); c.Table != "apps" || c.ID != app.ID {
		t.Fatalf("reader got %+v", c)
	}
	select {
	case c := <-reader.Changes():
		t.Fatalf("duplicate announcement %+v", c)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestPostgresReconnectEmitsResync(t *testing.T) {
	t.Parallel()
	requirePostgres(t)
	s, cfg := startedStore(t)
	admin, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	var killed int
	if err := admin.QueryRowContext(ctx, `SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
		WHERE datname = current_database() AND query LIKE 'LISTEN%' AND pid <> pg_backend_pid()`).Scan(&killed); err != nil {
		t.Fatal(err)
	}
	if killed != 1 {
		t.Fatalf("terminated %d listener backends, want 1", killed)
	}
	if c := next(t, s); c != store.Resync {
		t.Fatalf("after reconnect got %+v, want Resync", c)
	}
	// And the feed works again.
	app := createApp(t, s, "alice", "lab")
	if c := next(t, s); c.ID != app.ID {
		t.Fatalf("after reconnect got %+v", c)
	}
}

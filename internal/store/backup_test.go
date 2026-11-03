package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// envPGContainer names the running Postgres container make test-db started;
// the dump and restore tools run inside it, from the pinned image.
const envPGContainer = "JAVO_TEST_PG_CONTAINER"

// backupAndRestore takes a backup of the database s lives on, with the same
// tool an install uses for the engine (VACUUM INTO for SQLite, pg_dump from
// the pinned image for Postgres), restores it into a fresh database and
// returns a store opened on the copy.
func backupAndRestore(t *testing.T, s *store.Store, cfg store.Config) *store.Store {
	t.Helper()
	if cfg.Engine == store.EngineSQLite {
		file := filepath.Join(t.TempDir(), "backup.db")
		if err := s.Backup(ctx, file); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "restored.db")
		if err := store.RestoreSQLite(ctx, target, file); err != nil {
			t.Fatal(err)
		}
		return storetest.Open(t, store.Config{Engine: store.EngineSQLite, Path: target})
	}
	container := os.Getenv(envPGContainer)
	if container == "" {
		t.Fatalf("%s is not set: run make test-db", envPGContainer)
	}
	dump := pgTool(t, container, nil, "pg_dump", "--format=custom", "--dbname", containerDSN(t, cfg.DSN))
	fresh, cleanup, err := storetest.FreshConfig(ctx, store.EnginePostgres, os.Getenv("JAVO_TEST_PG_DSN"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	pgTool(t, container, dump, "pg_restore", "--no-owner", "--dbname", containerDSN(t, fresh.DSN))
	return storetest.Open(t, fresh)
}

// containerDSN is dsn as the container itself reaches the server.
func containerDSN(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = "127.0.0.1:5432"
	return u.String()
}

func pgTool(t *testing.T, container string, stdin []byte, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("docker", append([]string{"exec", "-i", container}, args...)...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v: %s", args[0], err, stderr.String())
	}
	return out.Bytes()
}

func TestBackupUnderWriteLoad(t *testing.T) {
	t.Parallel()
	cfg := storetest.Config(t)
	s := storetest.Open(t, cfg)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var before []string
	for i := range 30 {
		name := fmt.Sprintf("before-%02d", i)
		createApp(t, s, "alice", name)
		before = append(before, name)
	}

	// The writer commits as fast as it can. Its errors are kept: a backup that
	// made writers fail (SQLITE_BUSY) must fail the test.
	var (
		writes    atomic.Int64
		writerErr atomic.Value // error
		stop      = make(chan struct{})
		wg        sync.WaitGroup
	)
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := s.Apps().Create(ctx, alice, newApp("alice", fmt.Sprintf("during-%04d", i))); err != nil {
				writerErr.Store(err)
				return
			}
			writes.Add(1)
		}
	})
	// Start only once a concurrent write has committed.
	waitFor(t, func() bool { return writes.Load() > 0 || writerErr.Load() != nil })

	// A backup of a small database can end between two writes, so take
	// backups until one demonstrably overlapped a committed write.
	var restored *store.Store
	overlapped := false
	for attempt := 0; attempt < 25 && !overlapped; attempt++ {
		start := writes.Load()
		restored = backupAndRestore(t, s, cfg)
		overlapped = writes.Load() > start
	}
	close(stop)
	wg.Wait()

	if err, _ := writerErr.Load().(error); err != nil {
		t.Fatalf("a write failed while backups ran: %v", err)
	}
	if !overlapped {
		t.Fatal("no write committed while a backup was running, so the test proved nothing")
	}
	if err := restored.Ready(ctx); err != nil {
		t.Fatalf("restored database is not ready: %v", err)
	}
	apps, err := restored.Apps().ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range apps {
		got = append(got, a.Name)
	}
	for _, name := range before {
		if !slices.Contains(got, name) {
			t.Errorf("restored database lacks %s, committed before the backup started", name)
		}
	}
}

func TestRestoreRefusesOpenDatabase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := store.Config{Engine: store.EngineSQLite, Path: filepath.Join(dir, "javo.db")}
	s := storetest.Open(t, cfg)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	createApp(t, s, "alice", "lab")
	file := filepath.Join(dir, "backup.db")
	if err := s.Backup(ctx, file); err != nil {
		t.Fatal(err)
	}
	createApp(t, s, "alice", "later")

	err := store.RestoreSQLite(ctx, cfg.Path, file)
	if !errors.Is(err, store.ErrDatabaseInUse) {
		t.Fatalf("restore over an open database = %v, want ErrDatabaseInUse", err)
	}
	apps, err := s.Apps().ListAll(ctx)
	if err != nil || len(apps) != 2 {
		t.Fatalf("the open database lost data after a refused restore: %d apps, %v", len(apps), err)
	}

	// Once nothing has it open the same restore works and drops the later app.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreSQLite(ctx, cfg.Path, file); err != nil {
		t.Fatalf("restore with the database closed: %v", err)
	}
	again := storetest.Open(t, cfg)
	apps, err = again.Apps().ListAll(ctx)
	if err != nil || len(apps) != 1 || apps[0].Name != "lab" {
		t.Fatalf("after restore apps = %v, %v; want only lab", apps, err)
	}
	// Restored apps wait for an admin, so they do not all start at once.
	if apps[0].RestoreHold != "backup.db" || !apps[0].Held(time.Now()) {
		t.Fatalf("restored app hold = %q, want held under backup.db", apps[0].RestoreHold)
	}
}

func TestRestoreRefusesAFileThatIsNotADatabase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.db")
	if err := os.WriteFile(junk, []byte("this is not a database file at all, not even close"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "javo.db")
	if err := store.RestoreSQLite(ctx, target, junk); err == nil {
		t.Fatal("restore accepted a file that is not a database")
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("a refused restore left a database behind")
	}
}

func TestRestoreRefusesADatabaseWithoutTheJavoSchema(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.db")
	db, err := sql.Open("sqlite", "file:"+empty)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE unrelated (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	target := filepath.Join(dir, "javo.db")
	s := storetest.Open(t, store.Config{Engine: store.EngineSQLite, Path: target})
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreSQLite(ctx, target, empty); err == nil {
		t.Fatal("restore accepted a database with no javo schema")
	}
	if _, err := os.Stat(target + ".before-restore"); err == nil {
		t.Fatal("a refused restore moved the real database aside")
	}
	again := storetest.Open(t, store.Config{Engine: store.EngineSQLite, Path: target})
	if err := again.Ready(ctx); err != nil {
		t.Fatalf("real database damaged by a refused restore: %v", err)
	}
}

func TestBackupRefusesPostgres(t *testing.T) {
	t.Parallel()
	if storetest.Engine() != store.EnginePostgres {
		t.Skip("engine under test is not postgres")
	}
	s := storetest.New(t)
	err := s.Backup(ctx, filepath.Join(t.TempDir(), "x.db"))
	if err == nil || !strings.Contains(err.Error(), "pg_dump") {
		t.Fatalf("Backup on postgres = %v, want an error pointing to pg_dump", err)
	}
}

func TestBackupRetention(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	var names []string
	for i := range 5 {
		n := store.BackupName(base.Add(time.Duration(i) * time.Hour))
		names = append(names, n)
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := store.PruneBackups(dir, 3); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	slices.Sort(left)
	want := append(slices.Clone(names[2:]), "notes.txt")
	slices.Sort(want)
	if !slices.Equal(left, want) {
		t.Fatalf("left %v, want the three newest backups and the unrelated file %v", left, want)
	}
}

func TestScheduledBackupsRunAndPrune(t *testing.T) {
	t.Parallel()
	cfg := store.Config{Engine: store.EngineSQLite, Path: filepath.Join(t.TempDir(), "javo.db")}
	s := storetest.Open(t, cfg)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	createApp(t, s, "alice", "lab")
	dir := t.TempDir()

	runCtx, cancel := context.WithTimeout(ctx, 3500*time.Millisecond)
	defer cancel()
	err := s.RunBackups(runCtx, store.BackupPlan{Schedule: "@every 1s", Dir: dir, Keep: 2})
	if err != nil {
		t.Fatalf("RunBackups = %v, want nil when its context ends", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("%d backups on disk after three runs with keep=2, want 2", len(entries))
	}
	newest := filepath.Join(dir, entries[len(entries)-1].Name())
	target := filepath.Join(t.TempDir(), "javo.db")
	if err := store.RestoreSQLite(ctx, target, newest); err != nil {
		t.Fatal(err)
	}
	apps, err := storetest.Open(t, store.Config{Engine: store.EngineSQLite, Path: target}).Apps().ListAll(ctx)
	if err != nil || len(apps) != 1 {
		t.Fatalf("newest scheduled backup holds %d apps, %v; want 1", len(apps), err)
	}
}

// Each scheduled backup, once written and pruned, is handed to the plan's
// After (the copy to the bucket), with the file's full name.
func TestScheduledBackupsAreHandedOn(t *testing.T) {
	t.Parallel()
	cfg := store.Config{Engine: store.EngineSQLite, Path: filepath.Join(t.TempDir(), "javo.db")}
	s := storetest.Open(t, cfg)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var mu sync.Mutex
	var got []string
	// Stops once two backups were handed on; the timeout only bounds a
	// broken run, so a slow machine cannot fail the test.
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := s.RunBackups(runCtx, store.BackupPlan{Schedule: "@every 1s", Dir: dir, Keep: 5, After: func(_ context.Context, file string) {
		if _, err := os.Stat(file); err != nil {
			t.Errorf("handed %s, which is not there: %v", file, err)
		}
		mu.Lock()
		got = append(got, file)
		if len(got) == 2 {
			cancel()
		}
		mu.Unlock()
	}})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	files, err := store.ListBackups(dir)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, f := range files {
		want = append(want, filepath.Join(dir, f.Name))
	}
	slices.Sort(want)
	if len(got) < 2 || !slices.Equal(got, want) {
		t.Fatalf("handed on %v, want each backup written, %v", got, want)
	}
}

func TestBackupPlanRejectsABadSchedule(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	err := s.RunBackups(ctx, store.BackupPlan{Schedule: "not a cron", Dir: t.TempDir(), Keep: 1})
	if err == nil {
		t.Fatal("RunBackups accepted a schedule that does not parse")
	}
}

// waitFor polls cond until it holds or two seconds pass.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 2s")
		}
		time.Sleep(time.Millisecond)
	}
}

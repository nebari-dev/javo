package dbcmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/cli/clitest"
	"github.com/nebari-dev/javo/internal/store"
)

func runDBFlags(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return clitest.Run(New(nil), append([]string{"db"}, args...)...)
}

func TestDBBackupThenRestoreBringsBackWhatWasBackedUp(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "javo.db")
	s, err := store.Open(ctx, store.Config{Engine: store.EngineSQLite, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta(ctx, "greeting", "before"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "copy.db")
	// The server may be running: backup works on the open database.
	if out, err := runDBFlags(t, "backup", "--database-path", path, file); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	if err := s.SetMeta(ctx, "greeting", "after"); err != nil {
		t.Fatal(err)
	}
	// Restore is refused while it is.
	if _, err := runDBFlags(t, "restore", "--database-path", path, file); !errors.Is(err, store.ErrDatabaseInUse) {
		t.Fatalf("restore with the server running = %v, want ErrDatabaseInUse", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if out, err := runDBFlags(t, "restore", "--database-path", path, file); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	again, err := store.Open(ctx, store.Config{Engine: store.EngineSQLite, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got, err := again.Meta(ctx, "greeting"); err != nil || got != "before" {
		t.Fatalf("greeting after restore = %q, %v; want before", got, err)
	}
}

func TestDBBackupOfAMissingDatabaseCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	typo := filepath.Join(dir, "typo.db")
	out := filepath.Join(dir, "out.db")
	if _, err := runDBFlags(t, "backup", "--database-path", typo, out); err == nil {
		t.Fatal("backup of a database that does not exist succeeded")
	}
	for _, f := range []string{typo, out} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s was created by a refused backup", f)
		}
	}
}

func TestDBRestoreOfPostgresPointsToPgRestore(t *testing.T) {
	_, err := runDBFlags(t, "restore", "--database-engine", "postgres", "--database-dsn-file", "/nonexistent", "x.dump")
	if err == nil || !strings.Contains(err.Error(), "pg_restore") {
		t.Fatalf("restore on postgres = %v, want a message naming pg_restore", err)
	}
}

func TestDBUpgradeMigratesANewDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "javo.db")
	if out, err := runDBFlags(t, "upgrade", "--database-path", path); err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out)
	}
	s, err := store.Open(ctx, store.Config{Engine: store.EngineSQLite, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Ready(ctx); err != nil {
		t.Fatalf("schema after upgrade: %v", err)
	}
}

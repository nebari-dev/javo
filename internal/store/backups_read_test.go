package store_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// ListBackups lists the scheduled backups newest first, with the time from
// the name; other files are left out.
func TestListBackupsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)
	for i := range 3 {
		if err := os.WriteFile(filepath.Join(dir, store.BackupName(base.Add(time.Duration(i)*24*time.Hour))), []byte("xyz"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListBackups(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Name != store.BackupName(base.Add(48*time.Hour)) || !got[0].TakenAt.Equal(base.Add(48*time.Hour)) || got[0].Size != 3 {
		t.Fatalf("ListBackups = %+v", got)
	}
	if _, err := store.ListBackups(filepath.Join(dir, "missing")); err != nil {
		t.Fatalf("a missing directory is no backups yet, got %v", err)
	}
}

// ReadBackup reads a backup through a scratch copy brought to this schema,
// so the backup file itself is never changed, and removes the copy after.
func TestReadBackupLeavesTheFileAlone(t *testing.T) {
	cfg := store.Config{Engine: store.EngineSQLite, Path: filepath.Join(t.TempDir(), "javo.db")}
	s := storetest.Open(t, cfg)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	createApp(t, s, "alice", "lab")
	dir := t.TempDir()
	file := filepath.Join(dir, store.BackupName(time.Now()))
	if err := s.Backup(ctx, file); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(file)

	var names []string
	err := store.ReadBackup(ctx, file, func(b *store.Store) error {
		apps, err := b.Apps().ListAll(ctx)
		for _, a := range apps {
			names = append(names, a.Name)
		}
		return err
	})
	if err != nil || len(names) != 1 || names[0] != "lab" {
		t.Fatalf("ReadBackup: %v, %v", names, err)
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Error("reading the backup changed it")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("files left beside the backup: %d", len(entries))
	}
	if err := store.ReadBackup(ctx, filepath.Join(dir, "nope.db"), func(*store.Store) error { return nil }); err == nil {
		t.Error("reading a missing backup did not fail")
	}
}

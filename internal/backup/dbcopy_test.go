package backup_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/backup"
	"github.com/nebari-dev/javo/internal/backup/restic/restictest"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var ctx = context.Background()

func dbFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	f := filepath.Join(dir, name)
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// Each backup file is copied to the bucket, only the newest Keep copies
// stay there, and the outcome is recorded for the Backups page.
func TestDatabaseCopiesGoToTheBucketAndAreRecorded(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	repo := restictest.New(t, "pw")
	at := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	c := backup.DatabaseCopier{Repo: repo, Facts: db.Facts(), Keep: 2, Now: func() time.Time { return at }, Dest: "s3://b/javo/database"}

	if _, ok, err := backup.LastDatabaseCopy(ctx, db.Facts()); err != nil || ok {
		t.Fatalf("before any copy: %v, %v", ok, err)
	}
	dir := t.TempDir()
	for _, name := range []string{"javo-1.db", "javo-2.db", "javo-3.db"} {
		if err := c.Copy(ctx, dbFile(t, dir, name, "body of "+name)); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := backup.LastDatabaseCopy(ctx, db.Facts())
	if err != nil || !ok || got.File != "javo-3.db" || got.Snapshot == "" || !got.At.Equal(at) || got.Reason != "" {
		t.Fatalf("last copy = %+v, %v, %v", got, ok, err)
	}
	// What the run's log says about it: real numbers from the file and restic.
	if got.Size != int64(len("body of javo-3.db")) || got.Uploaded == 0 || got.Took <= 0 || got.Kept != 2 || got.Removed != 1 || got.Dest != "s3://b/javo/database" {
		t.Fatalf("copy details = %+v", got)
	}
	copies, err := c.List(ctx)
	if err != nil || len(copies) != 2 || copies[0].File() != "javo-3.db" {
		t.Fatalf("copies in the bucket: %+v, %v", copies, err)
	}
	var out bytes.Buffer
	if err := repo.Dump(ctx, copies[1].ID, copies[1].File(), &out); err != nil || out.String() != "body of javo-2.db" {
		t.Fatalf("dump = %q, %v", out.String(), err)
	}

	// A failed copy keeps the last good one and says why.
	c.Repo.Env = []string{"RESTIC_PASSWORD=wrong"}
	at = at.Add(24 * time.Hour)
	if err := c.Copy(ctx, dbFile(t, dir, "javo-4.db", "x")); err == nil {
		t.Fatal("copy with the wrong password worked")
	}
	got, _, err = backup.LastDatabaseCopy(ctx, db.Facts())
	if err != nil || got.File != "javo-3.db" || got.Reason != string(reasons.BackupWrongPassword) || !got.FailedAt.Equal(at) || len(got.Evidence) == 0 {
		t.Fatalf("after a failure: %+v, %v", got, err)
	}
}

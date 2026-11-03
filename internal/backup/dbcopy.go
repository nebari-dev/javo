// Package backup copies the database to the backup bucket and keeps what
// the Backups page says about it. The home folders' backups are Jobs the
// controller runs (internal/controller/storage); both use package restic.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nebari-dev/javo/internal/backup/restic"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// DatabaseRepo is the repository of the database copies under the bucket's
// prefix, and DatabaseTag the tag of each copy.
const (
	DatabaseRepo = "database"
	DatabaseTag  = "database"
)

// factDatabaseCopy is the cluster fact that holds the last copy's outcome.
const factDatabaseCopy = "backup.database.copy"

// DatabaseCopy is the outcome of the copies to the bucket: the newest that
// worked, and the reason of the last one when it did not.
type DatabaseCopy struct {
	// Run is the full backup run the newest copy was part of, if any.
	Run      string    `json:"run,omitempty"`
	At       time.Time `json:"at"`
	File     string    `json:"file,omitempty"`
	Snapshot string    `json:"snapshot,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	FailedAt time.Time `json:"failedAt"`
	Evidence []string  `json:"evidence,omitempty"`
	// What the newest copy did: the file's size, what restic found new and
	// sent to the bucket, its seconds, and what retention kept and removed.
	Size     int64   `json:"size,omitempty"`
	Added    int64   `json:"added,omitempty"`
	Uploaded int64   `json:"uploaded,omitempty"`
	Took     float64 `json:"took,omitempty"`
	Kept     int     `json:"kept,omitempty"`
	Removed  int     `json:"removed,omitempty"`
	Dest     string  `json:"dest,omitempty"`
}

// DatabaseCopier copies database backup files to the bucket.
type DatabaseCopier struct {
	Repo  restic.Repo
	Facts *store.Facts
	// Keep is how many copies stay in the bucket: the local keep count.
	Keep int
	Now  func() time.Time
	// Dest names the repository in logs (s3://bucket/prefix/database).
	Dest string
}

// Copy uploads file, removes the copies beyond Keep and records the
// outcome. A failure is recorded and returned.
func (c DatabaseCopier) Copy(ctx context.Context, file string) error { return c.CopyRun(ctx, file, "") }

// CopyRun is Copy as part of the full backup run run: the copy is tagged
// with it (RunTag), so the run can be restored from the bucket alone.
func (c DatabaseCopier) CopyRun(ctx context.Context, file, run string) error {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	last, _, err := LastDatabaseCopy(ctx, c.Facts)
	if err != nil {
		return err
	}
	done, err := c.copy(ctx, file, run)
	if err != nil {
		last.Reason, last.FailedAt, last.Evidence = string(reasons.BackupFailed), now(), []string{err.Error()}
		var re *restic.Error
		if errors.As(err, &re) {
			last.Reason, last.Evidence = string(re.Code), re.Lines
		}
	} else {
		done.Run, done.At, done.File, done.Dest = run, now(), filepath.Base(file), c.Dest
		last = done
	}
	if err != nil {
		last.Run = run
	}
	if serr := RecordDatabaseCopy(ctx, c.Facts, last, now()); serr != nil {
		return errors.Join(err, serr)
	}
	return err
}

// RecordDatabaseCopy stores the outcome of the copies, observed at now.
func RecordDatabaseCopy(ctx context.Context, facts *store.Facts, c DatabaseCopy, now time.Time) error {
	value, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return facts.Set(ctx, factDatabaseCopy, value, now)
}

func (c DatabaseCopier) copy(ctx context.Context, file, run string) (DatabaseCopy, error) {
	f, err := os.Open(file)
	if err != nil {
		return DatabaseCopy{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return DatabaseCopy{}, err
	}
	if err := c.Repo.Ensure(ctx); err != nil {
		return DatabaseCopy{}, err
	}
	tags := []string{DatabaseTag}
	if run != "" {
		tags = append(tags, RunTag(run))
	}
	sum, err := c.Repo.BackupStdin(ctx, filepath.Base(file), f, tags...)
	if err != nil {
		return DatabaseCopy{}, err
	}
	got, err := c.Repo.ForgetCount(ctx, restic.Keep{Last: max(c.Keep, 1)}, DatabaseTag)
	return DatabaseCopy{Snapshot: sum.SnapshotID, Size: fi.Size(), Added: sum.DataAdded, Uploaded: sum.DataAddedPacked,
		Took: sum.TotalDuration, Kept: got.Kept, Removed: got.Removed}, err
}

// Fetch writes the copy snap to the file dest, which must not exist: under
// a temporary name first, so a failed download never looks like a backup.
func (c DatabaseCopier) Fetch(ctx context.Context, snap restic.Snapshot, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists", dest)
	}
	partial := dest + ".partial"
	f, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	err = c.Repo.Dump(ctx, snap.ID, snap.File(), f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(partial)
		return err
	}
	return os.Rename(partial, dest)
}

// List returns the copies in the bucket, newest first.
func (c DatabaseCopier) List(ctx context.Context) ([]restic.Snapshot, error) {
	return c.Repo.Snapshots(ctx, DatabaseTag)
}

// RunTag is the tag of every snapshot made by the full backup run run.
func RunTag(run string) string { return "run-" + run }

// LastDatabaseCopy returns the recorded outcome; ok is false before the
// first copy.
func LastDatabaseCopy(ctx context.Context, facts *store.Facts) (DatabaseCopy, bool, error) {
	f, err := facts.Get(ctx, factDatabaseCopy)
	if errors.Is(err, store.ErrNotFound) {
		return DatabaseCopy{}, false, nil
	} else if err != nil {
		return DatabaseCopy{}, false, err
	}
	var out DatabaseCopy
	return out, true, json.Unmarshal(f.Value, &out)
}

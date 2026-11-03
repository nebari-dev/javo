package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/nebari-dev/javo/internal/model"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// ErrDatabaseInUse is returned by RestoreSQLite when another connection, in
// this or another process, has the database open.
var ErrDatabaseInUse = errors.New("store: the database is open by another process; scale the server to 0 and run the restore again")

// backuper takes a backup of the database into a new file. It is chosen once
// in Open for the engine (like notifier).
type backuper func(ctx context.Context, file string) error

// Backup writes a consistent copy of the live database to file, which must
// not exist. With SQLite it is one `VACUUM INTO` (a read transaction, so
// writers keep going); the copy is written under a temporary name and renamed,
// so a crash never leaves a half-written file under the final name. Postgres
// backups are `pg_dump`, run by the chart's CronJob (site/src/content/docs/admin/backups.mdx).
func (s *Store) Backup(ctx context.Context, file string) error { return s.backup(ctx, file) }

func (s *Store) vacuumInto(ctx context.Context, file string) error {
	if _, err := os.Stat(file); err == nil {
		return fmt.Errorf("store: backup: %s already exists", file)
	}
	partial := file + ".partial"
	_ = os.Remove(partial)
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", partial); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("store: backup: %w", err)
	}
	if err := os.Rename(partial, file); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("store: backup: %w", err)
	}
	return nil
}

func backupPostgres(context.Context, string) error {
	return errors.New("store: backups of a Postgres database are taken with pg_dump (the chart's backup CronJob); see the Backups page of the admin guide")
}

const (
	backupPrefix = "javo-"
	backupSuffix = ".db"
	backupLayout = "20060102T150405Z"
)

// BackupName is the file name of the backup taken at t. Names sort in time
// order.
func BackupName(t time.Time) string {
	return backupPrefix + t.UTC().Format(backupLayout) + backupSuffix
}

// PruneBackups deletes all but the keep newest backups in dir. Files that are
// not named like backups are left alone.
func PruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasPrefix(n, backupPrefix) && strings.HasSuffix(n, backupSuffix) {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	for len(names) > max(keep, 0) {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

// BackupFile is one scheduled backup on disk.
type BackupFile struct {
	Name    string
	TakenAt time.Time
	Size    int64
}

// ListBackups returns the backups in dir (files named like BackupName),
// newest first. A directory that does not exist yet holds none.
func ListBackups(dir string) ([]BackupFile, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []BackupFile
	for _, e := range entries {
		n := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(n, backupPrefix) || !strings.HasSuffix(n, backupSuffix) {
			continue
		}
		at, err := time.Parse(backupLayout, strings.TrimSuffix(strings.TrimPrefix(n, backupPrefix), backupSuffix))
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, BackupFile{Name: n, TakenAt: at, Size: info.Size()})
	}
	slices.SortFunc(out, func(a, b BackupFile) int { return b.TakenAt.Compare(a.TakenAt) })
	return out, nil
}

// ReadBackup calls fn with the SQLite backup at file opened as a store. It
// reads a scratch copy beside the file, brought to this binary's schema, so
// a backup taken by an older release reads like a new one and the backup
// itself is never changed; the copy is removed afterwards.
func ReadBackup(ctx context.Context, file string, fn func(*Store) error) error {
	if err := checkSQLiteFile(ctx, file); err != nil {
		return fmt.Errorf("store: %s is not a usable backup: %w", filepath.Base(file), err)
	}
	scratch, err := os.CreateTemp(filepath.Dir(file), ".read-*.partial")
	if err != nil {
		return err
	}
	name := scratch.Name()
	_ = scratch.Close()
	defer func() {
		for _, f := range []string{name, name + "-wal", name + "-shm"} {
			_ = os.Remove(f)
		}
	}()
	if err := copyFile(file, name); err != nil {
		return err
	}
	s, err := Open(ctx, Config{Engine: EngineSQLite, Path: name})
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		return err
	}
	return fn(s)
}

// BackupPlan says when and where the server backs up its own database.
type BackupPlan struct {
	Schedule string // standard five-field cron, or @every <duration>
	Dir      string
	Keep     int
	Log      *slog.Logger // nil means slog.Default()
	// After, when set, is given each new backup file once it is written and
	// the old ones pruned: the copy to the backup bucket.
	After func(ctx context.Context, file string)
}

// RunBackups takes a backup into plan.Dir on every tick of plan.Schedule and
// keeps the plan.Keep newest, until ctx ends (then it returns nil). A failed
// backup is logged and the next tick tries again. A schedule that does not
// parse is an error returned at once.
func (s *Store) RunBackups(ctx context.Context, plan BackupPlan) error {
	sched, err := cron.ParseStandard(plan.Schedule)
	if err != nil {
		return fmt.Errorf("backup schedule %q: %w", plan.Schedule, err)
	}
	log := plan.Log
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(plan.Dir, 0o750); err != nil {
		return fmt.Errorf("backup directory: %w", err)
	}
	for {
		timer := time.NewTimer(time.Until(sched.Next(now())))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		file := filepath.Join(plan.Dir, BackupName(now()))
		if err := s.Backup(ctx, file); err != nil {
			log.Error("scheduled database backup failed", "error", err)
			continue
		}
		if err := PruneBackups(plan.Dir, plan.Keep); err != nil {
			log.Error("pruning old database backups failed", "error", err)
		}
		log.Info("database backup written", "file", file)
		if plan.After != nil {
			plan.After(ctx, file)
		}
	}
}

// RestoreSQLite replaces the SQLite database at target with the backup file.
// It refuses a file that is not a healthy javo database (no schema), and refuses with
// ErrDatabaseInUse when anything has target open, so stop the server first.
// The database it replaces is kept beside it as <target>.before-restore.
func RestoreSQLite(ctx context.Context, target, backup string) error {
	if err := checkSQLiteFile(ctx, backup); err != nil {
		return fmt.Errorf("store: restore: %s is not a usable backup: %w", backup, err)
	}
	if err := ensureUnused(ctx, target); err != nil {
		return err
	}
	staged := target + ".restoring"
	if err := copyFile(backup, staged); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("store: restore: %w", err)
	}
	if err := holdRestored(ctx, staged, filepath.Base(backup)); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("store: restore: %w", err)
	}
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, target+".before-restore"); err != nil {
			_ = os.Remove(staged)
			return fmt.Errorf("store: restore: %w", err)
		}
	}
	// A leftover write-ahead log belongs to the replaced file.
	_ = os.Remove(target + "-wal")
	_ = os.Remove(target + "-shm")
	if err := os.Rename(staged, target); err != nil {
		return fmt.Errorf("store: restore: %w", err)
	}
	return nil
}

// holdRestored brings the restored copy at file to this binary's schema and
// holds every live app in it under the restore name, so that nothing starts
// until an admin releases the restored apps (Apps.Release).
func holdRestored(ctx context.Context, file, name string) error {
	s, err := Open(ctx, Config{Engine: EngineSQLite, Path: file})
	if err != nil {
		return err
	}
	if err := s.Migrate(ctx); err != nil {
		_ = s.Close()
		return err
	}
	if _, err := s.Apps().HoldAll(ctx, model.Actor{System: model.SystemCLI}, name); err != nil {
		_ = s.Close()
		return err
	}
	if err := s.Close(); err != nil {
		return err
	}
	// Closing the last connection folds the write-ahead log into the file.
	_ = os.Remove(file + "-wal")
	_ = os.Remove(file + "-shm")
	return nil
}

func openPlain(file, mode string) (*sql.DB, error) {
	q := url.Values{}
	q.Set("mode", mode)
	return sql.Open("sqlite", (&url.URL{Scheme: "file", Opaque: file, RawQuery: q.Encode()}).String())
}

func checkSQLiteFile(ctx context.Context, file string) error {
	if _, err := os.Stat(file); err != nil {
		return err
	}
	db, err := openPlain(file, "ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errors.New(result)
	}
	// A file with no applied migration is not a javo database (an empty file
	// from a mistyped path passes integrity_check).
	var applied int
	err = db.QueryRowContext(ctx, "SELECT count(*) FROM goose_db_version WHERE is_applied AND version_id > 0").Scan(&applied)
	if err != nil || applied == 0 {
		return errors.New("it has no javo schema (no applied migrations)")
	}
	return nil
}

// ensureUnused returns ErrDatabaseInUse when another connection has the
// database open. A write-ahead log database can only leave WAL mode when it
// is the only connection; SQLite answers with the mode it is still in when it
// could not.
func ensureUnused(ctx context.Context, target string) error {
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	db, err := openPlain(target, "rw")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode); err != nil {
		if strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), "busy") {
			return ErrDatabaseInUse
		}
		return fmt.Errorf("store: restore: %w", err)
	}
	if !strings.EqualFold(mode, "delete") {
		return ErrDatabaseInUse
	}
	return nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

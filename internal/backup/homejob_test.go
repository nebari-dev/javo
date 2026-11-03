package backup_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/backup"
	"github.com/nebari-dev/javo/internal/backup/restic"
	"github.com/nebari-dev/javo/internal/backup/restic/restictest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

func put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func body(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// What a home's backup Job and restore Job do, run here against a folder.
func TestHomeJobBacksUpAndRestoresAHome(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	repo := restictest.New(t, "pw")
	j := backup.HomeJob{Repo: repo, Dir: home, Keep: restic.Keep{Daily: 7}}

	put(t, filepath.Join(home, "report.txt"), "v1")
	first := j.Backup(ctx)
	if first.Reason != "" || first.Snapshot == "" || first.Files != 1 {
		t.Fatalf("first backup: %+v", first)
	}
	put(t, filepath.Join(home, "report.txt"), "v2")
	put(t, filepath.Join(home, "later.txt"), "new")

	beside := j.Restore(ctx, first.Snapshot, model.RestoreBeside, "restored-2026-11-01")
	if beside.Reason != "" {
		t.Fatalf("restore beside: %+v", beside)
	}
	if got := body(t, filepath.Join(home, "restored-2026-11-01", "report.txt")); got != "v1" {
		t.Errorf("restored copy: %q", got)
	}
	if got := body(t, filepath.Join(home, "report.txt")); got != "v2" {
		t.Errorf("current file touched: %q", got)
	}

	replace := j.Restore(ctx, first.Snapshot, model.RestoreReplace, "")
	if replace.Reason != "" {
		t.Fatalf("restore replace: %+v", replace)
	}
	if got := body(t, filepath.Join(home, "report.txt")); got != "v1" {
		t.Errorf("replaced file: %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, "later.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file made after the backup survived a replace: %v", err)
	}

	wrong := j
	wrong.Repo.Env = []string{"RESTIC_PASSWORD=nope"}
	if r := wrong.Backup(ctx); r.Reason != string(reasons.BackupWrongPassword) || len(r.Evidence) == 0 {
		t.Fatalf("backup with the wrong password: %+v", r)
	}
	if r := j.Restore(ctx, "ffffffff", model.RestoreReplace, ""); r.Reason == "" {
		t.Fatalf("restore of a snapshot that is not there: %+v", r)
	}
}

// Each run's snapshot carries its run's tag, and retention still counts the
// home's snapshots together (one a day here), whatever run made them.
func TestHomeBackupsOfRunsAreTaggedAndKeptTogether(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	repo := restictest.New(t, "pw")
	for i, run := range []string{"r1", "r2", "r3"} {
		put(t, filepath.Join(home, "f.txt"), strings.Repeat("x", i+1))
		if r := (backup.HomeJob{Repo: repo, Dir: home, Keep: restic.Keep{Daily: 1}, Run: run}).Backup(ctx); r.Reason != "" {
			t.Fatalf("run %s: %+v", run, r)
		}
	}
	snaps, err := repo.Snapshots(ctx, backup.HomeTag)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || !slices.Contains(snaps[0].Tags, backup.RunTag("r3")) {
		t.Fatalf("kept %+v, want only the newest, tagged run-r3", snaps)
	}
}

// The result fits a termination message (4096 bytes) and reads back.
func TestJobResultRoundTripsWithinATerminationMessage(t *testing.T) {
	t.Parallel()
	r := backup.JobResult{Snapshot: "abc", Reason: string(reasons.BackupFailed)}
	for range 100 {
		r.Evidence = append(r.Evidence, strings.Repeat("x", 200))
	}
	msg := r.Message()
	if len(msg) > 4096 {
		t.Fatalf("message is %d bytes", len(msg))
	}
	got, ok := backup.ParseJobResult(msg)
	if !ok || got.Snapshot != "abc" || got.Reason != r.Reason || len(got.Evidence) == 0 {
		t.Fatalf("parsed %+v, %v", got, ok)
	}
	if _, ok := backup.ParseJobResult("panic: something"); ok {
		t.Fatal("plain text parsed as a result")
	}
}

// A home's backup tells its log what it does, step by step, as timestamped
// key=value lines: where it reads, restic's progress, the snapshot with what
// was new and what was uploaded where, and what retention kept.
func TestHomeBackupLogsEachEventAsKeyValues(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	put(t, filepath.Join(home, "a", "b.txt"), "hello")
	// No repository yet: the first backup makes it.
	repo := restic.Repo{Bin: restictest.Bin(t), URL: filepath.Join(t.TempDir(), "repo"), Env: []string{"RESTIC_PASSWORD=pw"}}
	var out bytes.Buffer
	at := time.Date(2026, 11, 2, 14, 3, 12, 0, time.UTC)
	log := &backup.EventLog{W: &out, Now: func() time.Time { return at }, Fields: []string{"step", "home", "person", "Alice Example (alice)"}}
	r := backup.HomeJob{Repo: repo, Dir: home, Keep: restic.Keep{Daily: 7}, Run: "r1", Dest: "s3://b/javo/homes/ns/alice", Log: log}.Backup(ctx)
	if r.Reason != "" || r.Dirs == 0 || r.Took <= 0 {
		t.Fatalf("result = %+v", r)
	}
	text := out.String()
	for _, want := range []string{
		`14:03:12 step=home person="Alice Example (alice)" action=start dir=` + home + ` dest=s3://b/javo/homes/ns/alice`,
		"action=snapshot id=" + r.Snapshot[:8], "files=1 dirs=", "uploaded=", "dest=s3://b/javo/homes/ns/alice",
		"action=forget kept=1 removed=0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("log lacks %q:\n%s", want, text)
		}
	}
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		if !strings.HasPrefix(l, "14:03:12 step=home ") {
			t.Errorf("not an event line: %q", l)
		}
	}
}

func TestEventLogQuotesValuesWithSpaces(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	(&backup.EventLog{W: &out, Now: func() time.Time { return time.Date(2026, 1, 1, 9, 5, 0, 0, time.UTC) }}).Event("upload", "dest", "s3://b/x", "note", "two words", "n", 3)
	if got := out.String(); got != "09:05:00 action=upload dest=s3://b/x note=\"two words\" n=3\n" {
		t.Errorf("got %q", got)
	}
}

// Who owns a volume's files: its root's owner, or when the root is root's
// (a volume made by the storage driver), whoever owns most of what is in it.
func TestPickOwner(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		root    backup.Owner
		entries []backup.Owner
		want    backup.Owner
		ok      bool
	}{
		{backup.Owner{UID: 1001, GID: 100}, nil, backup.Owner{UID: 1001, GID: 100}, true},
		{backup.Owner{}, []backup.Owner{{UID: 4321, GID: 100}, {UID: 4321, GID: 100}, {UID: 7, GID: 7}, {}}, backup.Owner{UID: 4321, GID: 100}, true},
		{backup.Owner{}, []backup.Owner{{}, {}}, backup.Owner{}, false},
		{backup.Owner{}, nil, backup.Owner{}, false},
	} {
		got, ok := backup.PickOwner(c.root, c.entries)
		if got != c.want || ok != c.ok {
			t.Errorf("PickOwner(%+v, %+v) = %+v, %v", c.root, c.entries, got, ok)
		}
	}
}

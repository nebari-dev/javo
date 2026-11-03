package restic_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/backup/restic"
	"github.com/nebari-dev/javo/internal/reasons"
)

// bin is the restic the tests run: $JAVO_TEST_RESTIC, else the one
// `make bin/restic` downloads (scripts/build/fetch-restic.sh). A missing restic fails the
// tests, like a missing Postgres does.
func bin(t *testing.T) string {
	t.Helper()
	if b := os.Getenv("JAVO_TEST_RESTIC"); b != "" {
		return b
	}
	b, err := filepath.Abs("../../../bin/restic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b); err != nil {
		t.Fatalf("no restic at %s: run make bin/restic or set JAVO_TEST_RESTIC", b)
	}
	return b
}

// localRepo is a restic repository in a folder of the test's own.
func localRepo(t *testing.T, password string) restic.Repo {
	t.Helper()
	return restic.Repo{Bin: bin(t), URL: filepath.Join(t.TempDir(), "repo"), Env: []string{"RESTIC_PASSWORD=" + password}}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRepositoryOfATarget(t *testing.T) {
	for _, tc := range []struct {
		name string
		t    restic.Target
		want string
	}{
		{"prefix", restic.Target{Endpoint: "https://s3.example.com", Bucket: "backups", Prefix: "javo"}, "s3:https://s3.example.com/backups/javo/homes/alice"},
		{"no prefix", restic.Target{Endpoint: "http://minio:9000/", Bucket: "b"}, "s3:http://minio:9000/b/homes/alice"},
		{"nested prefix with slashes", restic.Target{Endpoint: "https://s3.example.com", Bucket: "b", Prefix: "/team/javo/"}, "s3:https://s3.example.com/b/team/javo/homes/alice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.t.Repo("restic", "homes/alice").URL; got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestTargetPassesCredentialsAndOptions(t *testing.T) {
	r := restic.Target{
		Endpoint: "https://s3.example.com", Bucket: "b", Region: "eu-west-1", BucketLookup: "path",
		InsecureTLS: true, CACertFile: "/etc/ca.crt", AccessKeyID: "id", SecretAccessKey: "secret", Password: "pw",
	}.Repo("restic", "database")
	for _, want := range []string{"AWS_ACCESS_KEY_ID=id", "AWS_SECRET_ACCESS_KEY=secret", "RESTIC_PASSWORD=pw", "AWS_DEFAULT_REGION=eu-west-1"} {
		if !slices.Contains(r.Env, want) {
			t.Errorf("env %v lacks %s", r.Env, want)
		}
	}
	args := strings.Join(r.Args, " ")
	for _, want := range []string{"-o s3.bucket-lookup=path", "-o s3.region=eu-west-1", "--insecure-tls", "--cacert /etc/ca.crt"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
}

func TestBackUpAFolderAndRestoreIt(t *testing.T) {
	ctx := context.Background()
	r := localRepo(t, "pw")
	if err := r.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	// A second Ensure finds the repository and leaves it.
	if err := r.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	write(t, filepath.Join(home, "notes.txt"), "first")
	write(t, filepath.Join(home, "data", "a.csv"), "1,2")
	first, err := r.BackupDir(ctx, home, "home")
	if err != nil {
		t.Fatal(err)
	}
	if first.SnapshotID == "" || first.FilesNew != 2 {
		t.Fatalf("first backup: %+v", first)
	}
	// Only what changed is uploaded again.
	again, err := r.BackupDir(ctx, home, "home")
	if err != nil {
		t.Fatal(err)
	}
	if again.FilesNew != 0 || again.FilesChanged != 0 || again.DataAdded >= first.DataAdded {
		t.Fatalf("unchanged backup uploaded files again: %+v after %+v", again, first)
	}
	snaps, err := r.Snapshots(ctx, "home")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 || snaps[0].ID != again.SnapshotID || snaps[0].Time.Before(snaps[1].Time) {
		t.Fatalf("snapshots, newest first: %+v", snaps)
	}

	write(t, filepath.Join(home, "notes.txt"), "changed")
	write(t, filepath.Join(home, "new.txt"), "made after the backup")
	if err := os.Remove(filepath.Join(home, "data", "a.csv")); err != nil {
		t.Fatal(err)
	}

	t.Run("beside", func(t *testing.T) {
		into := filepath.Join(home, "restored-2026-11-01")
		if err := r.Restore(ctx, first.SnapshotID, home, into, false); err != nil {
			t.Fatal(err)
		}
		if got := read(t, filepath.Join(into, "notes.txt")); got != "first" {
			t.Errorf("restored notes: %q", got)
		}
		if got := read(t, filepath.Join(home, "notes.txt")); got != "changed" {
			t.Errorf("current notes touched: %q", got)
		}
	})
	t.Run("replace", func(t *testing.T) {
		if err := r.Restore(ctx, first.SnapshotID, home, home, true); err != nil {
			t.Fatal(err)
		}
		if got := read(t, filepath.Join(home, "notes.txt")); got != "first" {
			t.Errorf("notes: %q", got)
		}
		if got := read(t, filepath.Join(home, "data", "a.csv")); got != "1,2" {
			t.Errorf("deleted file not back: %q", got)
		}
		if _, err := os.Stat(filepath.Join(home, "new.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a file not in the backup stayed: %v", err)
		}
	})
}

func TestBackUpAStreamKeepTheNewestAndDumpIt(t *testing.T) {
	ctx := context.Background()
	r := localRepo(t, "pw")
	if err := r.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, body := range []string{"one", "two", "three"} {
		s, err := r.BackupStdin(ctx, "javo-"+body+".db", strings.NewReader(body), "database")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.SnapshotID)
	}
	if err := r.Forget(ctx, restic.Keep{Last: 2}, "database"); err != nil {
		t.Fatal(err)
	}
	snaps, err := r.Snapshots(ctx, "database")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 || snaps[0].ID != ids[2] || snaps[1].ID != ids[1] {
		t.Fatalf("kept %+v, want the newest two of %v", snaps, ids)
	}
	if snaps[0].File() != "javo-three.db" {
		t.Errorf("file name of the newest: %q", snaps[0].File())
	}
	var out bytes.Buffer
	if err := r.Dump(ctx, snaps[0].ID, snaps[0].File(), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "three" {
		t.Errorf("dumped %q", out.String())
	}
}

func TestFailuresNameTheirReason(t *testing.T) {
	ctx := context.Background()
	r := localRepo(t, "right")
	if err := r.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	wrong := r
	wrong.Env = []string{"RESTIC_PASSWORD=wrong"}
	_, err := wrong.Snapshots(ctx)
	var re *restic.Error
	if !errors.As(err, &re) || re.Code != reasons.BackupWrongPassword {
		t.Fatalf("wrong password: %v", err)
	}
	if len(re.Lines) == 0 {
		t.Errorf("no evidence kept: %+v", re)
	}

	missing := r
	missing.URL = filepath.Join(t.TempDir(), "none")
	if _, err := missing.Snapshots(ctx); !errors.As(err, &re) || re.Code != reasons.BackupRepoMissing {
		t.Fatalf("missing repository: %v", err)
	}

	// restic keeps retrying an address that does not answer; the caller's
	// deadline ends it, and that is the bucket being unreachable.
	gone := restic.Target{Endpoint: "http://127.0.0.1:1", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s", Password: "p"}.Repo(bin(t), "x")
	short, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := gone.Snapshots(short); !errors.As(err, &re) || re.Code != reasons.BackupUnreachable {
		t.Fatalf("unreachable bucket: %v", err)
	}
}

func TestAFileThatCannotBeReadStillMakesASnapshot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	ctx := context.Background()
	r := localRepo(t, "pw")
	if err := r.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	write(t, filepath.Join(home, "ok.txt"), "fine")
	write(t, filepath.Join(home, "locked.txt"), "secret")
	if err := os.Chmod(filepath.Join(home, "locked.txt"), 0); err != nil {
		t.Fatal(err)
	}
	sum, err := r.BackupDir(ctx, home)
	var re *restic.Error
	if !errors.As(err, &re) || re.Code != reasons.BackupIncomplete || sum.SnapshotID == "" {
		t.Fatalf("got %+v, %v", sum, err)
	}
	if !slices.ContainsFunc(re.Lines, func(l string) bool { return strings.Contains(l, "locked.txt") }) {
		t.Errorf("the unreadable file is not named: %q", re.Lines)
	}
}

// A backup reports restic's own progress while it runs (from its --json
// status lines) and a full summary; forgetting says what it kept and removed.
func TestBackupReportsProgressAndForgetSaysWhatItRemoved(t *testing.T) {
	ctx := context.Background()
	r := localRepo(t, "pw")
	r.Env = append(r.Env, "RESTIC_PROGRESS_FPS=50")
	if err := r.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	big := make([]byte, 64<<20)
	for i := range big {
		big[i] = byte(i * 7919 % 251)
	}
	if err := os.WriteFile(filepath.Join(home, "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(home, "sub", "a.txt"), "a")
	var seen []restic.Progress
	sum, err := r.BackupDirProgress(ctx, home, func(p restic.Progress) { seen = append(seen, p) }, "home")
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 || seen[len(seen)-1].TotalBytes == 0 {
		t.Fatalf("progress = %+v", seen)
	}
	if sum.DirsNew == 0 || sum.TotalDuration <= 0 || sum.DataAddedPacked == 0 || sum.TotalFiles != 2 {
		t.Fatalf("summary = %+v", sum)
	}
	for range 2 {
		write(t, filepath.Join(home, "sub", "a.txt"), time.Now().String())
		if _, err := r.BackupDir(ctx, home, "home"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.ForgetCount(ctx, restic.Keep{Last: 1}, "home")
	if err != nil || got.Kept != 1 || got.Removed != 2 {
		t.Fatalf("forget = %+v, %v", got, err)
	}
}

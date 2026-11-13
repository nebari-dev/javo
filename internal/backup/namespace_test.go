package backup_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nebari-dev/javo/internal/backup"
	"github.com/nebari-dev/javo/internal/backup/restic"
	"github.com/nebari-dev/javo/internal/backup/restic/restictest"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Each namespace gets a password of its own, made from the backup password
// in the release namespace; none of them is that password, so a namespace's
// Secret opens neither the database's repository nor another namespace's.
func TestEachNamespaceHasItsOwnPassword(t *testing.T) {
	t.Parallel()
	a, b := backup.NamespacePassword("master", "team-a"), backup.NamespacePassword("master", "team-b")
	if a == b || a == "master" || b == "master" || len(a) < 32 {
		t.Fatalf("passwords %q and %q", a, b)
	}
	if again := backup.NamespacePassword("master", "team-a"); again != a {
		t.Fatalf("the password of team-a changed: %q, then %q", a, again)
	}
	if other := backup.NamespacePassword("another-master", "team-a"); other == a {
		t.Fatal("another backup password gives team-a the same password")
	}
}

func wrongPassword(err error) bool {
	var re *restic.Error
	return errors.As(err, &re) && re.Code == reasons.BackupWrongPassword
}

// A repository made under the backup password (before namespaces had their
// own) is moved to its namespace's password: after that only the
// namespace's password opens it, and its backups are still there. Doing it
// again, or on a repository that does not exist yet, changes nothing.
func TestRekeyMovesARepositoryToItsNamespacesPassword(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	master := restictest.New(t, "master")
	src := filepath.Join(t.TempDir(), "home")
	put(t, filepath.Join(src, "notes.txt"), "kept")
	if _, err := master.BackupDir(ctx, src, backup.HomeTag); err != nil {
		t.Fatal(err)
	}
	pw := backup.NamespacePassword("master", "team-a")

	// Before: the listing still reads it with the backup password.
	if snaps, err := backup.ListSnapshots(ctx, master, pw, backup.HomeTag); err != nil || len(snaps) != 1 {
		t.Fatalf("snapshots before the move: %v, %v", snaps, err)
	}
	for range 2 {
		if err := backup.Rekey(ctx, master, pw); err != nil {
			t.Fatal(err)
		}
		// The key restic made for the namespace's password costs what a
		// real one does on every command; what follows needs only its
		// password.
		restictest.Cheapen(t, master.URL, pw)
	}
	if err := master.Opens(ctx); !wrongPassword(err) {
		t.Fatalf("the backup password still opens the repository: %v", err)
	}
	if err := master.WithPassword(backup.NamespacePassword("master", "team-b")).Opens(ctx); !wrongPassword(err) {
		t.Fatalf("team-b's password opens team-a's repository: %v", err)
	}
	if snaps, err := backup.ListSnapshots(ctx, master, pw, backup.HomeTag); err != nil || len(snaps) != 1 {
		t.Fatalf("snapshots after the move: %v, %v", snaps, err)
	}

	missing := restic.Repo{Bin: restictest.Bin(t), URL: filepath.Join(t.TempDir(), "none"), Env: []string{"RESTIC_PASSWORD=master"}}
	if err := backup.Rekey(ctx, missing, pw); err != nil {
		t.Fatalf("rekey of a repository not made yet: %v", err)
	}
}

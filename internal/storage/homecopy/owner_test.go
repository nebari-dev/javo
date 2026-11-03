package homecopy

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// needRoot skips a test that gives files to other users: only root may.
// scripts/test/test-root.sh runs these in a container as root.
func needRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root to own files as other users (scripts/test/test-root.sh)")
	}
}

// sums is the sha256 of every regular file under root, by path.
func sums(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	must(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		out[rel] = sha256.Sum256(b)
		return err
	}))
	return out
}

// A home of another system (a JupyterHub home, uid 1000) copies with every
// entry given to the person who now owns it, private files included, with
// the same content and modes; the check of the copy finds nothing.
func TestRunAsGivesEveryEntryToTheNewOwner(t *testing.T) {
	needRoot(t)
	old, new := dirs(t)
	write(t, filepath.Join(old, "notebooks", "a.ipynb"), "{}\n", 0o644)
	write(t, filepath.Join(old, ".private", "token"), "private\n", 0o600)
	must(t, os.Chmod(filepath.Join(old, ".private"), 0o700))
	must(t, os.Link(filepath.Join(old, "notebooks", "a.ipynb"), filepath.Join(old, "hard")))
	must(t, os.Symlink("notebooks/a.ipynb", filepath.Join(old, "sym")))
	must(t, unix.Mkfifo(filepath.Join(old, "pipe"), 0o640))
	must(t, filepath.WalkDir(old, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, 1000, 1000)
	}))

	term := filepath.Join(t.TempDir(), "term")
	var log strings.Builder
	if err := RunAs(old, new, term, Owner{UID: 123456, GID: 100}, &log); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if b, _ := os.ReadFile(term); len(b) != 0 {
		t.Fatalf("termination message %q", b)
	}
	must(t, filepath.WalkDir(new, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if st := stat(t, p); st.Uid != 123456 || st.Gid != 100 {
			t.Errorf("%s: owner %d:%d, want 123456:100", p, st.Uid, st.Gid)
		}
		return nil
	}))
	if fi, err := os.Lstat(filepath.Join(new, ".private", "token")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("private file mode %v, %v", fi.Mode(), err)
	}
	a, b := sums(t, old), sums(t, new)
	if len(a) != len(b) || a["notebooks/a.ipynb"] != b["notebooks/a.ipynb"] || a[".private/token"] != b[".private/token"] {
		t.Errorf("contents differ: %v vs %v", a, b)
	}
}

// The check of an owned copy names an entry that does not belong to the new
// owner.
func TestVerifyAsNamesAnEntryOfAnotherOwner(t *testing.T) {
	needRoot(t)
	old, new := dirs(t)
	write(t, filepath.Join(old, "a.txt"), "a\n", 0o644)
	write(t, filepath.Join(old, "b.txt"), "b\n", 0o644)
	owner := Owner{UID: 123456, GID: 100}
	must(t, RunAs(old, new, filepath.Join(t.TempDir(), "term"), owner, &strings.Builder{}))
	must(t, os.Lchown(filepath.Join(new, "b.txt"), 1000, 100))
	diffs, err := verifyTree(old, new, &owner)
	must(t, err)
	if len(diffs) != 1 || diffs[0] != "owner b.txt" {
		t.Fatalf("diffs = %v, want [owner b.txt]", diffs)
	}
}

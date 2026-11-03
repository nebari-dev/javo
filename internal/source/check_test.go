package source

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

// clone makes what git-sync leaves: root/current -> .worktrees/<sha>.
func clone(t *testing.T, files ...string) (link string) {
	t.Helper()
	root := t.TempDir()
	wt := filepath.Join(root, ".worktrees", sha)
	for _, f := range files {
		p := filepath.Join(wt, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(root, "current")
	if err := os.Symlink(filepath.Join(".worktrees", sha), link); err != nil {
		t.Fatal(err)
	}
	return link
}

func TestCheckReturnsTheCommit(t *testing.T) {
	link := clone(t, "web/app.py")
	got, err := Check(link, "web", "app.py")
	if err != nil || got != sha {
		t.Fatalf("Check = %q, %v", got, err)
	}
	if got, err := Check(link, "", ""); err != nil || got != sha {
		t.Fatalf("Check without path or file = %q, %v", got, err)
	}
}

func TestCheckMissingFileSaysFileMissing(t *testing.T) {
	link := clone(t, "web/other.py")
	_, err := Check(link, "web", "app.py")
	if !errors.Is(err, ErrFileMissing) || !strings.HasPrefix(err.Error(), "file-missing: app.py") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Check(link, "nope", ""); !errors.Is(err, ErrFileMissing) {
		t.Fatalf("a missing folder is a missing file too: %v", err)
	}
}

func TestCheckRefusesWhatLeavesTheClone(t *testing.T) {
	link := clone(t, "web/app.py")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(filepath.Dir(link), ".worktrees", sha, "web", "out")); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string][2]string{
		"dotdot in file":  {"web", "../../etc/passwd"},
		"dotdot in path":  {"../..", ""},
		"symlinked dir":   {"web", "out/secret"},
		"absolute file":   {"web", "/etc/passwd"},
		"control in file": {"web", "app.py\n"},
	} {
		if _, err := Check(link, c[0], c[1]); !errors.Is(err, ErrFileMissing) {
			t.Errorf("%s: err = %v, want ErrFileMissing", name, err)
		}
	}
}

func TestCheckRefusesALinkThatIsNotACommit(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "wt", "main"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "current")
	if err := os.Symlink(filepath.Join("wt", "main"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(link, "", ""); err == nil || errors.Is(err, ErrFileMissing) {
		t.Fatalf("err = %v", err)
	}
}

func TestCommitOf(t *testing.T) {
	if c, ok := CommitOf("commit=" + sha + "\n"); !ok || c != sha {
		t.Fatalf("got %q %v", c, ok)
	}
	for _, bad := range []string{"", "commit=abc", "file-missing: x", "commit=" + strings.ToUpper(sha)} {
		if _, ok := CommitOf(bad); ok {
			t.Errorf("CommitOf(%q) ok", bad)
		}
	}
}

package homecopy

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p, body string, mode os.FileMode) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(p), 0o755))
	must(t, os.WriteFile(p, []byte(body), mode))
	must(t, os.Chmod(p, mode))
}

func stat(t *testing.T, p string) *syscall.Stat_t {
	t.Helper()
	fi, err := os.Lstat(p)
	must(t, err)
	return fi.Sys().(*syscall.Stat_t)
}

func dirs(t *testing.T) (old, new string) {
	root := t.TempDir()
	old, new = filepath.Join(root, "old"), filepath.Join(root, "new")
	must(t, os.MkdirAll(old, 0o755))
	must(t, os.MkdirAll(new, 0o755))
	return old, new
}

// A tree with what a home holds copies with its hard links, symbolic links,
// modes (setgid folders, 0660 files), times, empty folders and FIFOs, and
// the check of the copy finds nothing.
func TestATreeCopiesWithLinksModesAndTimes(t *testing.T) {
	old, new := dirs(t)
	write(t, filepath.Join(old, "src", "a.txt"), "one\n", 0o660)
	must(t, os.Link(filepath.Join(old, "src", "a.txt"), filepath.Join(old, "src", "a-link.txt")))
	must(t, os.Symlink("src/a.txt", filepath.Join(old, "sym")))
	write(t, filepath.Join(old, "src", "private.bin"), "secret\n", 0o600)
	must(t, os.MkdirAll(filepath.Join(old, "src", "empty"), 0o755))
	must(t, os.MkdirAll(filepath.Join(old, "group"), 0o755))
	must(t, os.Chmod(filepath.Join(old, "group"), 0o775|os.ModeSetgid))
	must(t, unix.Mkfifo(filepath.Join(old, "pipe"), 0o640))
	when := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	must(t, os.Chtimes(filepath.Join(old, "src", "private.bin"), when, when))
	must(t, os.Chtimes(filepath.Join(old, "src"), when, when))

	if err := copyTree(old, new, io.Discard); err != nil {
		t.Fatal(err)
	}
	if a, b := stat(t, filepath.Join(new, "src", "a.txt")), stat(t, filepath.Join(new, "src", "a-link.txt")); a.Ino != b.Ino {
		t.Error("the hard link became two files")
	}
	if l, err := os.Readlink(filepath.Join(new, "sym")); err != nil || l != "src/a.txt" {
		t.Errorf("symlink = %q, %v", l, err)
	}
	for p, want := range map[string]os.FileMode{
		"src/a.txt": 0o660, "src/private.bin": 0o600, "group": 0o775 | os.ModeSetgid | os.ModeDir, "src/empty": 0o755 | os.ModeDir,
	} {
		fi, err := os.Lstat(filepath.Join(new, p))
		if err != nil || fi.Mode()&(os.ModePerm|os.ModeSetgid|os.ModeDir) != want {
			t.Errorf("%s: mode %v, %v; want %v", p, fi.Mode(), err, want)
		}
	}
	if fi, err := os.Lstat(filepath.Join(new, "pipe")); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("pipe: %v, %v", fi, err)
	}
	for _, p := range []string{"src/private.bin", "src"} {
		if fi, err := os.Lstat(filepath.Join(new, p)); err != nil || !fi.ModTime().Equal(when) {
			t.Errorf("%s: mtime %v, %v; want %v", p, fi.ModTime(), err, when)
		}
	}
	if diffs, err := verifyTree(old, new); err != nil || len(diffs) != 0 {
		t.Fatalf("verify: %v, %v", diffs, err)
	}
}

// A file whose group is another of the process's groups (a team folder's
// gid, which the copy Job gets as a supplemental group) keeps it.
func TestAFileKeepsAGroupOfTheProcess(t *testing.T) {
	groups, err := os.Getgroups()
	must(t, err)
	other := -1
	for _, g := range groups {
		if g != os.Getgid() {
			other = g
		}
	}
	if other < 0 {
		t.Skip("the test process is in no group besides its own")
	}
	old, new := dirs(t)
	write(t, filepath.Join(old, "team.txt"), "team\n", 0o640)
	must(t, os.Lchown(filepath.Join(old, "team.txt"), -1, other))
	must(t, copyTree(old, new, io.Discard))
	if g := stat(t, filepath.Join(new, "team.txt")).Gid; int(g) != other {
		t.Fatalf("gid %d, want %d", g, other)
	}
	if diffs, err := verifyTree(old, new); err != nil || len(diffs) != 0 {
		t.Fatalf("verify: %v, %v", diffs, err)
	}
}

// lost+found at the root of the old volume (ext4 makes it, owned by root) is
// neither copied nor an error.
func TestLostAndFoundIsLeftAlone(t *testing.T) {
	old, new := dirs(t)
	must(t, os.MkdirAll(filepath.Join(old, "lost+found"), 0o700))
	must(t, os.Chmod(filepath.Join(old, "lost+found"), 0))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(old, "lost+found"), 0o700) })
	write(t, filepath.Join(old, "data", "f"), "x\n", 0o644)
	must(t, copyTree(old, new, io.Discard))
	if _, err := os.Lstat(filepath.Join(new, "lost+found")); !os.IsNotExist(err) {
		t.Errorf("lost+found was copied: %v", err)
	}
	if diffs, err := verifyTree(old, new); err != nil || len(diffs) != 0 {
		t.Fatalf("verify: %v, %v", diffs, err)
	}
}

// A file the copy cannot read fails the copy and is named; the rest is
// still copied.
func TestAnUnreadableFileFailsTheCopyAndIsNamed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every file")
	}
	old, new := dirs(t)
	write(t, filepath.Join(old, "readable"), "ok\n", 0o644)
	write(t, filepath.Join(old, "unreadable"), "no\n", 0)
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(old, "unreadable"), 0o600) })
	err := copyTree(old, new, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("err = %v, want one naming the unreadable file", err)
	}
	if b, err := os.ReadFile(filepath.Join(new, "readable")); err != nil || string(b) != "ok\n" {
		t.Errorf("readable = %q, %v", b, err)
	}
}

// The check finds content, mode, time, group, link and missing differences,
// whatever the copy did.
func TestVerifyFindsDifferences(t *testing.T) {
	old, new := dirs(t)
	write(t, filepath.Join(old, "content"), "real\n", 0o644)
	write(t, filepath.Join(old, "mode"), "m\n", 0o640)
	write(t, filepath.Join(old, "time"), "t\n", 0o644)
	write(t, filepath.Join(old, "gone"), "g\n", 0o644)
	write(t, filepath.Join(old, "h1"), "h\n", 0o644)
	must(t, os.Link(filepath.Join(old, "h1"), filepath.Join(old, "h2")))
	must(t, os.Symlink("content", filepath.Join(old, "sym")))
	must(t, copyTree(old, new, io.Discard))

	// Same size and time, other bytes: only a content check sees it.
	fi, _ := os.Stat(filepath.Join(old, "content"))
	write(t, filepath.Join(new, "content"), "fake\n", 0o644)
	must(t, os.Chtimes(filepath.Join(new, "content"), fi.ModTime(), fi.ModTime()))
	must(t, os.Chmod(filepath.Join(new, "mode"), 0o644))
	must(t, os.Chtimes(filepath.Join(new, "time"), time.Unix(1, 0), time.Unix(1, 0)))
	must(t, os.Remove(filepath.Join(new, "gone")))
	must(t, os.Remove(filepath.Join(new, "h2")))
	write(t, filepath.Join(new, "h2"), "h\n", 0o644)
	ht, _ := os.Stat(filepath.Join(old, "h1"))
	must(t, os.Chtimes(filepath.Join(new, "h2"), ht.ModTime(), ht.ModTime()))
	must(t, os.Remove(filepath.Join(new, "sym")))
	must(t, os.Symlink("elsewhere", filepath.Join(new, "sym")))

	diffs, err := verifyTree(old, new)
	must(t, err)
	got := strings.Join(diffs, "\n")
	for _, want := range []string{"content", "mode", "time", "gone", "h2", "sym"} {
		if !strings.Contains(got, want) {
			t.Errorf("no difference named %q in:\n%s", want, got)
		}
	}
}

// Run writes the outcome as the termination message: empty when the copy
// verified, else a headline and at most 20 lines, and fails.
func TestRunReportsInTheTerminationMessage(t *testing.T) {
	old, new := dirs(t)
	term := filepath.Join(t.TempDir(), "term")
	for i := 0; i < 40; i++ {
		write(t, filepath.Join(old, "f", string(rune('a'+i%26))+strings.Repeat("x", i)), "x\n", 0o644)
	}
	var log strings.Builder
	if err := Run(old, new, term, &log); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(term); len(b) != 0 || !strings.Contains(log.String(), "copy verified") {
		t.Fatalf("termination %q, log %q", b, log.String())
	}

	// The new side changes after the copy, keeping sizes and times: the
	// check finds every file, and the message is capped.
	for i := 0; i < 40; i++ {
		p := filepath.Join(old, "f", string(rune('a'+i%26))+strings.Repeat("x", i))
		fi, _ := os.Stat(p)
		write(t, filepath.Join(new, "f", filepath.Base(p)), "y\n", 0o644)
		must(t, os.Chtimes(filepath.Join(new, "f", filepath.Base(p)), fi.ModTime(), fi.ModTime()))
	}
	diffs, err := verifyTree(old, new)
	must(t, err)
	if len(diffs) != 40 {
		t.Fatalf("diffs = %d", len(diffs))
	}
	if err := report(term, "verify found differences", diffs); err == nil {
		t.Fatal("a failed check did not fail")
	}
	b, _ := os.ReadFile(term)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if lines[0] != "verify found differences" || len(lines) > 21 {
		t.Fatalf("termination message:\n%s", b)
	}
}

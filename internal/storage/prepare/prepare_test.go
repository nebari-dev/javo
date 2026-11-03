package prepare

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func mustMode(t *testing.T, s string) os.FileMode {
	t.Helper()
	m, err := ParseMode(s)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunSetsSetgidAndGroupWrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Run(dir, os.Getuid(), os.Getgid(), mustMode(t, "02775"), false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSetgid == 0 || info.Mode().Perm() != 0o775 {
		t.Fatalf("mode = %v, want setgid and 0775", info.Mode())
	}
}

func TestRunCreatesTheDirectoryOnlyWhenAsked(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home")
	if err := Run(dir, os.Getuid(), os.Getgid(), mustMode(t, "0775"), false); err == nil {
		t.Fatal("a missing directory must fail when mkdir is off")
	}
	if err := Run(dir, os.Getuid(), os.Getgid(), mustMode(t, "0775"), true); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}
}

// A driver that ignores fsGroup leaves the volume root in another group and
// another owner, with no write for others: the user could not write their
// home, so the result is a typed error naming the ids, not a silent success.
func TestRunReportsARootTheUserCannotWrite(t *testing.T) {
	dir := t.TempDir()
	err := Run(dir, os.Getuid()+1, os.Getgid()+1, mustMode(t, "0775"), false)
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *prepare.Error", err)
	}
	if pe.UID != os.Getuid()+1 || pe.GID != os.Getgid()+1 || pe.GotUID != os.Getuid() || pe.GotGID != os.Getgid() {
		t.Fatalf("error = %+v", pe)
	}
}

// A volume root the user owns is theirs to write, whatever its group.
func TestRunAcceptsARootTheUserOwns(t *testing.T) {
	if err := Run(t.TempDir(), os.Getuid(), os.Getgid()+1, mustMode(t, "0775"), false); err != nil {
		t.Fatal(err)
	}
}

// A hostPath-backed volume (local-path) gets no fsGroup from the kubelet: its
// root stays root-owned and world-writable. The prepare process cannot chmod
// it, but the user can write there, so it is a usable home. /tmp is such a
// directory on Linux and macOS.
func TestRunAcceptsAWorldWritableRootOwnedBySomeoneElse(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root owns /tmp and could chmod it")
	}
	info, err := os.Stat("/tmp")
	if err != nil || info.Mode().Perm()&0o003 != 0o003 {
		t.Skip("no world-writable /tmp here")
	}
	st := info.Sys().(*syscall.Stat_t)
	if int(st.Uid) == os.Getuid() || int(st.Gid) == os.Getgid() {
		t.Skip("/tmp is owned by this user or group")
	}
	if err := Run("/tmp", os.Getuid(), os.Getgid(), mustMode(t, "0775"), false); err != nil {
		t.Fatal(err)
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]os.FileMode{
		"0775":  0o775,
		"02775": 0o775 | os.ModeSetgid,
		"775":   0o775,
	} {
		if got := mustMode(t, in); got != want {
			t.Errorf("ParseMode(%q) = %v, want %v", in, got, want)
		}
	}
	for _, in := range []string{"", "9", "04755", "01777", "0x1ff"} {
		if _, err := ParseMode(in); err == nil {
			t.Errorf("ParseMode(%q) must fail", in)
		}
	}
}

// A folder's directory is created by the prepare user, so that user owns it
// and the owner bits always pass. Members write through the group: a
// directory left in another group must fail, naming the group it got.
func TestRunReportsAFolderLeftInAnotherGroup(t *testing.T) {
	dir := t.TempDir()
	want := os.Getgid() + 1
	err := Run(dir, os.Getuid(), want, mustMode(t, "02775"), false)
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *prepare.Error", err)
	}
	if pe.GotGID != os.Getgid() || pe.GID != want {
		t.Fatalf("error = %+v", pe)
	}
	if msg := pe.Error(); !strings.Contains(msg, fmt.Sprintf("group %d, want %d", os.Getgid(), want)) {
		t.Fatalf("message %q does not name the group", msg)
	}
}

// Setgid is what makes members' files take the group. A directory in the
// right group that lost setgid (or group write) still fails.
func TestRunReportsAFolderWithoutSetgidOrGroupWrite(t *testing.T) {
	for _, perm := range []os.FileMode{0o775, 0o755 | os.ModeSetgid} {
		dir := t.TempDir()
		// What the storage left behind: Run's own chmod is what the storage
		// would drop, so the stat result goes to check directly.
		if err := os.Chmod(dir, perm); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&(os.ModeSetgid|os.ModePerm) != perm {
			t.Skipf("this filesystem gave %v for %v", info.Mode(), perm)
		}
		var pe *Error
		if err := check(dir, info, os.Getuid(), os.Getgid(), true, nil); !errors.As(err, &pe) {
			t.Errorf("mode %v: err = %v, want *prepare.Error", perm, err)
		}
	}
}

package podcmd

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/nebari-dev/javo/internal/cli/clitest"
	"github.com/nebari-dev/javo/internal/cli/exitcode"
)

// sums runs our sha256sum in dir with args, reading stdin.
func sums(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	t.Chdir(dir)
	root := clitest.Root(NewSha256sum())
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"sha256sum"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// The install step names an environment by the hashes of its files
// (internal/pixienv): sha256sum prints what the real one prints, for
// files and for what it is piped, so a home keeps its environments
// whichever of the two a machine has.
func TestSha256sumPrintsWhatTheRealOnePrints(t *testing.T) {
	dir := t.TempDir()
	for name, text := range map[string]string{"pixi.toml": "[workspace]\n", "pixi.lock": "version: 6\n", "empty": ""} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const empty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	got := sums(t, dir, "", "./empty", "./pixi.toml")
	lines := strings.Split(got, "\n")
	if len(lines) != 3 || lines[0] != empty+"  ./empty" || !strings.HasSuffix(lines[1], "  ./pixi.toml") || lines[2] != "" {
		t.Errorf("for files it prints %q", got)
	}
	if got := sums(t, dir, ""); got != empty+"  -\n" {
		t.Errorf("for an empty pipe it prints %q", got)
	}
	real, err := exec.LookPath("sha256sum")
	if err != nil {
		t.Skip("no sha256sum on this machine to compare with")
	}
	for _, args := range [][]string{{"./pixi.lock", "./empty", "./pixi.toml"}, {}} {
		cmd := exec.Command(real, args...)
		cmd.Dir, cmd.Stdin = dir, strings.NewReader("piped text\n")
		want, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := sums(t, dir, "piped text\n", args...); got != string(want) {
			t.Errorf("sha256sum %q prints %q, the real one %q", args, got, want)
		}
	}
}

func TestSha256sumFailsForAFileItCannotRead(t *testing.T) {
	t.Chdir(t.TempDir())
	root := clitest.Root(NewSha256sum())
	root.SetArgs([]string{"sha256sum", "missing"})
	if err := root.Execute(); err == nil {
		t.Error("no error for a missing file")
	}
}

// flock runs a command, its flags untouched, and ends with its exit code.
func TestFlockRunsTheCommandAndEndsWithItsCode(t *testing.T) {
	dir := t.TempDir()
	lock, out := filepath.Join(dir, "env.lock"), filepath.Join(dir, "out")
	root := clitest.Root(NewFlock())
	root.SetArgs([]string{"flock", lock, "sh", "-c", `printf '%s %s' "$1" "$2" > "$0"; exit 7`, out, "a b", "-x"})
	err := root.Execute()
	var code exitcode.Error
	if !errors.As(err, &code) || code.Code != 7 {
		t.Errorf("flock ended with %v, want exit code 7", err)
	}
	if got, _ := os.ReadFile(out); string(got) != "a b -x" {
		t.Errorf("the command got %q", got)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Errorf("no lock file: %v", err)
	}
}

// Two installs of one environment never run together: while another
// process holds the lock, the command waits.
func TestFlockWaitsForTheLock(t *testing.T) {
	dir := t.TempDir()
	lock, log := filepath.Join(dir, "env.lock"), filepath.Join(dir, "log")
	held, err := os.OpenFile(lock, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		root := clitest.Root(NewFlock())
		root.SetArgs([]string{"flock", lock, "sh", "-c", `echo ran >> "$0"`, log})
		done <- root.Execute()
	}()
	// Written while the lock is held: the command's line comes after it.
	if err := os.WriteFile(log, []byte("held\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(log); string(got) != "held\nran\n" {
		t.Errorf("the log reads %q: the command did not wait for the lock", got)
	}
}

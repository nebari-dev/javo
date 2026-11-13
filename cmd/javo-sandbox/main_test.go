//go:build linux

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The test binary doubles as javo-sandbox: with JAVO_SANDBOX_AS_MAIN set it
// runs main, so each test drives the real command line in a child process
// (Landlock locks the process that calls it, never the test runner).
func TestMain(m *testing.M) {
	if os.Getenv("JAVO_SANDBOX_AS_MAIN") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

type result struct {
	code           int
	stdout, stderr string
}

func sandbox(t *testing.T, args ...string) result {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "JAVO_SANDBOX_AS_MAIN=1")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running javo-sandbox: %v", err)
	}
	return result{code, out.String(), errOut.String()}
}

// systemRO grants read and execute on the folders a shell needs, as the
// agent pods do; only those that exist on this machine.
func systemRO() []string {
	var args []string
	for _, d := range []string{"/usr", "/bin", "/lib", "/lib64", "/etc"} {
		if _, err := os.Stat(d); err == nil {
			args = append(args, "--ro", d)
		}
	}
	return args
}

func TestLockedCommandWritesItsFolderButCannotReadOutside(t *testing.T) {
	project, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}

	args := append([]string{"--rw", project}, systemRO()...)
	args = append(args, "--", "sh", "-c", `echo ok > "$1/out" && cat "$2"`, "sh", project, secret)
	r := sandbox(t, args...)

	if got, err := os.ReadFile(filepath.Join(project, "out")); err != nil || strings.TrimSpace(string(got)) != "ok" {
		t.Errorf("write inside the project: got %q, %v; want ok", got, err)
	}
	if strings.Contains(r.stdout, "s3cret") {
		t.Errorf("the locked command read a file outside its folders: %q", r.stdout)
	}
	if r.code == 0 || !strings.Contains(r.stderr, "Permission denied") {
		t.Errorf("reading outside: exit %d, stderr %q; want a permission error", r.code, r.stderr)
	}
}

// A kernel older than the version asked for fails closed: the command does
// not run at all, rather than run without the lock.
func TestOlderLandlockThanAskedForRefusesToRunTheCommand(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	args := append([]string{"--min-abi", "999", "--rw", filepath.Dir(marker)}, systemRO()...)
	r := sandbox(t, append(args, "--", "touch", marker)...)

	if _, err := os.Stat(marker); err == nil {
		t.Error("the command ran although the kernel's Landlock is older than asked for")
	}
	if r.code == 0 || !strings.Contains(r.stderr, "older than the 999 needed") || !strings.Contains(r.stderr, "not running it") {
		t.Errorf("exit %d, stderr %q; want a refusal naming the version", r.code, r.stderr)
	}
}

// A folder to grant that does not exist is a mistake in the command line,
// so it fails closed too: the command does not run with less than asked.
func TestMissingFolderToGrantRefusesToRunTheCommand(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	args := append([]string{"--rw", dir, "--ro", filepath.Join(dir, "missing")}, systemRO()...)
	r := sandbox(t, append(args, "--", "touch", marker)...)

	if _, err := os.Stat(marker); err == nil {
		t.Error("the command ran although a folder to grant is missing")
	}
	if r.code != 125 || !strings.Contains(r.stderr, "missing") || !strings.Contains(r.stderr, "not running the command") {
		t.Errorf("exit %d, stderr %q; want 125 naming the missing folder", r.code, r.stderr)
	}
}

//go:build linux || darwin

package local

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func program(t *testing.T, dir string, mode os.FileMode) string {
	t.Helper()
	file := filepath.Join(dir, "pixi")
	if err := os.WriteFile(file, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
	// Past the umask.
	if err := os.Chmod(file, mode); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestPixiIsTheOneBesideTheBinaryThenTheOneOnPath(t *testing.T) {
	onPath, bin, empty := t.TempDir(), t.TempDir(), t.TempDir()
	want := program(t, onPath, 0o755)
	t.Setenv("PATH", onPath)
	if got := FindPixi(filepath.Join(empty, "javo")); got != want {
		t.Errorf("with pixi on PATH only: %q, want %q", got, want)
	}
	beside := program(t, bin, 0o755)
	if got := FindPixi(filepath.Join(bin, "javo")); got != beside {
		t.Errorf("with pixi beside the binary: %q, want %q", got, beside)
	}
	t.Setenv("PATH", empty)
	if got := FindPixi(filepath.Join(empty, "javo")); got != "" {
		t.Errorf("with no pixi: %q", got)
	}
}

// A folder or a file anyone can write is where someone else can have put
// a program: the pixi beside the binary is then passed over.
func TestAPixiBesideTheBinaryThatOthersCanWriteIsNotRun(t *testing.T) {
	onPath := t.TempDir()
	want := program(t, onPath, 0o755)
	t.Setenv("PATH", onPath)
	openFolder := t.TempDir()
	program(t, openFolder, 0o755)
	if err := os.Chmod(openFolder, 0o777); err != nil {
		t.Fatal(err)
	}
	openFile := t.TempDir()
	program(t, openFile, 0o777)
	notAProgram := t.TempDir()
	program(t, notAProgram, 0o644)
	for name, dir := range map[string]string{"a folder others can write": openFolder, "a file others can write": openFile, "a file that is not a program": notAProgram} {
		if got := FindPixi(filepath.Join(dir, "javo")); got != want {
			t.Errorf("%s: %q, want the one on PATH", name, got)
		}
	}
}

// The step that installs an environment runs pixi and tells the person
// what its termination message says.
func TestWithoutPixiTheInstallStepSaysWhy(t *testing.T) {
	dir := t.TempDir()
	stand, err := WithoutPixi(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WithoutPixi(dir); err != nil {
		t.Fatalf("a second start: %v", err)
	}
	log := filepath.Join(dir, "termination-log")
	cmd := exec.Command(stand, "install", "--frozen")
	cmd.Env = []string{"JAVO_TERMINATION_LOG=" + log}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("the stand-in for pixi succeeded")
	}
	message, _ := os.ReadFile(log)
	for _, said := range []string{string(out), string(message)} {
		if !strings.Contains(said, "pixi") || !strings.Contains(said, "PATH") || !strings.Contains(said, "install pixi") {
			t.Errorf("it says %q", said)
		}
	}
}

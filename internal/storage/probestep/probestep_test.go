package probestep

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var uid, gid = os.Getuid(), os.Getgid()

func mustRun(t *testing.T, dir, step string) {
	t.Helper()
	if err := Run(dir, step, uid, gid); err != nil {
		t.Fatalf("%s: %v", step, err)
	}
}

func TestWriteCheckConfirmPass(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "write")
	mustRun(t, dir, "check")
	mustRun(t, dir, "confirm")
}

func TestCheckFailsWithoutSetgid(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "write")
	if err := os.Chmod(filepath.Join(dir, "shared"), 0o775); err != nil {
		t.Fatal(err)
	}
	var perr *Error
	err := Run(dir, "check", uid, gid)
	if !errors.As(err, &perr) {
		t.Fatalf("want *Error, got %v", err)
	}
	if perr.Step != "check" || !strings.Contains(perr.Want, "setgid") {
		t.Errorf("got %+v", perr)
	}
}

func TestCheckFailsWhenFileBelongsToAnotherUser(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "write")
	var perr *Error
	err := Run(dir, "check", uid+1, gid)
	if !errors.As(err, &perr) || perr.Step != "check" || !strings.Contains(perr.Want, "owner") {
		t.Fatalf("got %v", err)
	}
}

func TestConfirmFailsWhenCheckNeverAppended(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "write")
	var perr *Error
	err := Run(dir, "confirm", uid, gid)
	if !errors.As(err, &perr) || perr.Step != "confirm" {
		t.Fatalf("got %v", err)
	}
}

func TestWriteFailsOnUnwritableDir(t *testing.T) {
	var perr *Error
	err := Run(filepath.Join(t.TempDir(), "missing"), "write", uid, gid)
	if !errors.As(err, &perr) || perr.Step != "write" {
		t.Fatalf("got %v", err)
	}
}

func TestUnknownStep(t *testing.T) {
	if err := Run(t.TempDir(), "bogus", uid, gid); err == nil {
		t.Fatal("want error")
	}
}

func TestPrepareAcceptsARootTheProbeUserCanWriteInItsGroup(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o775); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "prepare")
}

func TestPrepareFailsWhenTheRootIsNotInTheGroupOrNotWritable(t *testing.T) {
	cases := map[string]struct {
		mode os.FileMode
		gid  int
	}{
		"other group":  {0o775, gid + 1},
		"not writable": {0o555, gid},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, c.mode); err != nil {
				t.Fatal(err)
			}
			var perr *Error
			err := Run(dir, "prepare", uid, c.gid)
			if !errors.As(err, &perr) || perr.Step != "prepare" {
				t.Fatalf("got %v, want *Error at prepare", err)
			}
		})
	}
}

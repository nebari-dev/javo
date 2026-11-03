// Package gocachetrim tests scripts/build/go-cache-trim.sh, which keeps only
// the part of Go's build cache that one CI run used, so the cache CI saves
// holds what the next run needs and nothing older.
package gocachetrim

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, path, content string) {
	t.Helper()
	p := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// run runs name in dir with Go's build cache at cache and returns its output.
func run(t *testing.T, dir, cache string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOCACHE="+cache, "GOTOOLCHAIN=local", "GOFLAGS=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func TestTrimKeepsWhatTheRunUsedAndDropsTheRest(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "build", "go-cache-trim.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cache := filepath.Join(t.TempDir(), "go-build")
	marker := filepath.Join(t.TempDir(), "marker")
	write(t, dir, "go.mod", "module example.com/m\n\ngo 1.24\n")
	write(t, dir, "used/used.go", "package used\n")
	write(t, dir, "unused/unused.go", "package unused\n")

	// A cache restored from an earlier run, holding both packages.
	run(t, dir, cache, "go", "build", "./...")

	run(t, dir, cache, "sh", script, "start", marker)
	run(t, dir, cache, "go", "build", "./used")
	run(t, dir, cache, "sh", script, "finish", marker)

	// go build -v names each package it compiles; a cached one is not named.
	if out := run(t, dir, cache, "go", "build", "-v", "./used"); strings.Contains(out, "example.com/m/used") {
		t.Errorf("the package the run built was compiled again after the trim:\n%s", out)
	}
	if out := run(t, dir, cache, "go", "build", "-v", "./unused"); !strings.Contains(out, "example.com/m/unused") {
		t.Errorf("the package the run did not build is still cached after the trim:\n%s", out)
	}
}

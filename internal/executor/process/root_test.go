//go:build linux || darwin

package process

import (
	"path/filepath"
	"strings"
	"testing"
)

// Run as root, the executor would run every app as root: it refuses to be
// made, before it touches the state directory.
func TestRefusesToRunAsRoot(t *testing.T) {
	was := euid
	euid = func() int { return 0 }
	defer func() { euid = was }()
	dir := filepath.Join(t.TempDir(), "state")
	if _, err := New(Options{Dir: dir}); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("New as uid 0: %v", err)
	}
	if matches, _ := filepath.Glob(dir); len(matches) != 0 {
		t.Fatal("the state directory was made")
	}
}

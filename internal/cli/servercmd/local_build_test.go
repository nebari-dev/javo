//go:build local

package servercmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// A javo built with the tag local carries what `javo local` runs and
// leaves the rest out, so that it is small. What it leaves out it says
// at start, in words that name the build, and never half starts.
const leftOut = "left out of a javo built with the tag local"

// It has no Kubernetes: the server and the gateway asked to run on a
// cluster (the executor of a config that names none) are each refused,
// once. The controller is refused by the same words; its command takes
// the process's signals, which only one test of a package can do
// (TestCommandsRejectInvalidConfig).
func TestALocalBuildRefusesToRunOnACluster(t *testing.T) {
	for name, cmd := range map[string]func() *cobra.Command{"server": NewServer, "gateway": NewGateway} {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(cfg, []byte("auth:\n  local:\n    enabled: true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		args := []string{"--config", cfg, "--database-path", filepath.Join(dir, "javo.db"), "--runner-image", "example.com/javo:1"}
		got := refused(t, cmd(), args...)
		if strings.Count(got, leftOut) != 1 || !strings.Contains(got, "executor kubernetes") {
			t.Errorf("javo %s: %q", name, got)
		}
	}
}

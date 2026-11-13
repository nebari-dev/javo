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

// It has no AI assistant: `javo local` never turns AI on, so the model
// providers' clients are not carried, and a server whose config turns it
// on is refused rather than started with an assistant that cannot answer.
func TestALocalBuildRefusesAnAIAssistant(t *testing.T) {
	dir, args := hostConfig(t, "process")
	cfg := filepath.Join(dir, "config.yaml")
	yaml, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	yaml = append(yaml, "ai:\n  enabled: true\n  provider: openai\n  model: m\n  baseURL: http://127.0.0.1:1\n"...)
	if err := os.WriteFile(cfg, yaml, 0o600); err != nil {
		t.Fatal(err)
	}
	got := refused(t, NewServer(), append(args, "--listen", "127.0.0.1:0")...)
	if !strings.Contains(got, leftOut) || !strings.Contains(got, "ai.enabled") {
		t.Errorf("javo server with ai.enabled: %q", got)
	}
}

// It has no agent endpoint: `javo local` never turns MCP on, so the
// protocol's library is not carried, and a server whose config turns it
// on is refused rather than started without /mcp.
func TestALocalBuildRefusesAnMCPEndpoint(t *testing.T) {
	dir, args := hostConfig(t, "process")
	cfg := filepath.Join(dir, "config.yaml")
	yaml, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	yaml = append(yaml, "mcp:\n  enabled: true\n"...)
	if err := os.WriteFile(cfg, yaml, 0o600); err != nil {
		t.Fatal(err)
	}
	got := refused(t, NewServer(), append(args, "--listen", "127.0.0.1:0")...)
	if !strings.Contains(got, leftOut) || !strings.Contains(got, "mcp.enabled") {
		t.Errorf("javo server with mcp.enabled: %q", got)
	}
}

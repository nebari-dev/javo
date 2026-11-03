package servercmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Both long-running commands refuse to start on a config that does not validate,
// and say which key is wrong.
func TestCommandsRejectInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("auth:\n  local:\n    enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The flags each command requires besides --config.
	required := map[string][]string{
		"controller": {"--runner-image", "example.org/javo:1", "--netprobe-control-url", "http://javo-gateway.javo-system.svc/healthz"},
		"gateway":    {"--runner-image", "example.org/javo:1"},
	}
	for name, newCmd := range map[string]func() *cobra.Command{"controller": NewController, "gateway": NewGateway} {
		t.Run(name, func(t *testing.T) {
			cmd := newCmd()
			cmd.SetArgs(append([]string{"--config", path}, required[name]...))
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "auth") || !strings.Contains(err.Error(), "no way to sign in") {
				t.Fatalf("err = %v, want the config error naming auth", err)
			}
		})
	}
}

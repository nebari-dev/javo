package podcmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nebari-dev/javo/internal/cli/clitest"
)

// The install step reaches pixienv.RegisterKernels through this command.
func TestEnvKernelsOffersTheEnvironmentsKernels(t *testing.T) {
	prefix, to := t.TempDir(), t.TempDir()
	dir := filepath.Join(prefix, "share", "jupyter", "kernels", "python3")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kernel.json"), []byte(`{"argv":["python"],"display_name":"Python 3"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	root := clitest.Root(NewEnvKernels())
	root.SetArgs([]string{"env-kernels", "--prefix", prefix, "--activate", "/a.sh", "--name", "sales", "--to", to})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(to, "kernels", "javo-sales-python3", "kernel.json")); err != nil {
		t.Fatal(err)
	}
}

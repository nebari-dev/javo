package workspace

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/model"
)

// Every shipped app can be shared with anyone, except the Terminal,
// JupyterLab and the coding agents, which stay private.
func TestShippedAppsAreShareableExceptShellsNotebooksAndAgents(t *testing.T) {
	t.Parallel()
	private := []string{"terminal", "jupyterlab", "claude-code", "pi", "herdr"}
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "deploy", "helm", "javo", "files", "templates", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("templates %v: %v", files, err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".yaml")
		tpl := shippedTemplate(t, name)
		if tpl.Spec.JobOnly {
			continue
		}
		want := model.AccessPublic
		if slices.Contains(private, name) {
			want = model.AccessPrivate
		}
		if got := model.MaxSharing(tpl); got != want {
			t.Errorf("%s: max sharing %q, want %q", name, got, want)
		}
	}
}

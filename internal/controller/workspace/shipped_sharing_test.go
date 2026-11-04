package workspace

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/model"
)

// Every shipped app can be shared with anyone, except the code editors, which
// only chosen people may open, and the Terminal, JupyterLab, Whybook and the
// coding agents, which stay private.
func TestShippedAppsShareUpToTheirKindsLimit(t *testing.T) {
	t.Parallel()
	private := []string{"terminal", "jupyterlab", "whybook", "claude-code", "pi", "herdr"}
	editors := []string{"vscode", "rstudio", "marimo", "pluto"}
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
		switch {
		case slices.Contains(private, name):
			want = model.AccessPrivate
		case slices.Contains(editors, name):
			want = model.AccessRestricted
		}
		if got := model.MaxSharing(tpl); got != want {
			t.Errorf("%s: max sharing %q, want %q", name, got, want)
		}
	}
}

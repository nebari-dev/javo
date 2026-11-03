package podcmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/cli/clitest"
	"github.com/nebari-dev/javo/internal/cli/exitcode"
)

const checkSHA = "0123456789abcdef0123456789abcdef01234567"

func cloneWith(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	wt := filepath.Join(root, ".worktrees", checkSHA)
	for _, f := range files {
		p := filepath.Join(wt, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "current")
	if err := os.Symlink(filepath.Join(".worktrees", checkSHA), link); err != nil {
		t.Fatal(err)
	}
	return link
}

func TestSourceCheckWritesTheCommitAsTerminationMessage(t *testing.T) {
	link := cloneWith(t, "web/app.py")
	term := filepath.Join(t.TempDir(), "term")
	root := clitest.Root(NewSourceCheck())
	root.SetArgs([]string{"source-check", "--link=" + link, "--path=web", "--file=app.py", "--termination-log=" + term})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(term)
	if err != nil || string(got) != "commit="+checkSHA {
		t.Fatalf("termination message %q, %v", got, err)
	}
}

func TestSourceCheckMissingFileExitsOneAndSaysWhich(t *testing.T) {
	link := cloneWith(t, "web/other.py")
	term := filepath.Join(t.TempDir(), "term")
	var stderr bytes.Buffer
	root := clitest.Root(NewSourceCheck())
	root.SetErr(&stderr)
	root.SetArgs([]string{"source-check", "--link=" + link, "--path=web", "--file=app.py", "--termination-log=" + term})
	err := root.Execute()
	var ee exitcode.Error
	if !errors.As(err, &ee) || ee.Code != 1 {
		t.Fatalf("err = %v, want exit code 1", err)
	}
	if !strings.Contains(stderr.String(), "file-missing: app.py") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if _, err := os.Stat(term); err == nil {
		t.Fatal("a failed check must not write a commit")
	}
}

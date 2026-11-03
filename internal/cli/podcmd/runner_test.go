package podcmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/cli/clitest"
	"github.com/nebari-dev/javo/internal/cli/exitcode"
	"github.com/nebari-dev/javo/internal/cli/wiring"
	"github.com/nebari-dev/javo/internal/jobs/runner"
)

func TestRunnerInstallCopiesTheBinary(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(self)
	if err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(t.TempDir(), "bin", "javo")
	root := clitest.Root(NewRunner(wiring.Engines()))
	root.SetArgs([]string{"runner", "install", "--to", to})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(to)
	if err != nil {
		t.Fatal(err)
	}
	if got.Size() != want.Size() || got.Mode().Perm() != 0o755 {
		t.Errorf("installed size %d mode %v, want size %d mode 0755", got.Size(), got.Mode().Perm(), want.Size())
	}
}

func TestRunnerReturnsTheRunExitCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JAVO_RUN", `{"Definition":"d","Engine":"papermill","Source":"missing.ipynb"}`)
	t.Setenv("JAVO_RUN_ID", "r1")
	root := clitest.Root(NewRunner(wiring.Engines()))
	root.SetArgs([]string{"runner", "--home", home, "--termination-log", filepath.Join(home, "term")})
	err := root.Execute()
	var ee exitcode.Error
	if !errors.As(err, &ee) || ee.Code != runner.ExitSourceMissing {
		t.Fatalf("err = %v, want exit code %d", err, runner.ExitSourceMissing)
	}
}

func TestRunnerBrokenSpecFailsWithReasonAndTemplateExitCode(t *testing.T) {
	cases := []struct{ name, run, runID string }{
		{"JAVO_RUN unset", "", "r1"},
		{"JAVO_RUN not JSON", "{", "r1"},
		{"JAVO_RUN_ID unset", `{"Definition":"d","Engine":"command"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			term := filepath.Join(home, "term")
			t.Setenv("JAVO_RUN", c.run)
			t.Setenv("JAVO_RUN_ID", c.runID)
			root := clitest.Root(NewRunner(wiring.Engines()))
			root.SetArgs([]string{"runner", "--home", home, "--termination-log", term})
			err := root.Execute()
			var ee exitcode.Error
			if !errors.As(err, &ee) || ee.Code != runner.ExitTemplateError {
				t.Fatalf("err = %v, want exit code %d", err, runner.ExitTemplateError)
			}
			b, err := os.ReadFile(term)
			if err != nil || !strings.Contains(string(b), `"reason":"TemplateInvalid"`) {
				t.Errorf("termination message = %q, err %v", b, err)
			}
		})
	}
}

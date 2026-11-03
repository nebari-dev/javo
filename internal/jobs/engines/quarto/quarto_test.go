package quarto_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/jobs/engines/quarto"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestStepsRenderIntoTheRunFolder(t *testing.T) {
	got, err := quarto.New().Steps(engine.Run{Source: "/h/jobs/a/r1/report.qmd", RunDir: "/h/jobs/a/r1", ParamsFile: "/h/jobs/a/r1/params.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"quarto", "render", "/h/jobs/a/r1/report.qmd", "--execute", "--output-dir", "/h/jobs/a/r1",
		"--execute-params", "/h/jobs/a/r1/params.yaml"}}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("Steps = %q\nwant %q", got, want)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// render-help.txt is `quarto render --help` of Quarto 1.10.19, the version
// the fixtures come from.
func TestStepsUseOnlyFlagsTheRenderHelpLists(t *testing.T) {
	help := string(fixture(t, "render-help.txt"))
	steps, _ := quarto.New().Steps(engine.Run{Source: "/s.qmd", RunDir: "/r", ParamsFile: "/r/p.yaml"})
	for _, a := range steps[0] {
		if strings.HasPrefix(a, "--") && !strings.Contains(help, a) {
			t.Errorf("%s is not in quarto render --help", a)
		}
	}
}

func TestClassifyCellErrorReportsTheExceptionLine(t *testing.T) {
	got := quarto.New().Classify(engine.Run{Source: "/r/report.qmd", RunDir: "/r"}, 0, 1, fixture(t, "cell-failed.log"))
	if got.Reason != string(reasons.CellError) || got.Message != "KeyError: 'region'" {
		t.Errorf("Classify = %+v", got)
	}
	if len(got.Outputs) != 0 {
		t.Errorf("Outputs = %v, want none for a failed render", got.Outputs)
	}
}

func TestClassifyQuartoErrorLineWinsOverTheStackTrace(t *testing.T) {
	got := quarto.New().Classify(engine.Run{}, 0, 1, fixture(t, "yaml-failed.log"))
	want := "ERROR: YAMLException: missed comma between flow collection entries (3:1)"
	if got.Reason != string(reasons.CellError) || got.Message != want {
		t.Errorf("Classify = %+v", got)
	}
}

func TestClassifyFailureWithoutAnErrorLineUsesTheLastLine(t *testing.T) {
	got := quarto.New().Classify(engine.Run{}, 0, 1, []byte("something broke\n"))
	if got.Message != "something broke" {
		t.Errorf("Message = %q", got.Message)
	}
}

func TestClassifySuccessListsTheRenderedHTMLOfEitherSourceType(t *testing.T) {
	for _, src := range []string{"/r/report.qmd", "/r/report.ipynb"} {
		got := quarto.New().Classify(engine.Run{Source: src, RunDir: "/r"}, 0, 0, fixture(t, "render-ok.log"))
		if got.Reason != "" || !slices.Equal(got.Outputs, []string{"/r/report.html"}) {
			t.Errorf("%s: Classify = %+v", src, got)
		}
	}
}

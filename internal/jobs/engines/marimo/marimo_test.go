// The fixture is real output of marimo 0.25.1 (javo python-apps image):
// `marimo export html` on a notebook whose second cell raises KeyError,
// exit status 1.
package marimo_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/jobs/engines/marimo"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestStepsPassParametersAfterDoubleDashInNameOrder(t *testing.T) {
	r := engine.Run{
		Source: "/h/jobs/a/r1/app.py", RunDir: "/h/jobs/a/r1",
		Params: map[string]string{"region": "emea; rm -rf ~", "limit": "5"},
	}
	got, err := marimo.New().Steps(r)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"marimo", "export", "html", "/h/jobs/a/r1/app.py", "-o", "/h/jobs/a/r1/output.html",
		"--", "--limit", "5", "--region", "emea; rm -rf ~"}}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("Steps = %q\nwant %q", got, want)
	}
}

func TestStepsWithoutParametersHaveNoDoubleDash(t *testing.T) {
	got, _ := marimo.New().Steps(engine.Run{Source: "/s.py", RunDir: "/r"})
	if slices.Contains(got[0], "--") {
		t.Errorf("unexpected -- in %q", got[0])
	}
}

func TestClassifyFailedCellReportsItsErrorLine(t *testing.T) {
	tail, err := os.ReadFile(filepath.Join("testdata", "cell-failed.log"))
	if err != nil {
		t.Fatal(err)
	}
	got := marimo.New().Classify(engine.Run{RunDir: "/r"}, 0, 1, tail)
	if got.Reason != string(reasons.CellError) {
		t.Fatalf("Reason = %q", got.Reason)
	}
	if want := "MarimoExceptionRaisedError: 'region'"; got.Message != want {
		t.Errorf("Message = %q, want %q", got.Message, want)
	}
}

func TestClassifySuccessHasNoReasonAndListsHTML(t *testing.T) {
	got := marimo.New().Classify(engine.Run{RunDir: "/r"}, 0, 0, nil)
	if got.Reason != "" || !slices.Equal(got.Outputs, []string{"/r/output.html"}) {
		t.Errorf("Classify = %+v", got)
	}
}

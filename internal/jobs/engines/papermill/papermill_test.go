// Fixtures in testdata are real outputs of papermill 2.7.0 and nbconvert
// 7.17.1 (quay.io/jupyter/base-notebook, python 3.13), captured by running
// the commands from Steps on a notebook whose last cell raises KeyError, a
// notebook that calls os._exit, and a run with -k nokernel.
package papermill_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/jobs/engines/papermill"
	"github.com/nebari-dev/javo/internal/reasons"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func runWithNotebook(t *testing.T, fixture string) engine.Run {
	t.Helper()
	dir := t.TempDir()
	if fixture != "" {
		if err := os.WriteFile(filepath.Join(dir, "output.ipynb"), read(t, fixture), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return engine.Run{RunDir: dir, Source: filepath.Join(dir, "notebook.ipynb"), Kernel: "python3"}
}

func TestStepsAreTwoArgvSlices(t *testing.T) {
	r := engine.Run{
		Source:     "/h/jobs/a/r1/notebook.ipynb",
		RunDir:     "/h/jobs/a/r1",
		Kernel:     "ir",
		ParamsFile: "/h/jobs/a/r1/params.yaml",
	}
	got, err := papermill.New().Steps(r)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"papermill", "/h/jobs/a/r1/notebook.ipynb", "/h/jobs/a/r1/output.ipynb", "-k", "ir",
			"-f", "/h/jobs/a/r1/params.yaml", "--cwd", "/h/jobs/a/r1", "--log-output", "--request-save-on-cell-execute"},
		{"jupyter", "nbconvert", "--to", "html", "/h/jobs/a/r1/output.ipynb"},
	}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("Steps = %q\nwant %q", got, want)
	}
}

func TestStepsPassKernelWithSpaceAsOneElementAndAddCellTimeout(t *testing.T) {
	r := engine.Run{Source: "/s.ipynb", RunDir: "/r", Kernel: "my kernel", ParamsFile: "/r/p.yaml", CellTimeout: 90e9}
	steps, _ := papermill.New().Steps(r)
	first := steps[0]
	if i := slices.Index(first, "-k"); i < 0 || first[i+1] != "my kernel" {
		t.Errorf("kernel not one element: %q", first)
	}
	if i := slices.Index(first, "--execution-timeout"); i < 0 || first[i+1] != "90" {
		t.Errorf("no --execution-timeout 90: %q", first)
	}
}

func TestClassifyFirstErrorCellSkipsInjectedParameters(t *testing.T) {
	r := runWithNotebook(t, "error-cell.out.ipynb")
	got := papermill.New().Classify(r, 0, 1, read(t, "error-cell.tail.txt"))
	if got.Reason != string(reasons.CellError) {
		t.Fatalf("Reason = %q", got.Reason)
	}
	// The notebook has 8 cells of the user's; papermill adds a ninth with the parameters.
	if want := "Cell 8 raised KeyError: 'region'"; got.Message != want {
		t.Errorf("Message = %q, want %q", got.Message, want)
	}
}

func TestClassifyKernelDiedOnExit138(t *testing.T) {
	got := papermill.New().Classify(runWithNotebook(t, ""), 0, 138, read(t, "kernel-died.tail.txt"))
	if got.Reason != string(reasons.KernelDied) {
		t.Errorf("Reason = %q", got.Reason)
	}
}

func TestClassifyKernelMissingNamesTheKernel(t *testing.T) {
	got := papermill.New().Classify(runWithNotebook(t, ""), 0, 1, read(t, "no-such-kernel.tail.txt"))
	if got.Reason != string(reasons.KernelMissing) {
		t.Fatalf("Reason = %q", got.Reason)
	}
	if want := `The kernel "nokernel" is not in this image`; got.Message != want {
		t.Errorf("Message = %q, want %q", got.Message, want)
	}
}

func TestClassifySuccessListsNotebookThenHTML(t *testing.T) {
	r := runWithNotebook(t, "")
	e := papermill.New()
	if got := e.Classify(r, 0, 0, nil); got.Reason != "" || !slices.Equal(got.Outputs, []string{filepath.Join(r.RunDir, "output.ipynb")}) {
		t.Errorf("step 0 = %+v", got)
	}
	if got := e.Classify(r, 1, 0, nil); got.Reason != "" || !slices.Equal(got.Outputs, []string{filepath.Join(r.RunDir, "output.html")}) {
		t.Errorf("step 1 = %+v", got)
	}
}

func TestClassifyHTMLConversionFailureDoesNotFailTheRun(t *testing.T) {
	if got := papermill.New().Classify(runWithNotebook(t, ""), 1, 1, []byte("nbconvert failed")); got.Reason != "" {
		t.Errorf("Reason = %q, want none: the executed notebook is the result", got.Reason)
	}
}

func TestMetadata(t *testing.T) {
	e := papermill.New()
	if e.ID() != "papermill" || !slices.Equal(e.Extensions(), []string{".ipynb"}) ||
		!slices.Equal(e.Binaries(), []string{"papermill", "jupyter"}) {
		t.Errorf("metadata: %s %v %v", e.ID(), e.Extensions(), e.Binaries())
	}
}

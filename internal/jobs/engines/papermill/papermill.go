// Package papermill runs .ipynb notebooks with papermill and renders the
// executed notebook to HTML with nbconvert.
package papermill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"

	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/reasons"
)

// exitKernelDied is papermill's exit status when the kernel dies mid-cell.
const exitKernelDied = 138

var noSuchKernel = regexp.MustCompile(`NoSuchKernel: No such kernel named (\S+)`)

// Engine implements engine.Engine.
type Engine struct{}

func New() Engine { return Engine{} }

func (Engine) ID() string           { return "papermill" }
func (Engine) DisplayName() string  { return "Jupyter notebook (papermill)" }
func (Engine) Extensions() []string { return []string{".ipynb"} }
func (Engine) Binaries() []string   { return []string{"papermill", "jupyter"} }

func (Engine) Steps(r engine.Run) ([][]string, error) {
	out := filepath.Join(r.RunDir, "output.ipynb")
	run := []string{"papermill", r.Source, out, "-k", r.Kernel, "-f", r.ParamsFile,
		"--cwd", r.RunDir, "--log-output", "--request-save-on-cell-execute"}
	if r.CellTimeout > 0 {
		run = append(run, "--execution-timeout", strconv.Itoa(int(r.CellTimeout.Seconds())))
	}
	// The HTML step runs even after a failed run, so a partial notebook is viewable.
	return [][]string{run, {"jupyter", "nbconvert", "--to", "html", out}}, nil
}

func (Engine) Env(engine.Run) []string { return nil }

func (Engine) Classify(r engine.Run, step, exitCode int, logTail []byte) engine.Result {
	if step == 1 {
		// The HTML is a convenience; the executed notebook is the result.
		if exitCode != 0 {
			return engine.Result{}
		}
		return engine.Result{Outputs: []string{filepath.Join(r.RunDir, "output.html")}}
	}
	out := filepath.Join(r.RunDir, "output.ipynb")
	switch {
	case exitCode == 0:
		return engine.Result{Outputs: []string{out}}
	case exitCode == exitKernelDied:
		return engine.Result{Reason: string(reasons.KernelDied), Message: "The kernel stopped while running a cell", Outputs: []string{out}}
	}
	if m := noSuchKernel.FindSubmatch(logTail); m != nil {
		return engine.Result{Reason: string(reasons.KernelMissing), Message: fmt.Sprintf("The kernel %q is not in this image", m[1])}
	}
	msg := engine.LastLine(logTail)
	if n, ename, evalue, ok := firstError(out); ok {
		msg = fmt.Sprintf("Cell %d raised %s: %s", n, ename, evalue)
	}
	return engine.Result{Reason: string(reasons.CellError), Message: msg, Outputs: []string{out}}
}

type notebook struct {
	Cells []struct {
		CellType string `json:"cell_type"`
		Metadata struct {
			Tags []string `json:"tags"`
		} `json:"metadata"`
		Outputs []struct {
			OutputType string `json:"output_type"`
			Ename      string `json:"ename"`
			Evalue     string `json:"evalue"`
		} `json:"outputs"`
	} `json:"cells"`
}

// firstError finds the first code cell with an error output. Its number counts
// the user's code cells only: the cell papermill injects for parameters is skipped.
func firstError(path string) (n int, ename, evalue string, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, "", "", false
	}
	var nb notebook
	if json.Unmarshal(b, &nb) != nil {
		return 0, "", "", false
	}
	for _, c := range nb.Cells {
		if c.CellType != "code" || slices.Contains(c.Metadata.Tags, "injected-parameters") {
			continue
		}
		n++
		for _, o := range c.Outputs {
			if o.OutputType == "error" {
				return n, o.Ename, o.Evalue, true
			}
		}
	}
	return 0, "", "", false
}

// Package marimo runs marimo notebooks (.py) by exporting them to HTML.
package marimo

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/reasons"
)

// cellsFailed is the text marimo prints when the export worked but a cell raised.
const cellsFailed = "some cells failed to execute"

type Engine struct{}

func New() Engine { return Engine{} }

func (Engine) ID() string           { return "marimo" }
func (Engine) DisplayName() string  { return "marimo notebook" }
func (Engine) Extensions() []string { return []string{".py"} }
func (Engine) Binaries() []string   { return []string{"marimo"} }

func (Engine) Steps(r engine.Run) ([][]string, error) {
	argv := []string{"marimo", "export", "html", r.Source, "-o", filepath.Join(r.RunDir, "output.html")}
	if len(r.Params) > 0 {
		argv = append(argv, "--")
		for _, name := range slices.Sorted(maps.Keys(r.Params)) {
			argv = append(argv, "--"+name, r.Params[name])
		}
	}
	return [][]string{argv}, nil
}

func (Engine) Env(engine.Run) []string { return nil }

func (Engine) Classify(r engine.Run, _ int, exitCode int, logTail []byte) engine.Result {
	out := []string{filepath.Join(r.RunDir, "output.html")}
	if exitCode == 0 {
		return engine.Result{Outputs: out}
	}
	// The failing cell's error is the line before marimo's summary line.
	lines := strings.Split(strings.TrimSpace(string(logTail)), "\n")
	msg := strings.TrimSpace(lines[len(lines)-1])
	for i, l := range lines {
		if i > 0 && strings.Contains(l, cellsFailed) {
			msg = strings.TrimSpace(lines[i-1])
			break
		}
	}
	return engine.Result{Reason: string(reasons.CellError), Message: msg, Outputs: out}
}

// Package quarto renders Quarto documents (.qmd, .ipynb) into the run folder.
// --execute-params needs the Python package papermill next to Quarto; without
// it every render fails with "The papermill package is required".
package quarto

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/reasons"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

type Engine struct{}

func New() Engine { return Engine{} }

func (Engine) ID() string           { return "quarto" }
func (Engine) DisplayName() string  { return "Quarto document" }
func (Engine) Extensions() []string { return []string{".qmd", ".ipynb"} }
func (Engine) Binaries() []string   { return []string{"quarto"} }

func (Engine) Steps(r engine.Run) ([][]string, error) {
	return [][]string{{"quarto", "render", r.Source, "--execute", "--output-dir", r.RunDir, "--execute-params", r.ParamsFile}}, nil
}

// Classify reads Quarto 1.10.19 output (fixtures in testdata). Quarto prints
// "ERROR: ..." for its own failures. A failing cell prints the traceback
// followed by "WARN: Error encountered when rendering files", so the message
// is the last line before that WARN line. Colour codes are stripped first.
func (Engine) Env(engine.Run) []string { return nil }

func (Engine) Classify(r engine.Run, _ int, exitCode int, logTail []byte) engine.Result {
	if exitCode == 0 {
		// No --to is passed, so the document renders in its default format, html.
		stem := strings.TrimSuffix(filepath.Base(r.Source), filepath.Ext(r.Source))
		return engine.Result{Outputs: []string{filepath.Join(r.RunDir, stem+".html")}}
	}
	clean := []byte(ansi.ReplaceAllString(string(logTail), ""))
	var errLine string
	var lines []string
	for _, l := range strings.Split(string(clean), "\n") {
		s := strings.TrimSpace(l)
		if strings.HasPrefix(s, "ERROR:") {
			errLine = s
		}
		if s != "" && !strings.HasPrefix(s, "WARN:") {
			lines = append(lines, s)
		}
	}
	msg := errLine
	if msg == "" && len(lines) > 0 {
		msg = lines[len(lines)-1]
	}
	return engine.Result{Reason: string(reasons.CellError), Message: msg}
}

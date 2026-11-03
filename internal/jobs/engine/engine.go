// Package engine is the plug-in point for job engines. An engine only
// describes how to run a file (argv per step) and how to read the outcome; the
// runner executes the steps. Engines are registered in internal/cli/wiring.
package engine

import (
	"bytes"
	"strings"
	"time"

	"github.com/nebari-dev/javo/internal/core/registry"
)

// Run is everything an engine needs to describe one run.
type Run struct {
	Source      string            // absolute path of the snapshot in the run folder
	RunDir      string            // absolute run folder, the working directory unless the engine is a HomeRunner
	Home        string            // absolute home folder
	Kernel      string            // papermill only
	Command     []string          // command engine only
	ParamsFile  string            // YAML written by the runner
	Params      map[string]string // typed values as strings, for engines that take argv
	CellTimeout time.Duration
}

// Result is the outcome of one step. Reason is a catalog code from
// internal/reasons, empty when the step succeeded.
type Result struct {
	Reason  string
	Message string
	Outputs []string
}

// Engine runs one kind of file. Implementations never start processes.
type Engine interface {
	ID() string          // "papermill"
	DisplayName() string // "Jupyter notebook (papermill)"
	Extensions() []string
	// Binaries are the executables that must be on PATH, for EngineMissing.
	Binaries() []string
	// Steps returns one argv per step, run in order, never through a shell.
	Steps(r Run) ([][]string, error)
	// Env is extra environment for every step, KEY=value; nil for engines
	// that take parameters through argv.
	Env(r Run) []string
	// Classify reads the exit code and the log tail of step number step.
	Classify(r Run, step int, exitCode int, logTail []byte) Result
}

// HomeRunner is an engine whose steps run in the home folder instead of the
// run folder, so relative paths name the person's own files (as an app's
// typed command does). Its log and result still go to the run folder.
type HomeRunner interface {
	RunsInHome() bool
}

// WorkDir is where r's steps run with engine e.
func WorkDir(e Engine, r Run) string {
	if h, ok := e.(HomeRunner); ok && h.RunsInHome() {
		return r.Home
	}
	return r.RunDir
}

// Registry holds the engines by id.
type Registry = registry.Registry[Engine]

// LastLine returns the last non-blank line of a log tail, trimmed.
func LastLine(tail []byte) string {
	lines := bytes.Split(bytes.TrimSpace(tail), []byte("\n"))
	return strings.TrimSpace(string(lines[len(lines)-1]))
}

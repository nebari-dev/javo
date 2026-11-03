// Package bash runs a script a person typed through a login bash, in the
// home folder. It is a command job (parameters in the environment, the same
// outcome rules) whose one word is the script, so variables, pipes and
// several lines work as in a terminal.
package bash

import (
	"errors"

	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/jobs/engines/command"
)

// Engine is the command engine with its own id and steps; Env, Classify and
// RunsInHome are the command engine's.
type Engine struct{ command.Engine }

func New() Engine { return Engine{command.New()} }

func (Engine) ID() string          { return "bash" }
func (Engine) DisplayName() string { return "Bash script" }
func (Engine) Binaries() []string  { return []string{"bash"} }

// Steps runs the script, the job's only command word, as bash -lc <script>.
func (Engine) Steps(r engine.Run) ([][]string, error) {
	if len(r.Command) != 1 || r.Command[0] == "" {
		return nil, errors.New("a bash job has exactly one non-empty script")
	}
	return [][]string{{"bash", "-lc", r.Command[0]}}, nil
}

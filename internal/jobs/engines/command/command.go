// Package command runs a plain command given as argv, in the home folder.
// Parameters reach it through the environment, never argv or a shell.
package command

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/reasons"
)

type Engine struct{}

func New() Engine { return Engine{} }

func (Engine) ID() string           { return "command" }
func (Engine) DisplayName() string  { return "Command" }
func (Engine) Extensions() []string { return nil }
func (Engine) Binaries() []string   { return nil }

// RunsInHome: a command names the person's files relative to the home folder.
func (Engine) RunsInHome() bool { return true }

func (Engine) Steps(r engine.Run) ([][]string, error) {
	if len(r.Command) == 0 {
		return nil, errors.New("command is empty")
	}
	return [][]string{slices.Clone(r.Command)}, nil
}

// Env is JAVO_PARAMS_FILE plus one JAVO_PARAM_<NAME> per parameter, in name order.
func (Engine) Env(r engine.Run) []string {
	env := []string{"JAVO_PARAMS_FILE=" + r.ParamsFile}
	for _, name := range slices.Sorted(maps.Keys(r.Params)) {
		key := strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
		env = append(env, "JAVO_PARAM_"+key+"="+r.Params[name])
	}
	return env
}

func (Engine) Classify(_ engine.Run, _ int, exitCode int, logTail []byte) engine.Result {
	if exitCode == 0 {
		return engine.Result{}
	}
	msg := fmt.Sprintf("The command exited with code %d", exitCode)
	if last := engine.LastLine(logTail); last != "" {
		msg += ": " + last
	}
	return engine.Result{Reason: string(reasons.CommandFailed), Message: msg}
}

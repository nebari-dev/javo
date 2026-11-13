// Package runner executes one job run: it snapshots the source into a run
// folder in the user's home, runs the engine's steps, tees the log and writes
// result.json plus the container's termination message. It needs only the
// filesystem: no Kubernetes API and no network.
package runner

import (
	"context"
	"errors"
	"fmt"
	"github.com/nebari-dev/javo/internal/jobs/runspec"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/nebari-dev/javo/internal/jobs/engine"
	jobstatus "github.com/nebari-dev/javo/internal/jobs/status"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Exit codes. podFailurePolicy maps them to "fail now" or "retry".
const (
	ExitOK            = 0
	ExitUserError     = 10
	ExitKernelDied    = 11
	ExitTemplateError = 12
	ExitSourceMissing = 14
	ExitOutputWrite   = 15
	ExitParams        = 16
)

// Spec and Param are shared with the controller and status; see runspec.
type (
	Spec  = runspec.Spec
	Param = runspec.Param
)

// ExecFunc runs one step's argv in dir with env, sending its output to out,
// and returns the step's exit code.
type ExecFunc func(ctx context.Context, argv []string, dir string, env []string, out io.Writer) (exitCode int, err error)

// Config is what the runner needs from its surroundings.
type Config struct {
	Home           string
	RunID          string
	TerminationLog string
	Registry       *engine.Registry
	Exec           ExecFunc
	Log            io.Writer // live log copy; os.Stdout when nil
}

// DefaultExec runs argv directly (no shell) and sends stdout and stderr to out.
// A non-zero exit is a result, not an error; a missing binary wraps exec.ErrNotFound.
// The engine runs in its own process group so cancelling kills its children
// (a papermill kernel) too; WaitDelay stops a leftover child that still holds
// the output pipe from delaying the result past the pod's grace period.
func DefaultExec(ctx context.Context, argv []string, dir string, env []string, out io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, err
}

// PodUserScript is where a pod with the platform's pod files has
// pod-user.sh (pod/files, internal/podfiles), which runs a command in the
// pod's environment as the pod's user.
const PodUserScript = "/opt/javo/pod-user.sh"

// PodUser is where this run has pod-user.sh: in the folder JAVO_FILES_DIR
// names, on a machine that runs the run as plain processes and has the
// platform's files in a folder of its own (the script reads the same
// variable), and at PodUserScript in a pod.
func PodUser(getenv func(string) string) string {
	if dir := getenv("JAVO_FILES_DIR"); dir != "" {
		return filepath.Join(dir, filepath.Base(PodUserScript))
	}
	return PodUserScript
}

// WithPodUser returns run that starts each step through script when the pod
// has it, so the engine and what it starts find the programs of the pod's
// environment (on the PATH only once the script has activated it) and the
// pod's user name (git, getpass, a shell prompt). Without the script, steps
// run directly. A step whose program is not installed still fails with
// exec.ErrNotFound: it is looked up through the script too.
func WithPodUser(script string, run ExecFunc) ExecFunc {
	return func(ctx context.Context, argv []string, dir string, env []string, out io.Writer) (int, error) {
		if fi, err := os.Stat(script); err != nil || fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
			return run(ctx, argv, dir, env, out)
		}
		look := exec.CommandContext(ctx, script, "sh", "-c", `command -v -- "$1" >/dev/null`, "sh", argv[0])
		look.Dir, look.Env = dir, env
		if err := look.Run(); err != nil {
			return 0, fmt.Errorf("%s: %w", argv[0], exec.ErrNotFound)
		}
		return run(ctx, append([]string{script}, argv...), dir, env, out)
	}
}

// exitFor maps a failure reason to the runner's exit code.
func exitFor(reason string) int {
	switch reasons.Code(reason) {
	case reasons.KernelDied:
		return ExitKernelDied
	case reasons.EngineMissing, reasons.KernelMissing, reasons.TemplateInvalid:
		return ExitTemplateError
	case reasons.SourceMissing:
		return ExitSourceMissing
	case reasons.OutputWriteFailed:
		return ExitOutputWrite
	case reasons.ParametersInvalid:
		return ExitParams
	}
	return ExitUserError
}

// Run executes the run and returns the process exit code.
func Run(ctx context.Context, cfg Config, s Spec) int {
	if !isPathElement(s.Definition) || !isPathElement(cfg.RunID) {
		return Fail(cfg.TerminationLog, reasons.TemplateInvalid,
			fmt.Sprintf("The job definition %q or run id %q is not a plain folder name", s.Definition, cfg.RunID))
	}
	started := time.Now().UTC()
	rel := filepath.Join("jobs", s.Definition, cfg.RunID)
	runDir := filepath.Join(cfg.Home, rel)
	res := result{Outcome: "Failed", StartedAt: started, Outputs: []string{}}

	finish := func(reason, message string) int {
		res.FinishedAt = time.Now().UTC()
		if reason == "" {
			res.Outcome = "Succeeded"
			reason = string(reasons.Succeeded)
		} else {
			res.Reason, res.Message = reason, message
		}
		// A full disk must not hide the outcome from the termination message.
		if b, err := jsonIndent(res); err == nil {
			if err := os.WriteFile(filepath.Join(runDir, "result.json"), b, 0o644); isNoSpace(err) {
				reason, message = string(reasons.OutputWriteFailed), "The home folder is full, so result.json could not be written"
			}
		}
		writeTermination(cfg.TerminationLog, jobstatus.Termination{
			Reason: reason, Message: message, OutputPath: filepath.ToSlash(rel), Outputs: res.Outputs,
		})
		prune(filepath.Dir(runDir), cfg.RunID, s.KeepRuns)
		if reason == string(reasons.Succeeded) {
			return ExitOK
		}
		return exitFor(reason)
	}

	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return finish(string(reasons.OutputWriteFailed), "Cannot create the run folder: "+err.Error())
	}
	logFile, err := os.Create(filepath.Join(runDir, "run.log"))
	if err != nil {
		return finish(string(reasons.OutputWriteFailed), "Cannot create the run log: "+err.Error())
	}
	defer logFile.Close()
	live := cfg.Log
	if live == nil {
		live = os.Stdout
	}
	fileLog := &softWriter{w: logFile}

	eng, ok := cfg.Registry.Get(s.Engine)
	if !ok {
		return finish(string(reasons.EngineMissing), fmt.Sprintf("This image has no engine %q", s.Engine))
	}
	vals, err := typedParams(s.Params)
	if err != nil {
		return finish(string(reasons.ParametersInvalid), err.Error())
	}

	run := engine.Run{
		RunDir: runDir, Home: cfg.Home, Kernel: s.Kernel, Command: s.Command, CellTimeout: s.CellTimeout,
		ParamsFile: filepath.Join(runDir, "params.yaml"), Params: map[string]string{},
	}
	for _, p := range s.Params {
		run.Params[p.Name] = p.Value
	}
	if s.Source != "" {
		if !filepath.IsLocal(s.Source) {
			return finish(string(reasons.SourceMissing), s.Source+" was not found in your home folder")
		}
		run.Source = filepath.Join(runDir, "input"+filepath.Ext(s.Source))
		if err := CopyFile(filepath.Join(cfg.Home, s.Source), run.Source, 0o644); err != nil {
			if os.IsNotExist(err) {
				return finish(string(reasons.SourceMissing), s.Source+" was not found in your home folder")
			}
			return finish(sourceCopyReason(err), "Cannot copy "+s.Source+" into the run folder: "+err.Error())
		}
	}
	if err := writeParams(run.ParamsFile, vals); err != nil {
		return finish(string(reasons.OutputWriteFailed), "Cannot write the parameters file: "+err.Error())
	}

	steps, err := eng.Steps(run)
	if err != nil {
		return finish(string(reasons.CommandFailed), err.Error())
	}
	env := append(os.Environ(), eng.Env(run)...)
	dir := engine.WorkDir(eng, run)
	var failReason, failMessage string
	for i, argv := range steps {
		if ctx.Err() != nil {
			break // SIGTERM: start no further step
		}
		var t tail
		code, err := cfg.Exec(ctx, argv, dir, env, io.MultiWriter(fileLog, live, &t))
		if ctx.Err() != nil {
			break // the engine was killed; its error or exit code is not the cause
		}
		if errors.Is(err, exec.ErrNotFound) {
			return finish(string(reasons.EngineMissing), fmt.Sprintf("%s is not installed in this image", argv[0]))
		}
		if err != nil {
			return finish(string(reasons.CommandFailed), fmt.Sprintf("Cannot start %s: %v", argv[0], err))
		}
		r := eng.Classify(run, i, code, t.b)
		for _, o := range r.Outputs {
			if _, err := os.Stat(o); err == nil {
				if rl, err := filepath.Rel(runDir, o); err == nil && !slices.Contains(res.Outputs, rl) {
					res.Outputs = append(res.Outputs, filepath.ToSlash(rl))
				}
			}
		}
		// Later steps still run after a failure (papermill renders the partial notebook).
		if r.Reason != "" && failReason == "" {
			failReason, failMessage = r.Reason, r.Message
		}
	}
	if ctx.Err() != nil {
		// SIGTERM (timeout, stop, drain) killed the engine: record why before exiting.
		return finish(string(reasons.RunInterrupted), "The run was stopped before it finished")
	}
	if isNoSpace(fileLog.err) {
		return finish(string(reasons.OutputWriteFailed), "The home folder is full, so the run log could not be written")
	}
	return finish(failReason, strings.TrimSpace(failMessage))
}

// sourceCopyReason reports a full disk as OutputWriteFailed and any other
// copy failure as a source problem.
func sourceCopyReason(err error) string {
	if isNoSpace(err) {
		return string(reasons.OutputWriteFailed)
	}
	return string(reasons.SourceMissing)
}

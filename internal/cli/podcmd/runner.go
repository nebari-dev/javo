// Package podcmd holds the commands the platform's own pods run: the job
// runner, init container steps and the NetworkPolicy probe.
package podcmd

import (
	"cmp"
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/cli/exitcode"
	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/jobs/runner"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/workload"
)

// NewRunner is the entrypoint of a job pod's main container: it runs the
// spec in env JAVO_RUN. "runner install" copies this binary for the init container.
func NewRunner(engines *engine.Registry) *cobra.Command {
	var home, termLog string
	cmd := &cobra.Command{
		Use:           "runner",
		Short:         "Run one job run from the spec in JAVO_RUN",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var s runner.Spec
			raw := os.Getenv("JAVO_RUN")
			if raw == "" {
				return specFailed(termLog, "JAVO_RUN is not set")
			}
			if err := json.Unmarshal([]byte(raw), &s); err != nil {
				return specFailed(termLog, "JAVO_RUN is not valid JSON: "+err.Error())
			}
			runID := os.Getenv("JAVO_RUN_ID")
			if runID == "" {
				return specFailed(termLog, "JAVO_RUN_ID is not set")
			}
			// A timeout, stop or drain sends SIGTERM to PID 1; cancelling kills the
			// engine so Run can still write result.json and the termination message.
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
			defer stop()
			code := runner.Run(ctx, runner.Config{
				Home: home, RunID: runID, TerminationLog: termLog,
				Registry: engines, Exec: runner.WithPodUser(runner.PodUser(os.Getenv), runner.DefaultExec),
			}, s)
			if code != runner.ExitOK {
				return exitcode.Error{Code: code}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&home, "home", podspec.HomeDir, "the user's home directory")
	// Kubernetes reads the message from a fixed file; an executor with no
	// such file names one of the run's own in the variable.
	cmd.Flags().StringVar(&termLog, "termination-log", cmp.Or(os.Getenv(workload.EnvTerminationLog), "/dev/termination-log"),
		"file the executor reads as the termination message (default: $"+workload.EnvTerminationLog+", else the one Kubernetes reads)")

	var to string
	install := &cobra.Command{
		Use:   "install",
		Short: "Copy this binary to --to (the init container step; the image has no cp)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			self, err := os.Executable()
			if err != nil {
				return err
			}
			return installBinary(self, to)
		},
	}
	install.Flags().StringVar(&to, "to", "", "destination path")
	_ = install.MarkFlagRequired("to")
	cmd.AddCommand(install)
	return cmd
}

// specFailed records a termination message for a spec the runner cannot read,
// so the failure carries a catalog reason and a code podFailurePolicy maps.
func specFailed(termLog, message string) error {
	return exitcode.Error{Code: runner.Fail(termLog, reasons.TemplateInvalid, message)}
}

func installBinary(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return runner.CopyFile(src, dst, 0o755)
}

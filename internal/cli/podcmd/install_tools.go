//go:build linux || darwin

package podcmd

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/cli/exitcode"
)

// The environment install step (internal/pixienv) needs sha256sum and
// flock, which the pixi image has and macOS does not. Where apps run as
// processes of the machine, these two commands stand in for them, under
// those names, on that step's PATH (internal/executor/process). Each does
// only what the step's script asks of it.

// NewSha256sum prints the SHA-256 of each file named, or of what it is
// piped when none is, as sha256sum does: the hash, two spaces, the name
// ("-" for a pipe).
func NewSha256sum() *cobra.Command {
	return &cobra.Command{
		Use:    "sha256sum [FILE...]",
		Short:  "Print the SHA-256 of files, as sha256sum does (for the environment install step on a machine without it)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			sum := func(name string, r io.Reader) error {
				h := sha256.New()
				if _, err := io.Copy(h, r); err != nil {
					return err
				}
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "%x  %s\n", h.Sum(nil), name)
				return err
			}
			if len(args) == 0 {
				return sum("-", cmd.InOrStdin())
			}
			for _, name := range args {
				f, err := os.Open(name)
				if err != nil {
					return err
				}
				err = sum(name, f)
				_ = f.Close()
				if err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// NewFlock runs a command while holding an exclusive lock on a file,
// which it makes if missing, as flock FILE COMMAND... does: it waits for
// the lock, and ends with the command's exit code. The lock goes when
// this process does, however it ends.
func NewFlock() *cobra.Command {
	return &cobra.Command{
		Use:    "flock FILE COMMAND [ARG...]",
		Short:  "Run a command holding a lock on a file, as flock does (for the environment install step on a machine without it)",
		Hidden: true,
		// The command's own flags are not this one's.
		DisableFlagParsing: true,
		Args:               cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			lock, err := os.OpenFile(args[0], os.O_RDONLY|os.O_CREATE, 0o644)
			if err != nil {
				return err
			}
			defer lock.Close()
			for {
				err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
				if err == nil {
					break
				}
				if !errors.Is(err, syscall.EINTR) {
					return fmt.Errorf("lock %s: %w", args[0], err)
				}
			}
			c := exec.CommandContext(cmd.Context(), args[1], args[2:]...)
			c.Stdin, c.Stdout, c.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
			err = c.Run()
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return exitcode.Error{Code: exit.ExitCode()}
			}
			return err
		},
	}
}

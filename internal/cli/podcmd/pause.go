package podcmd

import (
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// NewPause is the program of a Docker workload's sandbox container
// (internal/executor/docker), which holds the workload's network and
// secret files and runs nothing else: it does nothing until it is told to
// end. The image has no sleep.
func NewPause() *cobra.Command {
	return &cobra.Command{
		Use:    "pause",
		Short:  "Do nothing until told to end (the sandbox container of a Docker workload)",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
			defer stop()
			<-ctx.Done()
			return nil
		},
	}
}

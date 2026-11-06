package podcmd

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/agenthelper"
)

// NewAgentHelper is the credential helper sidecar of a coding-agent app
// (internal/agenthelper): it holds the app's API token and model key and adds
// them to the agent's calls. With --check it only reports whether a helper
// answers on --socket (the sidecar's startup probe).
func NewAgentHelper() *cobra.Command {
	var sock, listen string
	var check bool
	cmd := &cobra.Command{
		Use:   "agent-helper",
		Short: "Serve a coding-agent app's API and model calls with its credentials (the agent app's sidecar)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if check {
				return agenthelper.Ready(sock)
			}
			cfg, err := agenthelper.ConfigFromEnv(os.Getenv)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return agenthelper.Run(ctx, cfg, sock, listen)
		},
	}
	cmd.Flags().StringVar(&sock, "socket", "", "unix socket to serve on")
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:7690", "local TCP address to serve on")
	cmd.Flags().BoolVar(&check, "check", false, "only check that a helper answers on --socket")
	_ = cmd.MarkFlagRequired("socket")
	return cmd
}

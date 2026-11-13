package podcmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/netprobe"
)

// NewNetprobe holds the two entrypoints of the isolation probe: the
// controller starts a listener and a dialer from this image, as pods on
// Kubernetes and as containers on Docker.
func NewNetprobe() *cobra.Command {
	root := &cobra.Command{Use: "netprobe", Short: "Entrypoints of the NetworkPolicy enforcement probe pods"}

	var addr string
	listen := &cobra.Command{
		Use:   "listen",
		Short: "Answer 200 to every request",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			srv := &http.Server{Addr: addr, Handler: netprobe.Listener(), ReadHeaderTimeout: 10 * time.Second}
			return srv.ListenAndServe()
		},
	}
	listen.Flags().StringVar(&addr, "addr", ":8080", "address to listen on")

	var control, target, messageFile string
	var tcp, names []string
	var timeout time.Duration
	dial := &cobra.Command{
		Use:   "dial",
		Short: "Request the control and target URLs and record which answered",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := netprobe.Dial(cmd.Context(), control, target, timeout)
			if len(tcp) > 0 {
				out.TCP = netprobe.Connect(cmd.Context(), tcp, timeout)
			}
			out.Resolved = netprobe.Resolve(cmd.Context(), names, timeout)
			msg, err := json.Marshal(out)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(msg))
			return os.WriteFile(messageFile, msg, 0o600)
		},
	}
	dial.Flags().StringVar(&control, "control", "", "URL that must answer (the gateway)")
	dial.Flags().StringVar(&target, "target", "", "URL the cluster must refuse (the listener)")
	dial.Flags().StringSliceVar(&tcp, "tcp", nil, "addresses (host:port) to connect to as well, recording how each answered (the Docker probe)")
	dial.Flags().StringSliceVar(&names, "resolve", nil, "names to look up as well, recording which resolved (the Docker probe)")
	dial.Flags().DurationVar(&timeout, "timeout", 3*time.Second, "wait per request")
	dial.Flags().StringVar(&messageFile, "message-file", "/dev/termination-log", "where the outcome is written for the controller to read")
	_ = dial.MarkFlagRequired("control")
	_ = dial.MarkFlagRequired("target")

	root.AddCommand(listen, dial)
	return root
}

package podcmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/cli/exitcode"
	"github.com/nebari-dev/javo/internal/source"
)

// NewSourceCheck is the init container that runs after git-sync: it checks
// the clone has the folder and file the app opens and hands the commit to the
// controller as its termination message. It needs no network, Kubernetes API
// or database. A failure prints its reason to stderr, which the kubelet turns
// into the termination message (FallbackToLogsOnError).
func NewSourceCheck() *cobra.Command {
	var link, path, file, termLog string
	cmd := &cobra.Command{
		Use:           "source-check",
		Short:         "Check the clone git-sync made and report its commit",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			commit, err := source.Check(link, path, file)
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
				return exitcode.Error{Code: 1}
			}
			if err := os.WriteFile(termLog, []byte(source.CommitMessage(commit)), 0o644); err != nil {
				return fmt.Errorf("write the termination message: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&link, "link", "", "git-sync's link to the checked out commit")
	cmd.Flags().StringVar(&path, "path", "", "folder inside the repository the app runs from")
	cmd.Flags().StringVar(&file, "file", "", "file inside that folder the app opens")
	cmd.Flags().StringVar(&termLog, "termination-log", "/dev/termination-log", "file Kubernetes reads as the termination message")
	_ = cmd.MarkFlagRequired("link")
	return cmd
}

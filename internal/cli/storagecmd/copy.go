package storagecmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/cli/exitcode"
	"github.com/nebari-dev/javo/internal/storage/homecopy"
)

// newCopyCmd is the home mode-change copy Job: it copies the old home to the
// new claim and checks the copy (internal/storage/homecopy). The outcome is
// the termination message the controller reads: empty when the copy
// verified, else a headline and the paths involved.
func newCopyCmd() *cobra.Command {
	var old, new, termLog string
	cmd := &cobra.Command{
		Use:           "copy",
		Short:         "Copy a home to a new volume and check the copy (used by the home mode-change Job)",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := homecopy.Run(old, new, termLog, os.Stdout); err != nil {
				cmd.PrintErrln(err)
				return exitcode.Error{Code: 1}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&old, "old", "/old", "the old home, mounted read-only")
	cmd.Flags().StringVar(&new, "new", "/new", "the new claim")
	cmd.Flags().StringVar(&termLog, "termination-log", "/dev/termination-log", "file Kubernetes reads as the termination message")
	return cmd
}

// newImportCopyCmd is the first Job of a JupyterHub home import: as root, it
// copies the JupyterHub home to a staging claim, giving every entry the
// person's uid and gid, and checks the copy (homecopy.RunAs).
func newImportCopyCmd() *cobra.Command {
	var old, new, termLog string
	var uid, gid int
	cmd := &cobra.Command{
		Use:           "import-copy",
		Short:         "Copy a home from another system, giving every file to one uid and gid (used by the JupyterHub import Job)",
		Args:          cobra.NoArgs,
		Hidden:        true,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := homecopy.RunAs(old, new, termLog, homecopy.Owner{UID: uid, GID: gid}, os.Stdout); err != nil {
				cmd.PrintErrln(err)
				return exitcode.Error{Code: 1}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&old, "old", "/old", "the home to copy, mounted read-only")
	cmd.Flags().StringVar(&new, "new", "/new", "the claim to copy to")
	cmd.Flags().IntVar(&uid, "uid", 0, "the uid every entry gets")
	cmd.Flags().IntVar(&gid, "gid", 0, "the gid every entry gets")
	cmd.Flags().StringVar(&termLog, "termination-log", "/dev/termination-log", "file Kubernetes reads as the termination message")
	_ = cmd.MarkFlagRequired("uid")
	_ = cmd.MarkFlagRequired("gid")
	return cmd
}

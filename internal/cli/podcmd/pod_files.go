package podcmd

import (
	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/podfiles"
)

// NewPodFiles is the init container that copies the platform's pod files
// (pod/files: pod-user.sh, the samples, the environments) out of this image
// into the pod's empty folder (internal/podfiles); the image has no cp.
func NewPodFiles() *cobra.Command {
	var from, to string
	cmd := &cobra.Command{
		Use:   "pod-files",
		Short: "Copy the platform's pod files to --to (the init container step; the image has no cp)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return podfiles.Copy(from, to)
		},
	}
	cmd.Flags().StringVar(&from, "from", podfiles.ImageDir, "folder of this image that holds the files")
	cmd.Flags().StringVar(&to, "to", "", "destination folder")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

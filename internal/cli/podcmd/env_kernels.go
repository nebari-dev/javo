package podcmd

import (
	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/pixienv"
)

// NewEnvKernels is run by the environment install step for an
// environment installed beside the template's (internal/pixienv): it offers
// that environment's Jupyter kernels to the app's server.
func NewEnvKernels() *cobra.Command {
	var prefix, activate, name, to, notice string
	cmd := &cobra.Command{
		Use:   "env-kernels",
		Short: "Offer the Jupyter kernels of an installed environment under its name (used by the environment install step)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return pixienv.RegisterKernels(prefix, activate, name, to, notice)
		},
	}
	cmd.Flags().StringVar(&prefix, "prefix", "", "the installed environment's prefix")
	cmd.Flags().StringVar(&activate, "activate", "", "the environment's activation script")
	cmd.Flags().StringVar(&name, "name", "", "the environment's name, shown for its kernels")
	cmd.Flags().StringVar(&to, "to", "", "the Jupyter data folder to write the kernels to")
	cmd.Flags().StringVar(&notice, "notice", "", "file that gets a plain-words note when the environment has no kernel")
	for _, f := range []string{"prefix", "activate", "name", "to"} {
		_ = cmd.MarkFlagRequired(f)
	}
	return cmd
}

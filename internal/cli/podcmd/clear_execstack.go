package podcmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/execstack"
)

// NewClearExecstack is run by the environment install step on each
// environment it installs (internal/pixienv): shared libraries that ask
// for an executable stack, which glibc 2.41 and later refuse to load, lose
// that request (internal/execstack).
func NewClearExecstack() *cobra.Command {
	return &cobra.Command{
		Use:   "clear-execstack DIR",
		Short: "Clear the executable-stack request of the shared libraries under DIR (used by the environment install step)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cleared, err := execstack.Clear(args[0])
			for _, p := range cleared {
				fmt.Fprintln(cmd.OutOrStdout(), "cleared the executable-stack request of", p)
			}
			return err
		},
	}
}

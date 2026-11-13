//go:build !local

package servercmd

import (
	"errors"

	"github.com/spf13/cobra"
)

// errNoLocal is what `javo local` says in a binary that was built without
// the tag local: it carries no UI, no templates and none of the
// platform's files, and links none of the code that serves them.
var errNoLocal = errors.New("this javo was built without javo local, which serves the web UI, the templates and the platform's files from inside the binary; build one that carries them with `make local`")

// NewLocal is `javo local` in a binary that cannot run it: the command
// is there, so that it is found, and says how to get one that can.
func NewLocal() *cobra.Command {
	return &cobra.Command{
		Use:                "local",
		Short:              localShort,
		DisableFlagParsing: true,
		RunE:               func(*cobra.Command, []string) error { return errNoLocal },
	}
}

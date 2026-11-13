// Package cli is the javo command: the root command and every subcommand,
// wired explicitly here.
package cli

import (
	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/cli/backupcmd"
	"github.com/nebari-dev/javo/internal/cli/dbcmd"
	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/cli/homecmd"
	"github.com/nebari-dev/javo/internal/cli/kubeclient"
	"github.com/nebari-dev/javo/internal/cli/openapicmd"
	"github.com/nebari-dev/javo/internal/cli/podcmd"
	"github.com/nebari-dev/javo/internal/cli/secretscmd"
	"github.com/nebari-dev/javo/internal/cli/servercmd"
	"github.com/nebari-dev/javo/internal/cli/storagecmd"
	"github.com/nebari-dev/javo/internal/cli/usercmd"
	"github.com/nebari-dev/javo/internal/cli/wiring"
)

// New is the javo command. newClient builds the Kubernetes client of the
// admin commands that need one (nil: kubeclient.Default); openStore opens
// their database (nil: the database their flags name).
func New(newClient kubeclient.Factory, openStore dbflag.Opener) *cobra.Command {
	root := &cobra.Command{Use: "javo", SilenceUsage: true}
	root.AddCommand(usercmd.New(newClient, openStore))
	root.AddCommand(homecmd.New(newClient, openStore))
	root.AddCommand(secretscmd.New(openStore))
	root.AddCommand(dbcmd.New(openStore))
	root.AddCommand(storagecmd.New(openStore))
	root.AddCommand(backupcmd.New(openStore))
	root.AddCommand(servercmd.NewServer())
	root.AddCommand(servercmd.NewController())
	root.AddCommand(servercmd.NewGateway())
	root.AddCommand(openapicmd.New())
	root.AddCommand(podcmd.NewNetprobe())
	root.AddCommand(podcmd.NewRunner(wiring.Engines()))
	root.AddCommand(podcmd.NewSourceCheck())
	root.AddCommand(podcmd.NewPodFiles())
	root.AddCommand(podcmd.NewClearExecstack())
	root.AddCommand(podcmd.NewEnvKernels())
	root.AddCommand(podcmd.NewAgentHelper())
	root.AddCommand(podcmd.NewSha256sum())
	root.AddCommand(podcmd.NewFlock())
	root.AddCommand(podcmd.NewPause())
	return root
}

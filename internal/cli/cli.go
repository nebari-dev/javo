// Package cli is the javo command: the root command and every subcommand,
// wired explicitly here.
package cli

import (
	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/cli/backupcmd"
	"github.com/nebari-dev/javo/internal/cli/dbcmd"
	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/cli/homeclaims"
	"github.com/nebari-dev/javo/internal/cli/homecmd"
	"github.com/nebari-dev/javo/internal/cli/openapicmd"
	"github.com/nebari-dev/javo/internal/cli/podcmd"
	"github.com/nebari-dev/javo/internal/cli/secretscmd"
	"github.com/nebari-dev/javo/internal/cli/servercmd"
	"github.com/nebari-dev/javo/internal/cli/storagecmd"
	"github.com/nebari-dev/javo/internal/cli/usercmd"
	"github.com/nebari-dev/javo/internal/cli/wiring"
)

// New is the javo command. newClaims reaches the claims of people's
// homes on the cluster, for the admin commands that change them (nil: the
// cluster the kubeconfig names); openStore opens their database (nil: the
// database their flags name).
func New(newClaims homeclaims.Factory, openStore dbflag.Opener) *cobra.Command {
	if newClaims == nil {
		newClaims = clusterClaims
	}
	root := &cobra.Command{Use: "javo", SilenceUsage: true}
	root.AddCommand(usercmd.New(newClaims, openStore))
	root.AddCommand(homecmd.New(newClaims, openStore))
	root.AddCommand(secretscmd.New(openStore, kekFromSecret))
	root.AddCommand(dbcmd.New(openStore))
	root.AddCommand(storagecmd.New(openStore))
	root.AddCommand(backupcmd.New(openStore))
	root.AddCommand(servercmd.NewServer())
	root.AddCommand(servercmd.NewLocal())
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

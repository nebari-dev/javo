// Package openapicmd is `javo openapi`: print the gateway's OpenAPI document.
package openapicmd

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/cli/wiring"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/store"
)

// docConfig is the gateway configuration the OpenAPI document describes: no
// cluster, every provider that has routes of its own switched on.
func docConfig() gateway.Config {
	var p config.Platform
	p.Auth.Local.Enabled = true
	p.URLs.Mode = "path"
	// The store and clients are placeholders: nothing reads them while only
	// the document is built.
	return gateway.Config{
		Providers: wiring.AuthProviders(p, &local.DBUsers{}, nil), Exposure: wiring.Exposure(p),
		Engines: wiring.Engines(), NetworkPolicy: &netprobe.Store{},
		Tokens: &token.DBStore{}, Store: &store.Store{}, Machines: capacity.Nodes{}, Diagnose: wiring.DiagnoseEngine(),
		// Optional routes the UI calls when the install turns them on.
		Thumbnails: true,
	}
}

// New prints the gateway's OpenAPI document. It builds the same
// server the gateway runs, with no cluster, so there is one API definition.
func New() *cobra.Command {
	return &cobra.Command{
		Use:   "openapi",
		Short: "Print the gateway's OpenAPI 3.1 document",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := json.MarshalIndent(gateway.NewServer(docConfig()).API.OpenAPI(), "", "  ")
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(append(doc, '\n'))
			return err
		},
	}
}

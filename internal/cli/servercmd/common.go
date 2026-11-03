// Package servercmd holds the long-running commands: javo server, and the
// gateway and the controller it runs, alone or together.
package servercmd

import (
	"fmt"
	"os"

	"github.com/spf13/pflag"

	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/cli/wiring"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
)

// commonFlags are the settings the gateway and the controller share, so
// `javo server` registers each once.
type commonFlags struct {
	workspaceNamespace string
	namespace          string
	sizesFile          string
	// fingerprintKey is the key file of the secrets' fingerprints.
	fingerprintKey string
	loadConfig     wiring.ConfigLoader
	database       dbflag.Flags
}

func (c *commonFlags) register(f *pflag.FlagSet) {
	c.loadConfig = wiring.ConfigFlag(f)
	c.database.Register(f)
	f.StringVar(&c.workspaceNamespace, "workspace-namespace", model.DefaultWorkspaceNamespace, "namespace holding apps and their pods")
	f.StringVar(&c.namespace, "namespace", os.Getenv("POD_NAMESPACE"), "the release namespace, where API token records are kept (default $POD_NAMESPACE)")
	f.StringVar(&c.sizesFile, "sizes-file", "", sizesFileUsage)
	f.StringVar(&c.fingerprintKey, "secret-fingerprint-key-file", "", "file holding the 32-byte key of the fingerprints the controller makes of people's secret values and the gateway looks for in app logs before they go to a model (a mounted Secret); empty: those log lines are not sent")
}

// fingerprinter reads --secret-fingerprint-key-file; nil without one.
func (c *commonFlags) fingerprinter() (*secrets.Fingerprinter, error) {
	if c.fingerprintKey == "" {
		return nil, nil
	}
	f, err := secrets.FingerprinterFromFile(c.fingerprintKey)
	if err != nil {
		return nil, fmt.Errorf("--secret-fingerprint-key-file: %w", err)
	}
	return f, nil
}

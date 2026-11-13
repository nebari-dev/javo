// Package servercmd holds the long-running commands: javo server, and the
// gateway and the controller it runs, alone or together.
package servercmd

import (
	"context"
	"os"

	"github.com/spf13/pflag"

	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/cli/wiring"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// commonFlags are the settings the gateway and the controller share, so
// `javo server` registers each once.
type commonFlags struct {
	workspaceNamespace string
	namespace          string
	sizesFile          string
	// publicHost is the host browsers reach the gateway at, which templates
	// read as .Host: the controller puts it in pods, the gateway shows it.
	publicHost string
	// fingerprintKeySecret names the Secret in the release namespace with
	// the key of the secrets' fingerprints; fingerprintKeyCreate makes it.
	fingerprintKeySecret string
	fingerprintKeyCreate bool
	// backupPasswordSecret names the Secret in the release namespace with
	// the bucket's backup password; backupPasswordCreate makes it.
	backupPasswordSecret string
	backupPasswordCreate bool
	loadConfig           wiring.ConfigLoader
	database             dbflag.Flags
}

func (c *commonFlags) register(f *pflag.FlagSet) {
	c.loadConfig = wiring.ConfigFlag(f)
	c.database.Register(f)
	f.StringVar(&c.workspaceNamespace, "workspace-namespace", model.DefaultWorkspaceNamespace, "namespace holding apps and their pods")
	f.StringVar(&c.namespace, "namespace", os.Getenv("POD_NAMESPACE"), "the release namespace, where API token records are kept (default $POD_NAMESPACE)")
	f.StringVar(&c.sizesFile, "sizes-file", "", sizesFileUsage)
	f.StringVar(&c.publicHost, "public-host", "", "host (and port, if not the default) browsers reach the gateway at; templates read it as .Host, e.g. for a websocket origin check")
	f.StringVar(&c.fingerprintKeySecret, "secret-fingerprint-key-secret", "", "Secret in the release namespace whose key \"key\" holds the 32-byte key of the fingerprints the controller makes of people's secret values and the gateway looks for in app logs before they go to a model, read at start; empty: those log lines are not sent")
	f.BoolVar(&c.fingerprintKeyCreate, "secret-fingerprint-key-create", false, "make the --secret-fingerprint-key-secret Secret, immutable with a new random key, when it does not exist; never changed afterwards")
	f.StringVar(&c.backupPasswordSecret, "backup-password-secret", "", "Secret in the release namespace whose key \"password\" holds the password every backup in the bucket is encrypted with, read at start instead of backup.s3.passwordFile; empty: the file is read")
	f.BoolVar(&c.backupPasswordCreate, "backup-password-create", false, "make the --backup-password-secret Secret, immutable with a new random password, when it does not exist; never changed afterwards")
}

// load reads the platform config and, when the bucket's password is kept in
// a Secret, the password.
func (c *commonFlags) load(ctx context.Context) (config.Platform, error) {
	platform, err := c.loadConfig()
	if err != nil || c.backupPasswordSecret == "" || platform.Backup.S3 == nil {
		return platform, err
	}
	return platform, c.readBackupPassword(ctx, &platform)
}

// openDB opens the database the flags name, with the per-person caps of
// platform's limits section.
func (c *commonFlags) openDB(ctx context.Context, platform config.Platform) (*store.Store, error) {
	db, err := c.database.Open(ctx)
	if err != nil {
		return nil, err
	}
	db.SetLimits(wiring.StoreLimits(platform.Limits))
	return db, nil
}

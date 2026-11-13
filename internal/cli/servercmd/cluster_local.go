//go:build local

package servercmd

import (
	"context"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/placement"
	"github.com/nebari-dev/javo/internal/store"
)

// What runs Javo on a cluster, in a binary built with the tag local:
// nothing. These stand in for what the *_cluster.go files hold in every
// other build, and each answers that it is left out. The files that hold
// the real ones are not compiled here, so nothing in this binary can
// reach a cluster, whatever is added to them.

// runOnCluster is refused.
func (o *controllerOptions) runOnCluster(context.Context, *commonFlags, config.Platform, *store.Store) error {
	return errNoCluster
}

// clusterParts is refused.
func (o *gatewayOptions) clusterParts(context.Context, *commonFlags, config.Platform, *store.Store, placement.Placement) (gatewayParts, error) {
	return gatewayParts{}, errNoCluster
}

// readBackupPassword is refused: the bucket's password is a Secret of a
// cluster only there (--backup-password-secret).
func (c *commonFlags) readBackupPassword(context.Context, *config.Platform) error {
	return errNoCluster
}

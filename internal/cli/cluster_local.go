//go:build local

package cli

import (
	"context"

	"github.com/nebari-dev/javo/internal/buildinfo"
	"github.com/nebari-dev/javo/internal/cli/homeclaims"
	"github.com/nebari-dev/javo/internal/secrets"
)

// errNoCluster is what an admin command says when it is asked to reach a
// cluster: a binary built with the tag local carries no client of one.
var errNoCluster = buildinfo.LeftOut("the Kubernetes client")

// clusterClaims is refused: a home of `javo local` is a folder, never a
// claim.
func clusterClaims() (homeclaims.Claims, error) { return nil, errNoCluster }

// kekFromSecret is refused: the key of `javo local` is a file.
func kekFromSecret(context.Context, string, string) (secrets.KEK, error) { return nil, errNoCluster }

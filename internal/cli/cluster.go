//go:build !local

package cli

import "github.com/nebari-dev/javo/internal/cli/kubeclient"

// What the admin commands do on a cluster: the claims of people's homes
// and the secrets key kept in a Secret, through the Kubernetes client of
// the kubeconfig. A binary built with the tag local has neither.
var (
	clusterClaims = kubeclient.DefaultClaims
	kekFromSecret = kubeclient.KEKFromSecret
)

//go:build local

package servercmd

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/nebari-dev/javo/internal/store"
)

// addNotifications is the failure notices of a binary built with the tag
// local: none. They are sent from a cluster's controller, which that
// binary refuses to run (errNoCluster) before it gets here, and the
// services they are sent through would add about 4 MB to it.
func addNotifications(ctrl.Manager, *store.Store, notificationFlags) error { return errNoCluster }

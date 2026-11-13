package storage

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/controller/storage/rows"
	"github.com/nebari-dev/javo/internal/model"
)

// SingletonName names the request of the reconcilers that keep the cluster's
// storage as a whole (findings, probe, quotas).
const SingletonName = "cluster"

// singleton is the one request those reconcilers take.
var singleton = []reconcile.Request{{NamespacedName: types.NamespacedName{Name: SingletonName}}}

// singletonID is that request for a source of row ids.
var singletonID = []string{SingletonName}

// toSingleton maps any object to the singleton request.
var toSingleton = handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request { return singleton })

// Rows is what every storage reconciler reads (rows.Rows).
type Rows = rows.Rows

// blocked is why an object is not Ready (rows.Blocked).
type blocked = rows.Blocked

// What the reconcilers of a machine without a cluster wait as well.
const (
	retryConflict    = rows.RetryConflict
	homeWaitRetry    = rows.HomeWaitRetry
	homeClaimRetry   = rows.HomeClaimRetry
	homePrepareRetry = rows.HomePrepareRetry
)

// errRowChanged is a write in the middle of a pass that lost the race to
// another writer; the pass is run again.
var errRowChanged = rows.ErrRowChanged

// notReadyHome keeps homes whose Ready condition is not True: a change of the
// cluster's storage (a raised limit, a new class) may unblock them.
func notReadyHome(h *model.Home) bool {
	return !meta.IsStatusConditionTrue(h.Status.Conditions, model.ConditionReady)
}

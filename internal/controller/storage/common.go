package storage

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
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

// Rows is what every storage reconciler reads: the database and the
// platform config's storage section (defaults applied).
type Rows struct {
	// Store is the database. Required.
	Store *store.Store
	// Config is the platform config's storage section, defaults applied.
	Config model.ClusterStorageSpec
}

// cluster is the storage view: config, the admin's requests and findings.
func (r Rows) cluster(ctx context.Context) (*model.ClusterStorage, error) {
	cs, err := r.Store.Storage().Cluster(ctx, r.Config)
	return &cs, err
}

// homeRequests is a request per home that keep accepts (all when nil).
func (r Rows) homeRequests(ctx context.Context, keep func(*model.Home) bool) ([]string, error) {
	homes, err := r.Store.Homes().List(ctx)
	return dbsource.IDs(homes, func(h *model.Home) string { return h.ID }, keep), err
}

// notReadyHome keeps homes whose Ready condition is not True: a change of the
// cluster's storage (a raised limit, a new class) may unblock them.
func notReadyHome(h *model.Home) bool {
	return !meta.IsStatusConditionTrue(h.Status.Conditions, model.ConditionReady)
}

// writeHome stores h if it differs from base (the home as read) and returns
// whether another writer got there first; the caller then reconciles again.
func (r Rows) writeHome(ctx context.Context, h, base *model.Home) (conflict bool, err error) {
	if homeEqual(h, base) {
		return false, nil
	}
	got, err := r.Store.Homes().Update(ctx, *h, base.Version)
	if errors.Is(err, store.ErrConflict) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	*h, *base = got, *got.DeepCopy()
	return false, nil
}

// folderRequests is a request per folder that keep accepts (all when nil).
func (r Rows) folderRequests(ctx context.Context, keep func(*model.SharedFolder) bool) ([]string, error) {
	folders, err := r.Store.SharedFolders().List(ctx)
	return dbsource.IDs(folders, func(f *model.SharedFolder) string { return f.ID }, keep), err
}

// writeFolder stores f if it differs from base (the folder as read); another
// writer getting there first is errRowChanged.
func (r Rows) writeFolder(ctx context.Context, f, base *model.SharedFolder) error {
	if equality.Semantic.DeepEqual(base.Status, f.Status) {
		return nil
	}
	got, err := r.Store.SharedFolders().Update(ctx, *f, base.Version)
	if errors.Is(err, store.ErrConflict) {
		return errRowChanged
	} else if err != nil {
		return err
	}
	*f, *base = got, *got.DeepCopy()
	return nil
}

// retryConflict is how soon a pass that lost a write race runs again.
const retryConflict = 200 * time.Millisecond

// homeEqual is true when a and b hold the same spec and status.
func homeEqual(a, b *model.Home) bool {
	return equality.Semantic.DeepEqual(a.Spec, b.Spec) && equality.Semantic.DeepEqual(a.Status, b.Status)
}

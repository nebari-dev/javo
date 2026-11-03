package storage

import (
	"context"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
)

// The storage reconcilers are driven by the database: a home row written by
// the app reconciler gets its claim, and findings written later reach the
// homes that waited for them, with no cluster object to watch.
func TestTheHomeReconcilerRunsOnTheChangeFeed(t *testing.T) {
	clean(t)
	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme: k8s.Scheme(), Metrics: metricsserver.Options{BindAddress: "0"},
		Controller: ctrlconfig.Controller{SkipNameValidation: ptr.To(true)},
	})
	must(t, err)
	feed := dbsource.NewFeed(db.Changes())
	r := homeReconciler(k8s)
	must(t, r.SetupWithManager(mgr, feed))
	runCtx, cancel := context.WithCancel(ctx)
	must(t, db.StartFeed(runCtx))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); feed.Run(runCtx) }()
	go func() { defer wg.Done(); _ = mgr.Start(runCtx) }()
	t.Cleanup(func() { cancel(); wg.Wait() })

	owner := uniqueOwner("feed")
	newHome(t, owner, "1Gi")
	waitFor(t, "the home to wait for the findings", func() bool {
		h := getHome(t, owner)
		return len(h.Status.Conditions) > 0 && h.Status.ClaimName == ""
	})
	// The cluster reconciler's write is news for the waiting home.
	setStatus(t, model.ClusterStorageStatus{
		Nodes:   3,
		Classes: []model.ClassCapability{{Name: "fast", RWXFile: fact(false), PosixOwnership: fact(true)}},
		Choice:  &model.StorageChoice{Mode: model.HomeModePerUser, HomeClass: "fast"},
	})
	waitFor(t, "the claim", func() bool {
		c := &corev1.PersistentVolumeClaim{}
		return k8s.Get(ctx, types.NamespacedName{Namespace: homeNS, Name: naming.HomeClaimName(owner)}, c) == nil
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

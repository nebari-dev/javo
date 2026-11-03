package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/ext/storage"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
)

// testGroups is the group membership the tests use: owner to groups.
type testGroups map[string][]string

func (g testGroups) Groups(_ context.Context, owner string) ([]string, error) { return g[owner], nil }

// testStorage is the storage view the tests' reconcilers read from s: homes
// ask for 1Gi, and gina is in group research.
func testStorage(s *store.Store) podstorage.Resolver {
	var spec model.ClusterStorageSpec
	spec.ApplyDefaults()
	size := resource.MustParse("1Gi")
	spec.Home.Size = &size
	return podstorage.Resolver{
		Homes: s.Homes(), Folders: s.SharedFolders(),
		Cluster: store.StorageView{Storage: s.Storage(), Config: spec},
		Groups:  testGroups{"gina": {"research"}},
	}
}

// stuckPrefix marks owners whose homes the stand-in below leaves alone, so a
// test can set the home's state itself.
const stuckPrefix = "stuck-"

// fakeHomes stands in for the storage controller's home reconciler (which
// has its own tests): it makes the claim and marks the home prepared, the
// state the app reconciler waits for.
type fakeHomes struct {
	client.Client
	Store *store.Store
}

func (f fakeHomes) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	h, err := f.Store.Homes().Get(ctx, req.Name)
	if errors.Is(err, store.ErrNotFound) || err == nil && (strings.HasPrefix(h.Spec.Owner, stuckPrefix) || h.Status.Prepared) {
		return reconcile.Result{}, nil
	}
	if err != nil {
		return reconcile.Result{}, err
	}
	err = prepareHome(ctx, f.Client, f.Store, h)
	if errors.Is(err, store.ErrConflict) {
		return reconcile.Result{Requeue: true}, nil
	}
	return reconcile.Result{}, err
}

// setupFakeHomes runs the stand-in on mgr, fed by the homes table.
func setupFakeHomes(mgr ctrl.Manager, s *store.Store, feed *dbsource.Feed) error {
	all := func(ctx context.Context) ([]reconcile.Request, error) {
		homes, err := s.Homes().List(ctx)
		return dbsource.Requests(homes, func(h *model.Home) string { return h.ID }, nil), err
	}
	return ctrl.NewControllerManagedBy(mgr).Named("fake-homes").
		WatchesRawSource(dbsource.For(feed.Subscribe(), store.TableHomes, 0, all)).
		Complete(fakeHomes{Client: mgr.GetClient(), Store: s})
}

// prepareHome gives h a claim and a prepared, ready status.
func prepareHome(ctx context.Context, c client.Client, s *store.Store, h model.Home) error {
	claim := storage.HomeClaim(&h, "fast", resource.MustParse("1Gi"), corev1.ReadWriteOnce)
	if err := c.Create(ctx, claim); client.IgnoreAlreadyExists(err) != nil {
		return err
	}
	h.Status = model.HomeStatus{
		Mode: model.HomeModePerUser, ClaimName: claim.Name, StorageClassName: "fast", Prepared: true,
		Conditions: []metav1.Condition{{Type: model.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Now()}},
	}
	_, err := s.Homes().Update(ctx, h, h.Version)
	return err
}

// primeHome makes owner's prepared home and claim in ns, for tests that call
// a Reconciler directly (no stand-in prepares homes there).
func primeHome(t *testing.T, s *store.Store, owner, ns string) {
	t.Helper()
	for range 5 {
		h, err := s.Homes().Ensure(bg, ns, owner, nil)
		if err != nil {
			t.Fatal(err)
		}
		if h.Status.Prepared {
			return
		}
		// A stand-in serving ns may prepare it at the same time.
		if err := prepareHome(bg, k8s, s, h); !errors.Is(err, store.ErrConflict) {
			if err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("home kept changing under the test")
}

// setHome changes owner's home in the test namespace through the store.
func setHome(t *testing.T, owner string, fn func(*model.Home)) {
	t.Helper()
	for range 5 {
		h, err := db.Homes().ByOwner(bg, testNS, owner)
		if err != nil {
			t.Fatal(err)
		}
		fn(&h)
		if _, err = db.Homes().Update(bg, h, h.Version); !errors.Is(err, store.ErrConflict) {
			if err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("home kept changing under the test")
}

// stuckHome makes a home the stand-in leaves alone, with Ready as given.
func stuckHome(t *testing.T, owner string, ready metav1.Condition) {
	t.Helper()
	if _, err := db.Homes().Ensure(bg, testNS, owner, nil); err != nil {
		t.Fatal(err)
	}
	ready.Type, ready.LastTransitionTime = model.ConditionReady, metav1.Now()
	setHome(t, owner, func(h *model.Home) { h.Status.Conditions = []metav1.Condition{ready} })
}

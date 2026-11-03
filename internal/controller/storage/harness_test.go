package storage

import (
	"context"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var (
	k8s        client.Client
	restConfig *rest.Config
	ctx        = context.Background()
	// db is the database the tests' reconcilers read; cfg is the platform
	// config's storage section they are given. clean gives a test fresh ones.
	db  *store.Store
	cfg model.ClusterStorageSpec
)

func TestMain(m *testing.M) {
	env := &envtest.Environment{}
	var err error
	restConfig, err = env.Start()
	if err != nil {
		panic(err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	k8s, err = client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}
	dir, err := os.MkdirTemp("", "javo-storage-test")
	if err != nil {
		panic(err)
	}
	scfg, cleanup, err := storetest.FreshConfig(ctx, storetest.Engine(), os.Getenv("JAVO_TEST_PG_DSN"), dir)
	if err != nil {
		panic(err)
	}
	if db, err = store.Open(ctx, scfg); err != nil {
		panic(err)
	}
	if err := db.Migrate(ctx); err != nil {
		panic(err)
	}
	cfg = defaultSpec()
	code := m.Run()
	_ = db.Close()
	cleanup()
	_ = os.RemoveAll(dir)
	_ = env.Stop()
	os.Exit(code)
}

// defaultSpec is the storage config with every default, as config.Load gives.
func defaultSpec() model.ClusterStorageSpec {
	var s model.ClusterStorageSpec
	s.ApplyDefaults()
	return s
}

// clean gives the test a fresh database and the default config, and removes
// the cluster objects a test may have made, so tests do not see each other's
// storage classes, nodes or homes.
func clean(t *testing.T) {
	t.Helper()
	wipe := func() {
		must(t, k8s.DeleteAllOf(ctx, &storagev1.StorageClass{}))
		must(t, k8s.DeleteAllOf(ctx, &corev1.Node{}))
	}
	wipe()
	t.Cleanup(wipe)
	db, cfg = storetest.New(t), defaultSpec()
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// testRows is what a reconciler under test reads: db and cfg as they are now.
func testRows() Rows { return Rows{Store: db, Config: cfg} }

// setStatus stores the findings as the cluster and probe reconcilers would.
func setStatus(t *testing.T, st model.ClusterStorageStatus) {
	t.Helper()
	must(t, db.Storage().SetClusterStatus(ctx, st))
	must(t, db.Storage().SetProbeStatus(ctx, st.Probe, st.ProbeResults))
}

// setRequests changes the admin's run-time requests.
func setRequests(t *testing.T, fn func(*model.StorageRequests)) {
	t.Helper()
	must(t, db.Storage().UpdateRequests(ctx, model.Actor{Person: "admin"}, "storage.test", func(r *model.StorageRequests) error {
		fn(r)
		return nil
	}))
}

// storageNow is the storage view the reconcilers see.
func storageNow(t *testing.T) *model.ClusterStorage {
	t.Helper()
	cs, err := testRows().cluster(ctx)
	must(t, err)
	return cs
}

// homeOf reads owner's home in namespace ns.
func homeOf(t *testing.T, ns, owner string) *model.Home {
	t.Helper()
	h, err := db.Homes().ByOwner(ctx, ns, owner)
	must(t, err)
	return &h
}

// editHome changes owner's home in ns through the store.
func editHome(t *testing.T, ns, owner string, fn func(*model.Home)) {
	t.Helper()
	h := homeOf(t, ns, owner)
	fn(h)
	if _, err := db.Homes().Update(ctx, *h, h.Version); err != nil {
		t.Fatal(err)
	}
}

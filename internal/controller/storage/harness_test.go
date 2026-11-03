package storage

import (
	"context"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

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
	scheme = runtime.NewScheme()
	ctx    = context.Background()
	// Each test has an envtest API server to itself for its whole run, so
	// the cluster-wide objects it makes (nodes, storage classes) are seen by
	// no other test running beside it. idle holds the servers no test has;
	// a test takes a token from starts to start another when none is idle.
	idle   = make(chan *apiServer, maxServers)
	starts = make(chan struct{}, maxServers)
	// envs is each running test's state by test name; a subtest without
	// its own uses its parent's.
	envs sync.Map
)

// maxServers caps the API servers running at once, and so the tests.
var maxServers = max(1, min(6, goruntime.GOMAXPROCS(0)/2))

// apiServer is one envtest control plane and a client of it.
type apiServer struct {
	env    *envtest.Environment
	k8s    client.Client
	config *rest.Config
}

// takeAPIServer gives t an idle API server, else starts one when fewer than
// maxServers run, else waits for one to be given back.
func takeAPIServer(t *testing.T) *apiServer {
	t.Helper()
	select {
	case s := <-idle:
		return s
	default:
	}
	select {
	case s := <-idle:
		return s
	case starts <- struct{}{}:
		s, err := startAPIServer()
		if err != nil {
			<-starts
			t.Fatal(err)
		}
		return s
	}
}

func startAPIServer() (*apiServer, error) {
	env := &envtest.Environment{}
	// Without the watch cache a list right after a write is answered by
	// etcd at once, instead of waiting for the cache to catch up (about
	// 100ms a list).
	env.ControlPlane.GetAPIServer().Configure().Set("watch-cache", "false")
	config, err := env.Start()
	if err != nil {
		return nil, err
	}
	k8s, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		_ = env.Stop()
		return nil, err
	}
	return &apiServer{env: env, k8s: k8s, config: config}, nil
}

// testEnv is what one test works with: an API server it has to itself, its
// own database, the platform config's storage section its reconcilers are
// given, and the clocks of the reconcilers that take one.
type testEnv struct {
	name        string // the test's
	k8s         client.Client
	restConfig  *rest.Config
	db          *store.Store
	cfg         model.ClusterStorageSpec
	clock       time.Time // the migration reconciler's
	backupClock time.Time // the backup reconciler's: 10:00 UTC, backups at 01:00
}

func TestMain(m *testing.M) {
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	code := m.Run()
	var wg sync.WaitGroup
	for range len(starts) {
		wg.Go(func() { _ = (<-idle).env.Stop() }) // about 1s each, so all at once
	}
	wg.Wait()
	os.Exit(code)
}

// env is the test's state: its own, else its nearest parent's, else a new
// one made with clean.
func env(t *testing.T) *testEnv {
	t.Helper()
	if e := envNamed(t.Name()); e != nil {
		return e
	}
	return clean(t)
}

func envNamed(name string) *testEnv {
	for {
		if v, ok := envs.Load(name); ok {
			return v.(*testEnv)
		}
		i := strings.LastIndex(name, "/")
		if i < 0 {
			return nil
		}
		name = name[:i]
	}
}

// defaultSpec is the storage config with every default, as config.Load gives.
func defaultSpec() model.ClusterStorageSpec {
	var s model.ClusterStorageSpec
	s.ApplyDefaults()
	return s
}

// clean gives the test a fresh database and the default config, and removes
// the cluster objects a test may have made, so tests do not see each other's
// storage classes, nodes or homes. A subtest keeps its parent's API server
// and clocks; a top-level test takes an API server until it ends.
func clean(t *testing.T) *testEnv {
	t.Helper()
	e := envNamed(t.Name())
	if e == nil || e.name != t.Name() {
		e = newEnv(t, e)
	}
	wipe := func() {
		must(t, e.k8s.DeleteAllOf(ctx, &storagev1.StorageClass{}))
		must(t, e.k8s.DeleteAllOf(ctx, &corev1.Node{}))
	}
	wipe()
	t.Cleanup(wipe)
	e.db, e.cfg = storetest.New(t), defaultSpec()
	return e
}

// newEnv registers a state for t, sharing parent's API server and clocks
// when there is a parent, else taking an API server of its own.
func newEnv(t *testing.T, parent *testEnv) *testEnv {
	t.Helper()
	e := &testEnv{
		name:        t.Name(),
		clock:       time.Now().Truncate(time.Second),
		backupClock: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC),
	}
	if parent != nil {
		e.k8s, e.restConfig, e.clock, e.backupClock = parent.k8s, parent.restConfig, parent.clock, parent.backupClock
	} else {
		s := takeAPIServer(t)
		t.Cleanup(func() { idle <- s })
		e.k8s, e.restConfig = s.k8s, s.config
	}
	envs.Store(e.name, e)
	t.Cleanup(func() { envs.Delete(e.name) })
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// testRows is what a reconciler under test reads: the test's db and cfg as
// they are now.
func testRows(t *testing.T) Rows { e := env(t); return Rows{Store: e.db, Config: e.cfg} }

// setStatus stores the findings as the cluster and probe reconcilers would.
func setStatus(t *testing.T, st model.ClusterStorageStatus) {
	t.Helper()
	must(t, env(t).db.Storage().SetClusterStatus(ctx, st))
	must(t, env(t).db.Storage().SetProbeStatus(ctx, st.Probe, st.ProbeResults))
}

// setRequests changes the admin's run-time requests.
func setRequests(t *testing.T, fn func(*model.StorageRequests)) {
	t.Helper()
	must(t, env(t).db.Storage().UpdateRequests(ctx, model.Actor{Person: "admin"}, "storage.test", func(r *model.StorageRequests) error {
		fn(r)
		return nil
	}))
}

// storageNow is the storage view the reconcilers see.
func storageNow(t *testing.T) *model.ClusterStorage {
	t.Helper()
	cs, err := testRows(t).cluster(ctx)
	must(t, err)
	return cs
}

// homeOf reads owner's home in namespace ns.
func homeOf(t *testing.T, ns, owner string) *model.Home {
	t.Helper()
	h, err := env(t).db.Homes().ByOwner(ctx, ns, owner)
	must(t, err)
	return &h
}

// editHome changes owner's home in ns through the store.
func editHome(t *testing.T, ns, owner string, fn func(*model.Home)) {
	t.Helper()
	h := homeOf(t, ns, owner)
	fn(h)
	if _, err := env(t).db.Homes().Update(ctx, *h, h.Version); err != nil {
		t.Fatal(err)
	}
}

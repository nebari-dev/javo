package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/ctrltest"
	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/executor/kube"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/features/exposurepath"
	"github.com/nebari-dev/javo/internal/features/exposuresubdomain"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var testSizes = func() sizes.Catalog {
	c, err := sizes.Parse([]byte(`{default: small, sizes: [
		{name: small, displayName: Small, cpu: "1", memory: 2Gi},
		{name: medium, displayName: Medium, cpu: "4", memory: 8Gi}]}`))
	if err != nil {
		panic(err)
	}
	return c
}()

var (
	bg     = context.Background()
	actor  = model.Actor{Person: "alice"}
	k8s    client.Client
	scheme = runtime.NewScheme()
	cfg    *rest.Config
	// nsCount numbers the namespaces newNamespace makes, so no two tests
	// (nor two runs of one test under -count) share one.
	nsCount atomic.Int32
)

// newNamespace makes a namespace no other test uses and returns its name.
func newNamespace(t *testing.T, prefix string, labels map[string]string) string {
	t.Helper()
	name := fmt.Sprintf("%s-%d", prefix, nsCount.Add(1))
	if err := k8s.Create(bg, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}); err != nil {
		t.Fatal(err)
	}
	return name
}

// env is one test's own world: a database, a namespace no other test uses,
// and a manager whose app reconciler serves that namespace from that
// database, so tests run in parallel without seeing each other's apps.
type env struct {
	db  *store.Store
	ns  string
	rec *Reconciler
	// cache is the manager's client: what rec sees of the cluster.
	cache  client.Client
	policy *stubPolicy
	keys   *secrets.Keys
	// stop stops the manager and waits for it; calling it again is a no-op.
	stop func()
}

func newEnv(t *testing.T, tune ...func(*Reconciler)) *env {
	t.Helper()
	fx := &env{db: storetest.New(t), ns: newNamespace(t, "javo-apps", nil), policy: &stubPolicy{state: netprobe.Enforced}}
	keys, err := newTestKeys(fx.db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fx.keys = keys
	mgr, feed, r, err := newManager(fx.db, []string{fx.ns}, func(r *Reconciler) {
		r.SecretKeys, r.NetworkPolicy = keys, fx.policy
		for _, f := range tune {
			f(r)
		}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fx.rec, fx.cache = r, mgr.GetClient()
	fx.stop = run(fx.db, mgr, feed)
	t.Cleanup(fx.stop)
	return fx
}

// newManager builds a manager over namespaces whose app reconciler reads
// the database s. The caller starts it with run.
func newManager(s *store.Store, namespaces []string, tune func(*Reconciler), mtune func(*ctrl.Options)) (ctrl.Manager, *dbsource.Feed, *Reconciler, error) {
	watched := map[string]cache.Config{}
	for _, ns := range namespaces {
		watched[ns] = cache.Config{}
	}
	opts := ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		Cache:   cache.Options{DefaultNamespaces: watched},
		// Several managers live in one test process.
		Controller: ctrlconfig.Controller{SkipNameValidation: ptr.To(true)},
	}
	if mtune != nil {
		mtune(&opts)
	}
	mgr, err := ctrl.NewManager(cfg, opts)
	if err != nil {
		return nil, nil, nil, err
	}
	ex := &kube.Executor{Sizes: testSizes, Machines: capacity.Nodes{Reader: mgr.GetClient()}, HomeClaim: testStorage(s).HomeClaim}
	r := &Reconciler{
		Exec:          ex,
		Place:         ex,
		Store:         s,
		Storage:       testStorage(s),
		ReadyTimeout:  time.Minute,
		Sizes:         testSizes,
		Exposure:      pathExposure,
		NetworkPolicy: policy,
		PolicyRecheck: 100 * time.Millisecond,
		Tokens:        &token.DBStore{Tokens: s.Tokens()},
		Secrets:       kube.Secrets{Reader: mgr.GetAPIReader(), Client: mgr.GetClient()},
		APIURL:        "https://javo.example.com",
		CloneImage:    testImages.Clone,
		RunnerImage:   testImages.Runner,
		PixiImage:     testPixiImage,
		NebiURL:       testNebiURL,
		Access:        allowAll{},
	}
	if tune != nil {
		tune(r)
	}
	if err := ex.SetupWithManager(mgr); err != nil {
		return nil, nil, nil, err
	}
	feed := dbsource.NewFeed(s.Changes())
	if err := r.SetupWithManager(mgr, feed); err != nil {
		return nil, nil, nil, err
	}
	if err := setupFakeHomes(mgr, s, feed); err != nil {
		return nil, nil, nil, err
	}
	for _, o := range []client.Object{&corev1.Pod{}, &corev1.Service{}, &corev1.Event{}, &corev1.PersistentVolumeClaim{}, &corev1.Secret{}, &networkingv1.NetworkPolicy{}, &corev1.Namespace{}, &corev1.Node{}} {
		if _, err := mgr.GetCache().GetInformer(bg, o); err != nil {
			return nil, nil, nil, err
		}
	}
	return mgr, feed, r, nil
}

// run starts the manager and the store's change feed and returns a function
// that stops them and waits until they have ended.
func run(s *store.Store, mgr ctrl.Manager, feed *dbsource.Feed) (stop func()) {
	ctx, cancel := context.WithCancel(bg)
	if err := s.StartFeed(ctx); err != nil {
		panic(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); feed.Run(ctx) }()
	go func() {
		defer wg.Done()
		if err := mgr.Start(ctx); err != nil {
			panic(err)
		}
	}()
	// Tests reconcile at once: wait until the cache the reconciler reads is filled.
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		panic("the controller's cache did not sync")
	}
	return func() { cancel(); wg.Wait() }
}

func TestMain(m *testing.M) {
	var (
		stop func()
		err  error
	)
	if cfg, stop, err = ctrltest.Start(); err != nil {
		panic(err)
	}
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if k8s, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		panic(err)
	}
	code := m.Run()
	stop()
	os.Exit(code)
}

// putTemplateIn stores an admin template in s; tune edits the spec first.
func putTemplateIn(t *testing.T, s *store.Store, name string, tune ...func(*model.TemplateSpec)) {
	t.Helper()
	spec := model.TemplateSpec{
		DisplayName: name, Kind: "jupyterlab", Image: "example.com/lab:1", Port: 8888,
		Command: []string{"lab"}, Args: []string{"--base-url", "{{.BaseURL}}"},
	}
	for _, f := range tune {
		f(&spec)
	}
	if _, err := s.Templates().Put(bg, actor, model.Template{Name: name, Source: store.SourceAdmin, Spec: spec}, 0); err != nil {
		t.Fatal(err)
	}
}

func (fx *env) newTemplate(t *testing.T, name string, tune ...func(*model.TemplateSpec)) {
	t.Helper()
	putTemplateIn(t, fx.db, name, tune...)
}

// newAppIn stores an app row in s for ns, which no stand-in for the storage
// controller serves, so the owner's home is prepared first.
func newAppIn(t *testing.T, s *store.Store, ns, name, owner, tpl string, tune ...func(*model.App)) model.App {
	t.Helper()
	primeHome(t, s, owner, ns)
	return createApp(t, s, ns, name, owner, tpl, tune...)
}

// createApp stores an app row in s for ns.
func createApp(t *testing.T, s *store.Store, ns, name, owner, tpl string, tune ...func(*model.App)) model.App {
	t.Helper()
	a := &model.App{Namespace: ns, Name: name, Owner: owner, Template: tpl}
	for _, f := range tune {
		f(a)
	}
	app, err := s.Apps().Create(bg, model.Actor{Person: owner}, a)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// newApp stores an app row the manager reconciles.
func (fx *env) newApp(t *testing.T, name, owner, tpl string, tune ...func(*model.App)) model.App {
	t.Helper()
	return createApp(t, fx.db, fx.ns, name, owner, tpl, tune...)
}

// editApp changes the app's spec through the store, as the gateway does.
func editApp(t *testing.T, s *store.Store, id string, fn func(*model.App)) model.App {
	t.Helper()
	cur, err := s.Apps().Get(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	app, err := s.Apps().UpdateSpec(bg, model.Actor{Person: cur.Owner}, id, cur.Version, func(a *model.App) error { fn(a); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (fx *env) key(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: fx.ns, Name: name}
}

func (fx *env) getApp(t *testing.T, id string) model.App {
	t.Helper()
	return mustApp(t, fx.db, id)
}

func (fx *env) phaseIs(id, phase string) func() bool {
	return func() bool {
		app, err := fx.db.Apps().Get(bg, id)
		return err == nil && app.Status.Phase == phase
	}
}

func (fx *env) exists(obj client.Object, name string) bool {
	return k8s.Get(bg, fx.key(name), obj) == nil
}

// reconcileNow runs one reconcile of the app on r, which reads the API
// server through its own client.
func reconcileNow(t *testing.T, r *Reconciler, id string) reconcile.Result {
	t.Helper()
	res, err := r.Reconcile(bg, reconcile.Request{NamespacedName: types.NamespacedName{Name: id}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// newRec is a Reconciler that reads the API server directly, so a reconcile
// run on it has finished when it returns.
func newRec(s *store.Store, tune ...func(*Reconciler)) *Reconciler {
	ex := &kube.Executor{Client: k8s, Reader: k8s, Sizes: testSizes, Machines: capacity.Nodes{Reader: k8s}, HomeClaim: testStorage(s).HomeClaim}
	r := &Reconciler{Exec: ex, Place: ex, Secrets: kube.Secrets{Reader: k8s, Client: k8s}, Store: s, Storage: testStorage(s), Sizes: testSizes,
		ReadyTimeout: time.Minute, Exposure: pathExposure, NetworkPolicy: policy,
		CloneImage: testImages.Clone, RunnerImage: testImages.Runner, Access: allowAll{}}
	for _, f := range tune {
		f(r)
	}
	return r
}

// onKube is the Kubernetes executor r runs its apps on, for a test that
// sets it up differently.
func onKube(r *Reconciler) *kube.Executor { return r.Exec.(*kube.Executor) }

// allowAll lets every owner use every template.
type allowAll struct{}

func (allowAll) MayUse(context.Context, string, *model.Template) (bool, error) { return true, nil }

func condition(app model.App, typ string) *metav1.Condition {
	return meta.FindStatusCondition(app.Status.Conditions, typ)
}

func TestCreateFromRow(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-create")
	app := fx.newApp(t, "ws-create", "alice", "lab-create")

	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
	got := fx.getApp(t, app.ID)
	if want := pathExposure.BasePath("alice", "ws-create"); got.Status.URL != want {
		t.Fatalf("url = %q, want %q", got.Status.URL, want)
	}
	if got.Status.ObservedVersion != app.Version {
		t.Fatalf("observed version = %d, want %d", got.Status.ObservedVersion, app.Version)
	}
	pod, svc := &corev1.Pod{}, &corev1.Service{}
	if !fx.exists(pod, app.ObjectName()) || !fx.exists(svc, app.ObjectName()) {
		t.Fatal("pod or service missing")
	}
	for _, o := range []client.Object{pod, svc} {
		if o.GetLabels()[model.LabelApp] != app.ID || o.GetLabels()[model.LabelOwner] != "alice" {
			t.Fatalf("%T labels = %v, want the app id and owner", o, o.GetLabels())
		}
		if len(o.GetOwnerReferences()) != 0 {
			t.Fatalf("%T has owner references %v; apps are not custom resources", o, o.GetOwnerReferences())
		}
	}
	if svc.Spec.Selector[model.LabelApp] != app.ID {
		t.Fatalf("service selector = %v", svc.Spec.Selector)
	}
	home, err := fx.db.Homes().ByOwner(bg, fx.ns, "alice")
	if err != nil || home.Spec.Owner != "alice" {
		t.Fatalf("home missing: %+v, %v", home, err)
	}

}

// The URL uses the short name the user knows; the object name is owner-scoped.
func TestURLUsesShortName(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-short")
	app := fx.newApp(t, "sales", "alice", "lab-short")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
	if got, want := fx.getApp(t, app.ID).Status.URL, pathExposure.BasePath("alice", "sales"); got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
	pod := &corev1.Pod{}
	if app.ObjectName() == "sales" || !fx.exists(pod, app.ObjectName()) ||
		!strings.Contains(strings.Join(pod.Spec.Containers[0].Args, " "), pathExposure.BasePath("alice", "sales")) {
		t.Fatalf("pod missing, or not named by the object name, or base URL not the short-name URL: %v", pod.Spec.Containers)
	}
}

// The app reconciler asks for a home and waits; it makes no claim, and no
// pod, until the home is prepared.
func TestAnAppMakesAHomeNotAClaimAndWaitsForIt(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-wait")
	app := fx.newApp(t, "ws-wait", stuckPrefix+"a", "lab-wait")
	eventually(t, "phase Pending", fx.phaseIs(app.ID, "Pending"))

	home, err := fx.db.Homes().ByOwner(bg, fx.ns, stuckPrefix+"a")
	if err != nil {
		t.Fatalf("home not requested: %v", err)
	}
	if home.Spec.Size == nil || home.Spec.Size.Cmp(resource.MustParse("1Gi")) != 0 {
		t.Fatalf("home size = %v, want the configured default 1Gi", home.Spec.Size)
	}
	c := condition(fx.getApp(t, app.ID), model.ConditionHomeReady)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != string(reasons.HomePreparing) {
		t.Fatalf("HomeReady = %+v", c)
	}
	if fx.exists(&corev1.Pod{}, app.ObjectName()) || fx.exists(&corev1.PersistentVolumeClaim{}, naming.HomeClaimName(stuckPrefix+"a")) {
		t.Fatal("a pod or claim exists before the home is prepared")
	}
}

// Whatever stops a home from being mounted is the app's reason too.
func TestAHomeThatCannotBeUsedFailsTheAppWithItsReason(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	for _, tc := range []struct {
		code      reasons.Code
		wantPhase string
	}{
		{reasons.HomeClaimForeign, "Failed"},
		{reasons.TeamStorageFull, "Failed"},
		{reasons.HomePending, "Pending"},
	} {
		t.Run(string(tc.code), func(t *testing.T) {
			owner := stuckPrefix + strings.ToLower(string(tc.code))
			fx.stuckHome(t, owner, metav1.Condition{Status: metav1.ConditionFalse, Reason: string(tc.code), Message: "evidence from the home"})
			fx.newTemplate(t, "lab-"+owner)
			app := fx.newApp(t, "ws-"+owner, owner, "lab-"+owner)
			eventually(t, "phase "+tc.wantPhase, fx.phaseIs(app.ID, tc.wantPhase))

			want := tc.code
			if tc.code == reasons.HomeClaimForeign {
				want = reasons.HomeConflict // the app-level wording for a home that is not theirs
			}
			c := condition(fx.getApp(t, app.ID), model.ConditionHomeReady)
			if c == nil || c.Status != metav1.ConditionFalse || c.Reason != string(want) {
				t.Fatalf("HomeReady = %+v, want reason %s", c, want)
			}
			if fx.exists(&corev1.Pod{}, app.ObjectName()) {
				t.Fatal("pod mounts a home that is not usable")
			}
		})
	}
}

// An app that is already running keeps its real status while its home is
// being (re)prepared, as on upgrade: the pod is the truth, not the home.
func TestARunningAppIsNotBlockedByAHomeBeingPrepared(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-run")
	app := fx.newApp(t, "ws-run", "pat", "lab-run")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))

	fx.setHome(t, "pat", func(h *model.Home) {
		h.Status.Prepared = false
		h.Status.Conditions = []metav1.Condition{{Type: model.ConditionReady, Status: metav1.ConditionFalse, Reason: string(reasons.HomePreparing), LastTransitionTime: metav1.Now()}}
	})
	for range 2 {
		reconcileNow(t, fx.rec, app.ID)
	}
	if got := fx.getApp(t, app.ID).Status.Phase; got != "Starting" {
		t.Fatalf("phase = %s, want the running pod's Starting", got)
	}
}

// While an admin-scheduled copy moves the home, no pod exists and the app
// reads Stopped with HomeMigrating (not Failed, not a spinner); clearing the
// copy brings the pod back with no action from the user.
func TestAppsWaitOutAHomeMigrationAndStartAfterIt(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-mig")
	app := fx.newApp(t, "ws-mig", "mia", "lab-mig")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))

	setMigrating := func(on bool) {
		st := metav1.ConditionFalse
		if on {
			st = metav1.ConditionTrue
		}
		fx.setHome(t, "mia", func(h *model.Home) {
			meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{Type: model.ConditionMigrating, Status: st, Reason: "Copying"})
		})
	}
	setMigrating(true)
	eventually(t, "phase Stopped", fx.phaseIs(app.ID, "Stopped"))
	eventually(t, "pod deleted", func() bool { return !fx.exists(&corev1.Pod{}, app.ObjectName()) })
	c := condition(fx.getApp(t, app.ID), model.ConditionHomeReady)
	if c == nil || c.Reason != string(reasons.HomeMigrating) {
		t.Fatalf("HomeReady = %+v", c)
	}

	setMigrating(false)
	eventually(t, "phase Starting again", fx.phaseIs(app.ID, "Starting"))
	if !fx.exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("no pod after the migration ended")
	}
}

// While an admin restores the owner's home from a backup the app is held
// like during a move, reading HomeRestoring, and starts again after.
func TestAppsWaitOutAHomeRestoreAndStartAfterIt(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-rst")
	app := fx.newApp(t, "ws-rst", "rex", "lab-rst")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
	setRestoring := func(st metav1.ConditionStatus) {
		fx.setHome(t, "rex", func(h *model.Home) {
			meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{Type: model.ConditionRestoring, Status: st, Reason: "HomeRestoreRunning"})
		})
	}
	setRestoring(metav1.ConditionTrue)
	eventually(t, "phase Stopped", fx.phaseIs(app.ID, "Stopped"))
	eventually(t, "pod deleted", func() bool { return !fx.exists(&corev1.Pod{}, app.ObjectName()) })
	if c := condition(fx.getApp(t, app.ID), model.ConditionHomeReady); c == nil || c.Reason != string(reasons.HomeRestoring) {
		t.Fatalf("HomeReady = %+v", c)
	}
	setRestoring(metav1.ConditionFalse)
	eventually(t, "phase Starting again", fx.phaseIs(app.ID, "Starting"))
}

// The pod mounts the ready folders of the owner's groups and carries their
// gids as supplemental groups, never as fsGroup.
func TestPodMountsGroupFoldersWithoutFSGroup(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	for _, group := range []string{"research", "finance"} {
		f, err := fx.db.SharedFolders().Create(bg, actor, model.SharedFolder{Namespace: fx.ns, Name: group, Spec: model.SharedFolderSpec{Group: group}})
		if err != nil {
			t.Fatal(err)
		}
		f.Status = model.SharedFolderStatus{ClaimName: "folder-" + group, GID: 5001, Prepared: true,
			Conditions: []metav1.Condition{{Type: model.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Now()}}}
		if _, err := fx.db.SharedFolders().Update(bg, f, f.Version); err != nil {
			t.Fatal(err)
		}
	}
	fx.newTemplate(t, "lab-grp")
	app := fx.newApp(t, "ws-grp", "gina", "lab-grp")
	eventually(t, "pod", func() bool { return fx.exists(&corev1.Pod{}, app.ObjectName()) })
	pod := &corev1.Pod{}
	fx.exists(pod, app.ObjectName())
	if pod.Spec.SecurityContext.FSGroup != nil {
		t.Error("fsGroup set on an app pod")
	}
	if got := pod.Spec.SecurityContext.SupplementalGroups; len(got) != 1 || got[0] != 5001 {
		t.Errorf("supplemental groups = %v, want only the research folder's gid", got)
	}
	if !slices.ContainsFunc(pod.Spec.Containers[0].VolumeMounts, func(m corev1.VolumeMount) bool { return m.MountPath == "/shared/research" }) {
		t.Errorf("mounts = %+v", pod.Spec.Containers[0].VolumeMounts)
	}
}

func TestMissingTemplateFails(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	app := fx.newApp(t, "ws-notpl", "bob", "no-such-template")

	eventually(t, "phase Failed", fx.phaseIs(app.ID, "Failed"))
	c := condition(fx.getApp(t, app.ID), model.ConditionScheduled)
	if c == nil || c.Reason != "TemplateMissing" || c.Status != metav1.ConditionFalse {
		t.Fatalf("scheduled condition = %+v", c)
	}
	if fx.exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("pod must not exist without a template")
	}
}

// An app on a job-only template (a row written around the API) must not get
// a pod: the template's command is a placeholder that exits at once.
func TestJobOnlyTemplateBuildsNoPod(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-jobonly", func(s *model.TemplateSpec) {
		s.JobOnly = true
		s.Jobs = &model.TemplateJobs{Engines: []string{"command"}}
	})
	app := fx.newApp(t, "ws-jobonly", "bob", "lab-jobonly")

	eventually(t, "phase Failed", fx.phaseIs(app.ID, "Failed"))
	c := condition(fx.getApp(t, app.ID), model.ConditionScheduled)
	if c == nil || c.Reason != "TemplateInvalid" || c.Status != metav1.ConditionFalse {
		t.Fatalf("scheduled condition = %+v", c)
	}
	if !strings.Contains(c.Message, "only runs jobs") {
		t.Fatalf("scheduled condition message lacks the evidence: %q", c.Message)
	}
	if fx.exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("pod must not exist for a job-only template")
	}
}

func TestPauseDeletesPodKeepsServiceAndClaim(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-pause")
	app := fx.newApp(t, "ws-pause", "carol", "lab-pause")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))

	editApp(t, fx.db, app.ID, func(a *model.App) { a.Paused = true })
	eventually(t, "phase Stopped", fx.phaseIs(app.ID, "Stopped"))
	eventually(t, "pod gone", func() bool { return !fx.exists(&corev1.Pod{}, app.ObjectName()) })
	if !fx.exists(&corev1.Service{}, app.ObjectName()) || !fx.exists(&corev1.PersistentVolumeClaim{}, naming.HomeClaimName("carol")) {
		t.Fatal("service and claim must survive pause")
	}

	editApp(t, fx.db, app.ID, func(a *model.App) { a.Paused = false })
	eventually(t, "pod recreated", func() bool { return fx.exists(&corev1.Pod{}, app.ObjectName()) })
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
}

// Deleting an app removes its Pod, Service and token Secret, purges the row,
// and keeps the owner's home.
func TestDeleteRemovesObjectsKeepsHomeClaim(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-del", func(s *model.TemplateSpec) { s.APIToken = true })
	bob := fx.signedIn(t, "bob-del")
	app := fx.newApp(t, "ws-del", bob, "lab-del")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
	fx.appTokenSecret(t, app.ObjectName())

	if err := fx.db.Apps().MarkDeleted(bg, model.Actor{Person: bob}, app.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "row purged", func() bool {
		_, err := fx.db.Apps().Get(bg, app.ID)
		return errors.Is(err, store.ErrNotFound)
	})
	eventually(t, "objects gone", func() bool {
		return !fx.exists(&corev1.Pod{}, app.ObjectName()) && !fx.exists(&corev1.Service{}, app.ObjectName()) &&
			!fx.exists(&corev1.Secret{}, APITokenSecretName(app.ObjectName()))
	})
	claim := &corev1.PersistentVolumeClaim{}
	if !fx.exists(claim, naming.HomeClaimName(bob)) {
		t.Fatal("home claim deleted with the app")
	}
	if len(claim.OwnerReferences) != 0 {
		t.Fatalf("home claim has owner references %v", claim.OwnerReferences)
	}
}

// An app marked deleted while no controller ran loses its objects and its row
// as soon as a controller starts, though nobody announced the change to it.
func TestDeleteWhileControllerDown(t *testing.T) {
	t.Parallel()
	ns := newNamespace(t, "javo-down", nil)
	s := storetest.New(t)
	putTemplateIn(t, s, "lab-down")
	app := newAppIn(t, s, ns, "ws-down", "dave", "lab-down")

	mgr, feed, _, err := newManager(s, []string{ns}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stop := run(s, mgr, feed)
	existsIn := func(obj client.Object) bool {
		return k8s.Get(bg, types.NamespacedName{Namespace: ns, Name: app.ObjectName()}, obj) == nil
	}
	eventually(t, "pod and service", func() bool { return existsIn(&corev1.Pod{}) && existsIn(&corev1.Service{}) })
	stop()

	if err := s.Apps().MarkDeleted(bg, model.Actor{Person: "dave"}, app.ID); err != nil {
		t.Fatal(err)
	}
	if !existsIn(&corev1.Pod{}) {
		t.Fatal("pod vanished with no controller running")
	}
	// A second handle never saw the announcement above.
	s2 := storetest.Reopen(t, s)
	mgr, feed, _, err = newManager(s2, []string{ns}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run(s2, mgr, feed)()
	eventually(t, "objects gone and row purged", func() bool {
		_, err := s2.Apps().Get(bg, app.ID)
		return errors.Is(err, store.ErrNotFound) && !existsIn(&corev1.Pod{}) && !existsIn(&corev1.Service{})
	})
}

func TestReconcileTwiceMakesNoWrites(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-idem")
	app := fx.newApp(t, "ws-idem", "erin", "lab-idem")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
	time.Sleep(500 * time.Millisecond) // let any in-flight reconcile settle

	rvs := func() [3]string {
		pod, svc, pvc := &corev1.Pod{}, &corev1.Service{}, &corev1.PersistentVolumeClaim{}
		fx.exists(pod, app.ObjectName())
		fx.exists(svc, app.ObjectName())
		fx.exists(pvc, naming.HomeClaimName("erin"))
		return [3]string{pod.ResourceVersion, svc.ResourceVersion, pvc.ResourceVersion}
	}
	before, status := rvs(), fx.getApp(t, app.ID).Status
	for range 2 {
		reconcileNow(t, fx.rec, app.ID)
	}
	after := fx.getApp(t, app.ID).Status
	if got := rvs(); got != before {
		t.Fatalf("resource versions changed: %v -> %v", before, got)
	}
	if !conditionsEqual(after.Conditions, status.Conditions) || after.Phase != status.Phase || after.URL != status.URL {
		t.Fatalf("status rewritten: %+v -> %+v", status, after)
	}
}

func conditionsEqual(a, b []metav1.Condition) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] && !(a[i].Type == b[i].Type && a[i].Status == b[i].Status && a[i].Reason == b[i].Reason &&
			a[i].Message == b[i].Message && a[i].LastTransitionTime.Equal(&b[i].LastTransitionTime)) {
			return false
		}
	}
	return true
}

func (fx *env) makePodReady(t *testing.T, name string) {
	t.Helper()
	pod := &corev1.Pod{}
	if !fx.exists(pod, name) {
		t.Fatal("pod missing")
	}
	started := metav1.NewTime(time.Now().Add(-10 * time.Second))
	pod.Status.Phase = corev1.PodRunning
	pod.Status.StartTime = &started
	pod.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: started},
		{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: started},
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app", Ready: true, Image: "example.com/lab:1", ImageID: "x",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: started}},
	}}
	if err := k8s.Status().Update(bg, pod); err != nil {
		t.Fatal(err)
	}
}

// A status write names the spec version the controller acted on and never
// undoes a spec edit made in the meantime.
func TestStatusWriteKeepsSpecEdit(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-keep")
	app := fx.newApp(t, "ws-keep", "alice", "lab-keep")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))

	edited := editApp(t, fx.db, app.ID, func(a *model.App) { a.DisplayName = "renamed"; a.Size = "medium" })
	eventually(t, "status observed the edit", func() bool {
		return fx.getApp(t, app.ID).Status.ObservedVersion == edited.Version
	})
	got := fx.getApp(t, app.ID)
	if got.DisplayName != "renamed" || got.Size != "medium" || got.Version != edited.Version {
		t.Fatalf("spec after status writes: %+v", got)
	}
	// A reconcile that read the old version finishes late.
	if err := fx.db.Apps().SetStatus(bg, app.ID, app.Version, model.AppStatus{Phase: "Starting"}); err != nil {
		t.Fatal(err)
	}
	if got := fx.getApp(t, app.ID); got.DisplayName != "renamed" || got.Size != "medium" || got.Version != edited.Version {
		t.Fatalf("late status write undid the spec edit: %+v", got)
	}
}

// Running time is recorded: an interval opens when the app becomes Running
// and closes when it stops.
func TestUsageIntervalOpenClose(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-usage")
	app := fx.newApp(t, "ws-usage", "alice", "lab-usage")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
	intervals := func() []store.UsageInterval {
		got, err := fx.db.Usage().ForApp(bg, app.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if n := len(intervals()); n != 0 {
		t.Fatalf("%d intervals before the app ran", n)
	}

	fx.makePodReady(t, app.ObjectName())
	eventually(t, "phase Running", fx.phaseIs(app.ID, "Running"))
	got := intervals()
	if len(got) != 1 || !got[0].StoppedAt.IsZero() || got[0].Owner != "alice" || got[0].Template != "lab-usage" {
		t.Fatalf("intervals while running: %+v", got)
	}

	editApp(t, fx.db, app.ID, func(a *model.App) { a.Paused = true })
	eventually(t, "phase Stopped", fx.phaseIs(app.ID, "Stopped"))
	if got = intervals(); len(got) != 1 || got[0].StoppedAt.IsZero() {
		t.Fatalf("intervals after stop: %+v", got)
	}
}

// An app that never picked a size runs with the default size, and its usage
// interval names that size, so a later change of the default cannot make the
// row ambiguous.
func TestUsageIntervalNamesDefaultSize(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-usage-size")
	app := fx.newApp(t, "ws-usage-size", "alice", "lab-usage-size")
	if app.Size != "" {
		t.Fatalf("app size = %q, want none", app.Size)
	}
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
	fx.makePodReady(t, app.ObjectName())
	eventually(t, "phase Running", fx.phaseIs(app.ID, "Running"))
	got, err := fx.db.Usage().ForApp(bg, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Size != testSizes.Default() {
		t.Fatalf("intervals: %+v, want size %q", got, testSizes.Default())
	}
}

// A restart request replaces the pod once. Later reconciles, including the
// periodic ones, leave the new pod alone.
func TestRestartRequest(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-restart")
	app := fx.newApp(t, "ws-restart", "alice", "lab-restart")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
	old := &corev1.Pod{}
	if !fx.exists(old, app.ObjectName()) {
		t.Fatal("pod missing")
	}

	editApp(t, fx.db, app.ID, func(a *model.App) { a.RestartRequestedAt = time.Now() })
	var fresh corev1.Pod
	eventually(t, "a new pod", func() bool { return fx.exists(&fresh, app.ObjectName()) && fresh.UID != old.UID })
	eventually(t, "the cache sees the new pod", func() bool {
		p := &corev1.Pod{}
		return fx.cache.Get(bg, fx.key(app.ObjectName()), p) == nil && p.UID == fresh.UID
	})
	for range 3 {
		reconcileNow(t, fx.rec, app.ID)
	}
	time.Sleep(300 * time.Millisecond)
	if cur := (&corev1.Pod{}); !fx.exists(cur, app.ObjectName()) || cur.UID != fresh.UID {
		t.Fatal("the pod was replaced again without a new request")
	}
}

func TestRunningButNotReadyFailsAfterTimeout(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-slow")
	app := fx.newApp(t, "ws-slow", "frank", "lab-slow")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))

	pod := &corev1.Pod{}
	if !fx.exists(pod, app.ObjectName()) {
		t.Fatal("pod missing")
	}
	started := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	pod.Status.Phase = corev1.PodRunning
	pod.Status.StartTime = &started
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app", Image: "example.com/lab:1", ImageID: "x",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: started}},
	}}
	if err := k8s.Status().Update(bg, pod); err != nil {
		t.Fatal(err)
	}
	eventually(t, "phase Failed", fx.phaseIs(app.ID, "Failed"))
	if c := condition(fx.getApp(t, app.ID), model.ConditionReady); c == nil || c.Reason != "AppNotAnswering" {
		t.Fatalf("ready condition = %+v", c)
	}
}

func TestPodEventsReachStatus(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-ev")
	app := fx.newApp(t, "ws-ev", "gina", "lab-ev")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))

	live := &corev1.Pod{}
	fx.exists(live, app.ObjectName())
	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "ws-ev.fs", Namespace: fx.ns},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: app.ObjectName(), Namespace: fx.ns, UID: live.UID},
		Reason:         "FailedAttachVolume", Message: "Multi-Attach error for volume", Type: corev1.EventTypeWarning,
	}
	if err := k8s.Create(bg, ev); err != nil {
		t.Fatal(err)
	}
	eventually(t, "HomeBusy reason", func() bool {
		c := condition(fx.getApp(t, app.ID), model.ConditionHomeReady)
		return c != nil && c.Reason == "HomeBusy"
	})
}

func TestStaleEventFromDeletedPodIsIgnored(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-stale")
	app := fx.newApp(t, "ws-stale", "hana", "lab-stale")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
	old := &corev1.Pod{}
	fx.exists(old, app.ObjectName())

	editApp(t, fx.db, app.ID, func(a *model.App) { a.Paused = true })
	eventually(t, "phase Stopped", fx.phaseIs(app.ID, "Stopped"))
	editApp(t, fx.db, app.ID, func(a *model.App) { a.Paused = false })
	eventually(t, "pod recreated", func() bool {
		p := &corev1.Pod{}
		return fx.exists(p, app.ObjectName()) && p.UID != old.UID
	})
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))

	stale := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "ws-stale.old", Namespace: fx.ns},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: app.ObjectName(), Namespace: fx.ns, UID: old.UID},
		Reason:         "FailedAttachVolume", Message: "Multi-Attach error for volume", Type: corev1.EventTypeWarning,
	}
	if err := k8s.Create(bg, stale); err != nil {
		t.Fatal(err)
	}
	// The manager cache must see the event before the direct reconcile below.
	eventually(t, "cache sees event", func() bool {
		var l corev1.EventList
		return fx.cache.List(bg, &l, client.InNamespace(fx.ns)) == nil && func() bool {
			for _, e := range l.Items {
				if e.Name == "ws-stale.old" {
					return true
				}
			}
			return false
		}()
	})
	reconcileNow(t, fx.rec, app.ID)
	c := condition(fx.getApp(t, app.ID), model.ConditionHomeReady)
	if c == nil || c.Reason == "HomeBusy" {
		t.Fatalf("stale event changed status: %+v", c)
	}
}

func TestTemplateCreatedLaterRecoversApp(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	app := fx.newApp(t, "ws-late", "ivy", "lab-late")
	eventually(t, "phase Failed", fx.phaseIs(app.ID, "Failed"))
	fx.newTemplate(t, "lab-late")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
}

// An image saved while its registry was allowed does not start once the
// admin removes the registry from containerImages.allowedRegistries.
func TestImageFromARegistryNoLongerAllowedDoesNotStart(t *testing.T) {
	t.Parallel()
	fx := newEnv(t, func(r *Reconciler) { r.AllowedRegistries = []string{"ghcr.io/example"} })
	fx.newTemplate(t, "web-own", func(s *model.TemplateSpec) {
		s.Image, s.UserImage, s.Command, s.Args, s.ReadinessPath = "", true, nil, nil, ""
	})
	app := fx.newApp(t, "ws-own", "kim", "web-own", func(a *model.App) { a.Image = "registry.example.com/private/web:1" })
	eventually(t, "phase Failed", fx.phaseIs(app.ID, "Failed"))
	c := condition(fx.getApp(t, app.ID), model.ConditionScheduled)
	if c == nil || c.Reason != "ImageNotAllowed" || !strings.Contains(c.Message, "registry.example.com") {
		t.Fatalf("scheduled condition = %+v", c)
	}
	if fx.exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("pod must not exist for an image whose registry is not allowed")
	}
}

// App pods get the isolation the admin chose for people's pods: here their
// own user namespace (hostUsers false).
func TestAppPodsRunInTheirOwnUserNamespaceWhenAsked(t *testing.T) {
	t.Parallel()
	fx := newEnv(t, func(r *Reconciler) { onKube(r).Isolation = podspec.Isolation{UserNamespace: true} })
	fx.newTemplate(t, "lab-userns")
	app := fx.newApp(t, "ws-userns", "lou", "lab-userns")
	var pod corev1.Pod
	eventually(t, "pod created", func() bool {
		return fx.exists(&pod, app.ObjectName())
	})
	if pod.Spec.HostUsers == nil || *pod.Spec.HostUsers {
		t.Fatalf("hostUsers = %v, want false", pod.Spec.HostUsers)
	}
}

// With the lock on (workloads.landlock), the reconciler starts a coding
// agent's pod locked in, and leaves other apps' pods as they are.
func TestReconcilerLocksCodingAgentsWhenAsked(t *testing.T) {
	t.Parallel()
	fx := newEnv(t, func(r *Reconciler) { r.LockAgents = true })
	fx.newTemplate(t, "agent-locked", func(s *model.TemplateSpec) { s.Category = model.CategoryAgents })
	fx.newTemplate(t, "lab-unlocked")
	agent := fx.newApp(t, "ws-agent-locked", "lou", "agent-locked")
	lab := fx.newApp(t, "ws-lab-unlocked", "lou", "lab-unlocked")
	var agentPod, labPod corev1.Pod
	eventually(t, "pods created", func() bool {
		return fx.exists(&agentPod, agent.ObjectName()) && fx.exists(&labPod, lab.ObjectName())
	})
	if c := agentPod.Spec.Containers[0].Command; len(c) == 0 || c[0] != agentLock {
		t.Fatalf("agent command = %q, want it started through %s", c, agentLock)
	}
	if c := labPod.Spec.Containers[0].Command; slices.Contains(c, agentLock) {
		t.Fatalf("lab command = %q, want it unlocked", c)
	}
}

func TestInvalidTemplateFailsWithTemplateInvalid(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-bad", func(s *model.TemplateSpec) { s.Command = nil; s.Args = []string{"{{.Nope}}"} })
	app := fx.newApp(t, "ws-bad", "jon", "lab-bad")
	eventually(t, "phase Failed", fx.phaseIs(app.ID, "Failed"))
	c := condition(fx.getApp(t, app.ID), model.ConditionScheduled)
	if c == nil || c.Reason != "TemplateInvalid" {
		t.Fatalf("scheduled condition = %+v", c)
	}
	if fx.exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("pod must not exist for an invalid template")
	}
}

func (fx *env) appResources(t *testing.T, app model.App) (*corev1.Pod, corev1.ResourceList) {
	t.Helper()
	pod := &corev1.Pod{}
	if !fx.exists(pod, app.ObjectName()) {
		t.Fatal("pod missing")
	}
	return pod, pod.Spec.Containers[0].Resources.Limits
}

// An app with no size runs at the default size.
func TestEmptySizeRunsAtDefault(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-size-default")
	app := fx.newApp(t, "ws-size-default", "alice", "lab-size-default")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
	_, lim := fx.appResources(t, app)
	if !lim.Cpu().Equal(resource.MustParse("1")) || !lim.Memory().Equal(resource.MustParse("2Gi")) {
		t.Fatalf("limits = %v, want the small size", lim)
	}
}

// Changing the size replaces the pod with one of the new size.
func TestChangingSizeRestartsThePod(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-resize")
	app := fx.newApp(t, "ws-resize", "alice", "lab-resize")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
	old, _ := fx.appResources(t, app)

	editApp(t, fx.db, app.ID, func(a *model.App) { a.Size = "medium" })
	eventually(t, "pod of the new size", func() bool {
		p := &corev1.Pod{}
		return fx.exists(p, app.ObjectName()) && p.UID != old.UID &&
			p.Spec.Containers[0].Resources.Limits.Memory().Equal(resource.MustParse("8Gi"))
	})
}

// A size that is not configured fails the app with SizeUnknown and builds
// no pod.
func TestUnknownSizeFailsWithSizeUnknown(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-size-unknown")
	app := fx.newApp(t, "ws-size-unknown", "bob", "lab-size-unknown", func(a *model.App) { a.Size = "huge" })
	eventually(t, "phase Failed", fx.phaseIs(app.ID, "Failed"))
	c := condition(fx.getApp(t, app.ID), model.ConditionScheduled)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != string(reasons.SizeUnknown) || !strings.Contains(c.Message, "huge") {
		t.Fatalf("scheduled condition = %+v", c)
	}
	if fx.exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("pod built for an unknown size")
	}

	editApp(t, fx.db, app.ID, func(a *model.App) { a.Size = "small" })
	eventually(t, "pod built once the size is known", func() bool { return fx.exists(&corev1.Pod{}, app.ObjectName()) })
}

// A pod from before sizes existed (no size annotation) of an app that never
// picked a size keeps running: upgrading does not restart every app.
func TestPodWithoutSizeAnnotationIsKeptForAppWithoutSize(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-legacy")
	app := fx.newApp(t, "ws-legacy", "alice", "lab-legacy")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
	pod, _ := fx.appResources(t, app)
	delete(pod.Annotations, model.AnnotationSize)
	if err := k8s.Update(bg, pod); err != nil {
		t.Fatal(err)
	}
	// Wait for the cache to see the stripped pod, or for it to be replaced.
	eventually(t, "cache sees the update", func() bool {
		p := &corev1.Pod{}
		err := fx.cache.Get(bg, fx.key(app.ObjectName()), p)
		return apierrors.IsNotFound(err) || err == nil && (p.UID != pod.UID || p.Annotations[model.AnnotationSize] == "")
	})
	reconcileNow(t, fx.rec, app.ID)
	if got := &(corev1.Pod{}); !fx.exists(got, app.ObjectName()) || got.UID != pod.UID {
		t.Fatal("pod without a size annotation was replaced")
	}
}

func TestAllocatedModeWaitsForTheOwnersUID(t *testing.T) {
	t.Parallel()
	alloc := newNamespace(t, "javo-alloc", nil)
	s := storetest.New(t)
	person, err := s.People(store.IDRange{}).Ensure(bg, store.Claims{Issuer: "https://idp.example.com", Subject: "dora", Username: "dora"})
	if err != nil {
		t.Fatal(err)
	}
	putTemplateIn(t, s, "lab-alloc")
	app := newAppIn(t, s, alloc, "ws-alloc", person.Name, "lab-alloc")
	r := newRec(s, func(r *Reconciler) { r.UIDMode = config.UIDModeAllocated })

	reconcileNow(t, r, app.ID)
	c := condition(mustApp(t, s, app.ID), model.ConditionHomeReady)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != string(reasons.IdentityPending) {
		t.Fatalf("HomeReady = %+v", c)
	}
	if err := k8s.Get(bg, types.NamespacedName{Namespace: alloc, Name: app.ObjectName()}, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("pod exists before a uid was allocated: %v", err)
	}

	storetest.Exec(t, s, "UPDATE people SET uid = ? WHERE id = ?", 100500, person.ID)
	reconcileNow(t, r, app.ID)
	pod := &corev1.Pod{}
	if err := k8s.Get(bg, types.NamespacedName{Namespace: alloc, Name: app.ObjectName()}, pod); err != nil {
		t.Fatal(err)
	}
	if uid := *pod.Spec.SecurityContext.RunAsUser; uid != 100500 {
		t.Fatalf("runAsUser = %d, want the owner's uid", uid)
	}
}

func mustApp(t *testing.T, s *store.Store, id string) model.App {
	t.Helper()
	app, err := s.Apps().Get(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// A running app keeps its real status when its owner's person row is gone
// (offboarding).
func TestExistingPodKeepsStatusWhenThePersonIsMissing(t *testing.T) {
	t.Parallel()
	alloc := newNamespace(t, "javo-alloc", nil)
	s := storetest.New(t)
	person, err := s.People(store.IDRange{First: 100600, Last: 100699}).Ensure(bg, store.Claims{Issuer: "https://idp.example.com", Subject: "erin", Username: "erin"})
	if err != nil {
		t.Fatal(err)
	}
	putTemplateIn(t, s, "lab-gone")
	app := newAppIn(t, s, alloc, "ws-gone", person.Name, "lab-gone")
	r := newRec(s, func(r *Reconciler) { r.UIDMode = config.UIDModeAllocated })
	reconcileNow(t, r, app.ID)
	if err := k8s.Get(bg, types.NamespacedName{Namespace: alloc, Name: app.ObjectName()}, &corev1.Pod{}); err != nil {
		t.Fatal(err)
	}

	storetest.Exec(t, s, "DELETE FROM people WHERE id = ?", person.ID)
	reconcileNow(t, r, app.ID)
	got := mustApp(t, s, app.ID)
	if c := condition(got, model.ConditionHomeReady); c != nil && c.Reason == string(reasons.IdentityPending) {
		t.Fatalf("HomeReady = %+v, want status derived from the existing pod", c)
	}
	if got.Status.Phase == "Pending" {
		t.Fatalf("phase = %s with a pod present", got.Status.Phase)
	}
}

// pathExposure is the one-host layout: apps under /u/<owner>/<name>/.
var pathExposure = exposurepath.New(config.URLs{})

// With subdomain URLs the status URL is the app's own host and the app is
// told to serve from "/".
func TestSubdomainExposureSetsHostURLAndRootBase(t *testing.T) {
	t.Parallel()
	alloc := newNamespace(t, "javo-alloc", nil)
	s := storetest.New(t)
	putTemplateIn(t, s, "lab-sub")
	app := newAppIn(t, s, alloc, "sales", "carol", "lab-sub")
	r := newRec(s, func(r *Reconciler) {
		r.Exposure = exposuresubdomain.New(config.URLs{AppsDomain: "apps.example.com", Scheme: "https"})
	})
	reconcileNow(t, r, app.ID)
	if got, want := mustApp(t, s, app.ID).Status.URL, "https://"+app.ObjectName()+".apps.example.com/"; got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
	pod := &corev1.Pod{}
	if err := k8s.Get(bg, types.NamespacedName{Namespace: alloc, Name: app.ObjectName()}, pod); err != nil || strings.Join(pod.Spec.Containers[0].Args, " ") != "--base-url /" {
		t.Fatalf("pod missing or not served from /: %v %v", err, pod.Spec.Containers)
	}
}

// Where apps may live in many namespaces, an app is built only in a namespace
// the controller labelled as managed: elsewhere there is no Pod Security
// label and no gateway-only NetworkPolicy.
func TestManagedOnlyIgnoresAppsInOtherNamespaces(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	putTemplateIn(t, s, "lab-managed")
	stray := newNamespace(t, "javo-stray", nil)
	labelled := newNamespace(t, "javo-labelled", map[string]string{model.LabelManaged: "true"})
	r := newRec(s, func(r *Reconciler) { onKube(r).ManagedOnly = true })
	build := func(ns string) types.NamespacedName {
		t.Helper()
		app := newAppIn(t, s, ns, "managed-"+ns, "alice", "lab-managed")
		reconcileNow(t, r, app.ID)
		return types.NamespacedName{Namespace: ns, Name: app.ObjectName()}
	}

	if err := k8s.Get(bg, build(stray), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("pod in an unlabelled namespace: %v", err)
	}
	if err := k8s.Get(bg, build(labelled), &corev1.Pod{}); err != nil {
		t.Fatalf("pod in a managed namespace: %v", err)
	}
}

// A namespace labelled after its app was created (or not yet visible in the
// cache) must not leave the app without a Pod: the app is looked at again
// until the namespace is managed.
func TestManagedOnlyRetriesUntilTheNamespaceIsLabelled(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	putTemplateIn(t, s, "lab-relabel")
	ns := &corev1.Namespace{}
	if err := k8s.Get(bg, client.ObjectKey{Name: newNamespace(t, "javo-relabel", nil)}, ns); err != nil {
		t.Fatal(err)
	}
	app := newAppIn(t, s, ns.Name, "ws-relabel", "alice", "lab-relabel")
	r := newRec(s, func(r *Reconciler) { onKube(r).ManagedOnly = true })
	podKey := types.NamespacedName{Namespace: ns.Name, Name: app.ObjectName()}

	if res := reconcileNow(t, r, app.ID); res.RequeueAfter <= 0 {
		t.Fatalf("unmanaged namespace: result %+v; want a requeue", res)
	}
	if err := k8s.Get(bg, podKey, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("pod before the label: %v", err)
	}

	ns.Labels = map[string]string{model.LabelManaged: "true"}
	if err := k8s.Update(bg, ns); err != nil {
		t.Fatal(err)
	}
	reconcileNow(t, r, app.ID)
	if err := k8s.Get(bg, podKey, &corev1.Pod{}); err != nil {
		t.Fatalf("pod after the label: %v", err)
	}
}

// Apps that check the websocket Origin are told the host the browser loads
// them from: their own subdomain, the apps host, or the public host when apps
// share the UI host.
func TestTemplateHostIsTheHostTheAppIsServedOn(t *testing.T) {
	t.Parallel()
	alloc := newNamespace(t, "javo-alloc", nil)
	s := storetest.New(t)
	putTemplateIn(t, s, "lab-origin", func(ts *model.TemplateSpec) {
		ts.Kind, ts.Image, ts.Port = "bokeh", "example.com/bokeh:1", 5006
		ts.Command, ts.Args = nil, []string{"--allow-websocket-origin={{.Host}}"}
	})
	for _, tc := range []struct {
		name     string
		exposure exposure.Backend
		want     string
	}{
		{"origin-sub", exposuresubdomain.New(config.URLs{AppsDomain: "apps.example.com", Scheme: "https"}), naming.ObjectName("frank", "origin-sub") + ".apps.example.com"},
		{"origin-apps", exposurepath.New(config.URLs{AppsHost: "apps.example.com", Scheme: "https"}), "apps.example.com"},
		{"origin-one", pathExposure, "javo.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newAppIn(t, s, alloc, tc.name, "frank", "lab-origin")
			r := newRec(s, func(r *Reconciler) { r.PublicHost, r.Exposure = "javo.example.com", tc.exposure })
			reconcileNow(t, r, app.ID)
			pod := &corev1.Pod{}
			if err := k8s.Get(bg, types.NamespacedName{Namespace: alloc, Name: app.ObjectName()}, pod); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(pod.Spec.Containers[0].Args, " "); got != "--allow-websocket-origin="+tc.want {
				t.Fatalf("args = %q, want origin %q", got, tc.want)
			}
		})
	}
}

// stubPolicy is the NetworkPolicy probe result the tests control.
type stubPolicy struct {
	mu    sync.Mutex
	state netprobe.State
}

func (s *stubPolicy) set(st netprobe.State) { s.mu.Lock(); s.state = st; s.mu.Unlock() }
func (s *stubPolicy) Get(context.Context) (netprobe.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return netprobe.Result{State: s.state}, nil
}

// policy is the probe result for reconcilers whose test does not change it.
var policy = &stubPolicy{state: netprobe.Enforced}

func TestLoginlessAppWaitsForEnforcedNetworkPolicy(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.policy.set(netprobe.NotEnforced)
	t.Cleanup(func() { fx.policy.set(netprobe.Enforced) })
	fx.newTemplate(t, "lab-loginless") // appLogin defaults to Disabled
	app := fx.newApp(t, "ws-loginless", "alice", "lab-loginless")

	eventually(t, "phase Failed", fx.phaseIs(app.ID, "Failed"))
	c := condition(fx.getApp(t, app.ID), model.ConditionScheduled)
	if c == nil || c.Reason != "NetworkPolicyNotEnforced" {
		t.Fatalf("scheduled condition = %+v", c)
	}
	if fx.exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("pod created although NetworkPolicy is not enforced")
	}

	fx.policy.set(netprobe.Enforced)
	eventually(t, "pod after the policy is enforced", func() bool { return fx.exists(&corev1.Pod{}, app.ObjectName()) })
}

// A site app has its Build step from the start, before any pod: here the pod
// waits for an enforced NetworkPolicy.
func TestSiteAppHasABuildStepBeforeItsPod(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.policy.set(netprobe.NotEnforced)
	t.Cleanup(func() { fx.policy.set(netprobe.Enforced) })
	fx.newTemplate(t, "site-steps", func(s *model.TemplateSpec) {
		s.Kind, s.Command, s.Args, s.FileExtensions = "static", nil, nil, []string{".html"}
		s.Build = &model.TemplateBuild{Image: "example.com/node:1", ServeDir: "/usr/share/nginx/html"}
	})
	app := fx.newApp(t, "ws-site-steps", "alice", "site-steps", func(a *model.App) { a.File = "page.html" })

	eventually(t, "phase Failed", fx.phaseIs(app.ID, "Failed"))
	var got []string
	for _, c := range fx.getApp(t, app.ID).Status.Conditions {
		got = append(got, c.Type)
	}
	if want := []string{"Scheduled", "HomeReady", "ImageReady", "Built", "Started", "Ready"}; !slices.Equal(got, want) {
		t.Fatalf("conditions = %v, want %v", got, want)
	}
}

func TestAppWithItsOwnLoginStartsWithoutEnforcedNetworkPolicy(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.policy.set(netprobe.Unknown)
	t.Cleanup(func() { fx.policy.set(netprobe.Enforced) })
	fx.newTemplate(t, "lab-login", func(s *model.TemplateSpec) { s.AppLogin = "Enabled" })
	app := fx.newApp(t, "ws-login", "alice", "lab-login")
	eventually(t, "pod", func() bool { return fx.exists(&corev1.Pod{}, app.ObjectName()) })
}

// Changing the typed command replaces the pod with one running the new one.
func TestChangingCommandRestartsThePod(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-recommand")
	app := fx.newApp(t, "ws-recommand", "alice", "lab-recommand")
	editApp(t, fx.db, app.ID, func(a *model.App) { a.Command = "serve --port={{.Port}}" })
	var old corev1.Pod
	eventually(t, "pod with the first command", func() bool {
		return fx.exists(&old, app.ObjectName()) && strings.Join(old.Spec.Containers[0].Args, " ") == "serve --port=8888"
	})
	editApp(t, fx.db, app.ID, func(a *model.App) { a.Command = "serve --port={{.Port}} --debug" })
	eventually(t, "pod with the new command", func() bool {
		p := &corev1.Pod{}
		return fx.exists(p, app.ObjectName()) && p.UID != old.UID && strings.Join(p.Spec.Containers[0].Args, " ") == "serve --port=8888 --debug"
	})
}

// Events last about an hour. What each done step recorded while its events
// existed stays in the app's status after they are gone.
func TestStepFactsOutliveTheirEvents(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-facts")
	app := fx.newApp(t, "ws-facts", "ivy", "lab-facts")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))

	pod := &corev1.Pod{}
	fx.exists(pod, app.ObjectName())
	if err := k8s.SubResource("binding").Create(bg, pod, &corev1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
		Target:     corev1.ObjectReference{Kind: "Node", Name: "node-1"},
	}); err != nil {
		t.Fatal(err)
	}
	fx.exists(pod, app.ObjectName())
	placed := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
	started := metav1.NewTime(placed.Add(5 * time.Second))
	answered := metav1.NewTime(placed.Add(9 * time.Second))
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: placed},
		{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: answered},
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app", Ready: true, Image: "example.com/lab:1", ImageID: "example.com/lab@sha256:4b1c",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: started}},
	}}
	if err := k8s.Status().Update(bg, pod); err != nil {
		t.Fatal(err)
	}
	pulled := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-facts.pulled", Namespace: fx.ns},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: app.ObjectName(), Namespace: fx.ns, UID: pod.UID,
			FieldPath: "spec.containers{app}"},
		Reason: "Pulled", Message: `Successfully pulled image "example.com/lab:1" in 4.2s (4.2s including waiting)`, Type: corev1.EventTypeNormal,
	}
	if err := k8s.Create(bg, pulled); err != nil {
		t.Fatal(err)
	}
	message := func(typ string) string {
		c := condition(fx.getApp(t, app.ID), typ)
		if c == nil {
			return ""
		}
		return c.Message
	}
	eventually(t, "download recorded", func() bool { return strings.Contains(message(model.ConditionImageReady), "downloaded in 4.2s") })

	if err := k8s.Delete(bg, pulled); err != nil {
		t.Fatal(err)
	}
	eventually(t, "cache drops event", func() bool {
		return apierrors.IsNotFound(fx.cache.Get(bg, client.ObjectKeyFromObject(pulled), &corev1.Event{}))
	})
	reconcileNow(t, fx.rec, app.ID)
	want := map[string]string{
		model.ConditionScheduled:  "node node-1\nplaced at " + placed.UTC().Format("2006-01-02 15:04:05 UTC"),
		model.ConditionImageReady: "image example.com/lab:1\ndigest sha256:4b1c\ndownloaded in 4.2s",
		model.ConditionStarted:    "container app started at " + started.UTC().Format("2006-01-02 15:04:05 UTC") + "\nrestarts 0",
		model.ConditionReady:      "checked GET :8888/u/ivy/ws-facts/\nanswered 4s after the container started",
	}
	for typ, w := range want {
		if got := message(typ); got != w {
			t.Errorf("%s message = %q, want %q", typ, got, w)
		}
	}
	if got := message(model.ConditionHomeReady); !strings.HasPrefix(got, "claim "+naming.HomeClaimName("ivy")+", ") {
		t.Errorf("HomeReady message = %q", got)
	}
}

// Only the elected leader reconciles. Two managers share one database; the
// URL each writes into status tells them apart.
func TestLeaderOnly(t *testing.T) {
	t.Parallel()
	ns := newNamespace(t, "javo-leader", nil)
	s1 := storetest.New(t)
	s2 := storetest.Reopen(t, s1)
	putTemplateIn(t, s1, "lab-leader")
	subdomain := exposuresubdomain.New(config.URLs{AppsDomain: "b.example.com", Scheme: "https"})
	elect := func(o *ctrl.Options) {
		o.LeaderElection, o.LeaderElectionID = true, "javo-leader-test"
		o.LeaderElectionNamespace, o.LeaderElectionReleaseOnCancel = ns, true
		// The second manager tries for the lease this often, so it takes over
		// soon after the first lets go.
		o.RetryPeriod = ptr.To(100 * time.Millisecond)
	}
	mgrA, feedA, _, err := newManager(s1, []string{ns}, nil, elect)
	if err != nil {
		t.Fatal(err)
	}
	stopA := run(s1, mgrA, feedA)
	defer stopA()
	select {
	case <-mgrA.Elected():
	case <-time.After(15 * time.Second):
		t.Fatal("first manager was not elected")
	}
	mgrB, feedB, _, err := newManager(s2, []string{ns}, func(r *Reconciler) { r.Exposure = subdomain }, elect)
	if err != nil {
		t.Fatal(err)
	}
	defer run(s2, mgrB, feedB)()

	app := newAppIn(t, s1, ns, "lead", "alice", "lab-leader")
	uA, uB := pathExposure.URLFor("alice", "lead", app.ObjectName()), subdomain.URLFor("alice", "lead", app.ObjectName())
	urlA, urlB := uA.String(), uB.String()
	urlAt := func(version int64) func() string {
		return func() string {
			got := mustApp(t, s1, app.ID)
			if got.Status.ObservedVersion != version {
				return ""
			}
			return got.Status.URL
		}
	}
	waitURL := func(version int64, want string) {
		t.Helper()
		eventually(t, "status for version "+want, func() bool { return urlAt(version)() == want })
	}
	waitURL(1, urlA)
	edited := editApp(t, s1, app.ID, func(a *model.App) { a.DisplayName = "again" })
	waitURL(edited.Version, urlA)
	time.Sleep(time.Second)
	if got := urlAt(edited.Version)(); got != urlA {
		t.Fatalf("status url = %q, want the leader's %q (the other manager is %q)", got, urlA, urlB)
	}

	stopA()
	edited = editApp(t, s2, app.ID, func(a *model.App) { a.DisplayName = "third" })
	waitURL(edited.Version, urlB)
}

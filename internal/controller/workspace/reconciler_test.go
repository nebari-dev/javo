package workspace

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
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
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/features/exposurepath"
	"github.com/nebari-dev/javo/internal/features/exposuresubdomain"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

const (
	testNS = "javo-apps"
	// allocNS is not watched by the manager; tests that need a Reconciler
	// of their own reconcile there directly so the manager's reconciler
	// cannot race them.
	allocNS = "javo-alloc"
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
	rec    *Reconciler
	db     *store.Store
	scheme = runtime.NewScheme()
	cfg    *rest.Config
)

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
	r := &Reconciler{
		Client:        mgr.GetClient(),
		Store:         s,
		Storage:       testStorage(s),
		ReadyTimeout:  time.Minute,
		Sizes:         testSizes,
		Machines:      capacity.Nodes{Reader: mgr.GetClient()},
		Exposure:      pathExposure,
		NetworkPolicy: policy,
		PolicyRecheck: 100 * time.Millisecond,
		Tokens:        &token.DBStore{Tokens: s.Tokens()},
		Secrets:       mgr.GetAPIReader(),
		APIURL:        "https://javo.example.com",
		CloneImage:    testImages.Clone,
		RunnerImage:   testImages.Runner,
		PixiImage:     testPixiImage,
		NebiURL:       testNebiURL,
	}
	if tune != nil {
		tune(r)
	}
	feed := dbsource.NewFeed(s.Changes())
	if err := r.SetupWithManager(mgr, feed); err != nil {
		return nil, nil, nil, err
	}
	if err := setupFakeHomes(mgr, s, feed); err != nil {
		return nil, nil, nil, err
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
	env := &envtest.Environment{}
	var err error
	if cfg, err = env.Start(); err != nil {
		panic(err)
	}
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	dir, err := os.MkdirTemp("", "javo-workspace-test")
	if err != nil {
		panic(err)
	}
	scfg, cleanup, err := storetest.FreshConfig(bg, storetest.Engine(), os.Getenv("JAVO_TEST_PG_DSN"), dir)
	if err != nil {
		panic(err)
	}
	if db, err = store.Open(bg, scfg); err != nil {
		panic(err)
	}
	if err := db.Migrate(bg); err != nil {
		panic(err)
	}
	policy.set(netprobe.Enforced)
	if testKeys, err = newTestKeys(db, dir); err != nil {
		panic(err)
	}
	mgr, feed, r, err := newManager(db, []string{testNS}, func(r *Reconciler) { r.SecretKeys = testKeys }, nil)
	if err != nil {
		panic(err)
	}
	rec = r
	stop := run(db, mgr, feed)
	if k8s, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		panic(err)
	}
	for _, ns := range []string{testNS, allocNS} {
		if err := k8s.Create(bg, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
			panic(err)
		}
	}
	code := m.Run()
	stop()
	_ = db.Close()
	cleanup()
	_ = os.RemoveAll(dir)
	_ = env.Stop()
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

func newTemplate(t *testing.T, name string, tune ...func(*model.TemplateSpec)) {
	t.Helper()
	putTemplateIn(t, db, name, tune...)
}

// newAppIn stores an app row in s. Outside the test namespace, which the
// stand-in for the storage controller serves, the owner's home is prepared
// first.
func newAppIn(t *testing.T, s *store.Store, ns, name, owner, tpl string, tune ...func(*model.App)) model.App {
	t.Helper()
	if ns != testNS {
		primeHome(t, s, owner, ns)
	}
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
func newApp(t *testing.T, name, owner, tpl string, tune ...func(*model.App)) model.App {
	t.Helper()
	return newAppIn(t, db, testNS, name, owner, tpl, tune...)
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
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func key(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: testNS, Name: name}
}

func getApp(t *testing.T, id string) model.App {
	t.Helper()
	return mustApp(t, db, id)
}

func phaseIs(id, phase string) func() bool {
	return func() bool {
		app, err := db.Apps().Get(bg, id)
		return err == nil && app.Status.Phase == phase
	}
}

func exists(obj client.Object, name string) bool {
	return k8s.Get(bg, key(name), obj) == nil
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
	r := &Reconciler{Client: k8s, Store: s, Storage: testStorage(s), Sizes: rec.Sizes, ReadyTimeout: rec.ReadyTimeout, Exposure: pathExposure, NetworkPolicy: policy,
		Machines: capacity.Nodes{Reader: k8s}, CloneImage: testImages.Clone, RunnerImage: testImages.Runner}
	for _, f := range tune {
		f(r)
	}
	return r
}

func condition(app model.App, typ string) *metav1.Condition {
	return meta.FindStatusCondition(app.Status.Conditions, typ)
}

func TestCreateFromRow(t *testing.T) {
	newTemplate(t, "lab-create")
	app := newApp(t, "ws-create", "alice", "lab-create")

	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))
	got := getApp(t, app.ID)
	if want := pathExposure.BasePath("alice", "ws-create"); got.Status.URL != want {
		t.Fatalf("url = %q, want %q", got.Status.URL, want)
	}
	if got.Status.ObservedVersion != app.Version {
		t.Fatalf("observed version = %d, want %d", got.Status.ObservedVersion, app.Version)
	}
	pod, svc := &corev1.Pod{}, &corev1.Service{}
	if !exists(pod, app.ObjectName()) || !exists(svc, app.ObjectName()) {
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
	home, err := db.Homes().ByOwner(bg, testNS, "alice")
	if err != nil || home.Spec.Owner != "alice" {
		t.Fatalf("home missing: %+v, %v", home, err)
	}

}

// The URL uses the short name the user knows; the object name is owner-scoped.
func TestURLUsesShortName(t *testing.T) {
	newTemplate(t, "lab-short")
	app := newApp(t, "sales", "alice", "lab-short")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))
	if got, want := getApp(t, app.ID).Status.URL, pathExposure.BasePath("alice", "sales"); got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
	pod := &corev1.Pod{}
	if app.ObjectName() == "sales" || !exists(pod, app.ObjectName()) ||
		!strings.Contains(strings.Join(pod.Spec.Containers[0].Args, " "), pathExposure.BasePath("alice", "sales")) {
		t.Fatalf("pod missing, or not named by the object name, or base URL not the short-name URL: %v", pod.Spec.Containers)
	}
}

// The app reconciler asks for a home and waits; it makes no claim, and no
// pod, until the home is prepared.
func TestAnAppMakesAHomeNotAClaimAndWaitsForIt(t *testing.T) {
	newTemplate(t, "lab-wait")
	app := newApp(t, "ws-wait", stuckPrefix+"a", "lab-wait")
	eventually(t, "phase Pending", phaseIs(app.ID, "Pending"))

	home, err := db.Homes().ByOwner(bg, testNS, stuckPrefix+"a")
	if err != nil {
		t.Fatalf("home not requested: %v", err)
	}
	if home.Spec.Size == nil || home.Spec.Size.Cmp(resource.MustParse("1Gi")) != 0 {
		t.Fatalf("home size = %v, want the configured default 1Gi", home.Spec.Size)
	}
	c := condition(getApp(t, app.ID), model.ConditionHomeReady)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != string(reasons.HomePreparing) {
		t.Fatalf("HomeReady = %+v", c)
	}
	if exists(&corev1.Pod{}, app.ObjectName()) || exists(&corev1.PersistentVolumeClaim{}, naming.HomeClaimName(stuckPrefix+"a")) {
		t.Fatal("a pod or claim exists before the home is prepared")
	}
}

// Whatever stops a home from being mounted is the app's reason too.
func TestAHomeThatCannotBeUsedFailsTheAppWithItsReason(t *testing.T) {
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
			stuckHome(t, owner, metav1.Condition{Status: metav1.ConditionFalse, Reason: string(tc.code), Message: "evidence from the home"})
			newTemplate(t, "lab-"+owner)
			app := newApp(t, "ws-"+owner, owner, "lab-"+owner)
			eventually(t, "phase "+tc.wantPhase, phaseIs(app.ID, tc.wantPhase))

			want := tc.code
			if tc.code == reasons.HomeClaimForeign {
				want = reasons.HomeConflict // the app-level wording for a home that is not theirs
			}
			c := condition(getApp(t, app.ID), model.ConditionHomeReady)
			if c == nil || c.Status != metav1.ConditionFalse || c.Reason != string(want) {
				t.Fatalf("HomeReady = %+v, want reason %s", c, want)
			}
			if exists(&corev1.Pod{}, app.ObjectName()) {
				t.Fatal("pod mounts a home that is not usable")
			}
		})
	}
}

// An app that is already running keeps its real status while its home is
// being (re)prepared, as on upgrade: the pod is the truth, not the home.
func TestARunningAppIsNotBlockedByAHomeBeingPrepared(t *testing.T) {
	newTemplate(t, "lab-run")
	app := newApp(t, "ws-run", "pat", "lab-run")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))

	setHome(t, "pat", func(h *model.Home) {
		h.Status.Prepared = false
		h.Status.Conditions = []metav1.Condition{{Type: model.ConditionReady, Status: metav1.ConditionFalse, Reason: string(reasons.HomePreparing), LastTransitionTime: metav1.Now()}}
	})
	for range 2 {
		reconcileNow(t, rec, app.ID)
	}
	if got := getApp(t, app.ID).Status.Phase; got != "Starting" {
		t.Fatalf("phase = %s, want the running pod's Starting", got)
	}
}

// While an admin-scheduled copy moves the home, no pod exists and the app
// reads Stopped with HomeMigrating (not Failed, not a spinner); clearing the
// copy brings the pod back with no action from the user.
func TestAppsWaitOutAHomeMigrationAndStartAfterIt(t *testing.T) {
	newTemplate(t, "lab-mig")
	app := newApp(t, "ws-mig", "mia", "lab-mig")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))

	setMigrating := func(on bool) {
		st := metav1.ConditionFalse
		if on {
			st = metav1.ConditionTrue
		}
		setHome(t, "mia", func(h *model.Home) {
			meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{Type: model.ConditionMigrating, Status: st, Reason: "Copying"})
		})
	}
	setMigrating(true)
	eventually(t, "phase Stopped", phaseIs(app.ID, "Stopped"))
	eventually(t, "pod deleted", func() bool { return !exists(&corev1.Pod{}, app.ObjectName()) })
	c := condition(getApp(t, app.ID), model.ConditionHomeReady)
	if c == nil || c.Reason != string(reasons.HomeMigrating) {
		t.Fatalf("HomeReady = %+v", c)
	}

	setMigrating(false)
	eventually(t, "phase Starting again", phaseIs(app.ID, "Starting"))
	if !exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("no pod after the migration ended")
	}
}

// While an admin restores the owner's home from a backup the app is held
// like during a move, reading HomeRestoring, and starts again after.
func TestAppsWaitOutAHomeRestoreAndStartAfterIt(t *testing.T) {
	newTemplate(t, "lab-rst")
	app := newApp(t, "ws-rst", "rex", "lab-rst")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))
	setRestoring := func(st metav1.ConditionStatus) {
		setHome(t, "rex", func(h *model.Home) {
			meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{Type: model.ConditionRestoring, Status: st, Reason: "HomeRestoreRunning"})
		})
	}
	setRestoring(metav1.ConditionTrue)
	eventually(t, "phase Stopped", phaseIs(app.ID, "Stopped"))
	eventually(t, "pod deleted", func() bool { return !exists(&corev1.Pod{}, app.ObjectName()) })
	if c := condition(getApp(t, app.ID), model.ConditionHomeReady); c == nil || c.Reason != string(reasons.HomeRestoring) {
		t.Fatalf("HomeReady = %+v", c)
	}
	setRestoring(metav1.ConditionFalse)
	eventually(t, "phase Starting again", phaseIs(app.ID, "Starting"))
}

// The pod mounts the ready folders of the owner's groups and carries their
// gids as supplemental groups, never as fsGroup.
func TestPodMountsGroupFoldersWithoutFSGroup(t *testing.T) {
	for _, group := range []string{"research", "finance"} {
		f, err := db.SharedFolders().Create(bg, actor, model.SharedFolder{Namespace: testNS, Name: group, Spec: model.SharedFolderSpec{Group: group}})
		if err != nil {
			t.Fatal(err)
		}
		f.Status = model.SharedFolderStatus{ClaimName: "folder-" + group, GID: 5001, Prepared: true,
			Conditions: []metav1.Condition{{Type: model.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Now()}}}
		if _, err := db.SharedFolders().Update(bg, f, f.Version); err != nil {
			t.Fatal(err)
		}
	}
	newTemplate(t, "lab-grp")
	app := newApp(t, "ws-grp", "gina", "lab-grp")
	eventually(t, "pod", func() bool { return exists(&corev1.Pod{}, app.ObjectName()) })
	pod := &corev1.Pod{}
	exists(pod, app.ObjectName())
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
	app := newApp(t, "ws-notpl", "bob", "no-such-template")

	eventually(t, "phase Failed", phaseIs(app.ID, "Failed"))
	c := condition(getApp(t, app.ID), model.ConditionScheduled)
	if c == nil || c.Reason != "TemplateMissing" || c.Status != metav1.ConditionFalse {
		t.Fatalf("scheduled condition = %+v", c)
	}
	if exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("pod must not exist without a template")
	}
}

// An app on a job-only template (a row written around the API) must not get
// a pod: the template's command is a placeholder that exits at once.
func TestJobOnlyTemplateBuildsNoPod(t *testing.T) {
	newTemplate(t, "lab-jobonly", func(s *model.TemplateSpec) {
		s.JobOnly = true
		s.Jobs = &model.TemplateJobs{Engines: []string{"command"}}
	})
	app := newApp(t, "ws-jobonly", "bob", "lab-jobonly")

	eventually(t, "phase Failed", phaseIs(app.ID, "Failed"))
	c := condition(getApp(t, app.ID), model.ConditionScheduled)
	if c == nil || c.Reason != "TemplateInvalid" || c.Status != metav1.ConditionFalse {
		t.Fatalf("scheduled condition = %+v", c)
	}
	if !strings.Contains(c.Message, "only runs jobs") {
		t.Fatalf("scheduled condition message lacks the evidence: %q", c.Message)
	}
	if exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("pod must not exist for a job-only template")
	}
}

func TestPauseDeletesPodKeepsServiceAndClaim(t *testing.T) {
	newTemplate(t, "lab-pause")
	app := newApp(t, "ws-pause", "carol", "lab-pause")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))

	editApp(t, db, app.ID, func(a *model.App) { a.Paused = true })
	eventually(t, "phase Stopped", phaseIs(app.ID, "Stopped"))
	eventually(t, "pod gone", func() bool { return !exists(&corev1.Pod{}, app.ObjectName()) })
	if !exists(&corev1.Service{}, app.ObjectName()) || !exists(&corev1.PersistentVolumeClaim{}, naming.HomeClaimName("carol")) {
		t.Fatal("service and claim must survive pause")
	}

	editApp(t, db, app.ID, func(a *model.App) { a.Paused = false })
	eventually(t, "pod recreated", func() bool { return exists(&corev1.Pod{}, app.ObjectName()) })
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))
}

// Deleting an app removes its Pod, Service and token Secret, purges the row,
// and keeps the owner's home.
func TestDeleteRemovesObjectsKeepsHomeClaim(t *testing.T) {
	newTemplate(t, "lab-del", func(s *model.TemplateSpec) { s.APIToken = true })
	bob := signedIn(t, "bob-del")
	app := newApp(t, "ws-del", bob, "lab-del")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))
	appTokenSecret(t, app.ObjectName())

	if err := db.Apps().MarkDeleted(bg, model.Actor{Person: bob}, app.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "row purged", func() bool {
		_, err := db.Apps().Get(bg, app.ID)
		return errors.Is(err, store.ErrNotFound)
	})
	eventually(t, "objects gone", func() bool {
		return !exists(&corev1.Pod{}, app.ObjectName()) && !exists(&corev1.Service{}, app.ObjectName()) &&
			!exists(&corev1.Secret{}, APITokenSecretName(app.ObjectName()))
	})
	claim := &corev1.PersistentVolumeClaim{}
	if !exists(claim, naming.HomeClaimName(bob)) {
		t.Fatal("home claim deleted with the app")
	}
	if len(claim.OwnerReferences) != 0 {
		t.Fatalf("home claim has owner references %v", claim.OwnerReferences)
	}
}

// An app marked deleted while no controller ran loses its objects and its row
// as soon as a controller starts, though nobody announced the change to it.
func TestDeleteWhileControllerDown(t *testing.T) {
	const ns = "javo-down"
	if err := k8s.Create(bg, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
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
	newTemplate(t, "lab-idem")
	app := newApp(t, "ws-idem", "erin", "lab-idem")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))
	time.Sleep(500 * time.Millisecond) // let any in-flight reconcile settle

	rvs := func() [3]string {
		pod, svc, pvc := &corev1.Pod{}, &corev1.Service{}, &corev1.PersistentVolumeClaim{}
		exists(pod, app.ObjectName())
		exists(svc, app.ObjectName())
		exists(pvc, naming.HomeClaimName("erin"))
		return [3]string{pod.ResourceVersion, svc.ResourceVersion, pvc.ResourceVersion}
	}
	before, status := rvs(), getApp(t, app.ID).Status
	for range 2 {
		reconcileNow(t, rec, app.ID)
	}
	after := getApp(t, app.ID).Status
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

func makePodReady(t *testing.T, name string) {
	t.Helper()
	pod := &corev1.Pod{}
	if !exists(pod, name) {
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
	newTemplate(t, "lab-keep")
	app := newApp(t, "ws-keep", "alice", "lab-keep")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))

	edited := editApp(t, db, app.ID, func(a *model.App) { a.DisplayName = "renamed"; a.Size = "medium" })
	eventually(t, "status observed the edit", func() bool {
		return getApp(t, app.ID).Status.ObservedVersion == edited.Version
	})
	got := getApp(t, app.ID)
	if got.DisplayName != "renamed" || got.Size != "medium" || got.Version != edited.Version {
		t.Fatalf("spec after status writes: %+v", got)
	}
	// A reconcile that read the old version finishes late.
	if err := db.Apps().SetStatus(bg, app.ID, app.Version, model.AppStatus{Phase: "Starting"}); err != nil {
		t.Fatal(err)
	}
	if got := getApp(t, app.ID); got.DisplayName != "renamed" || got.Size != "medium" || got.Version != edited.Version {
		t.Fatalf("late status write undid the spec edit: %+v", got)
	}
}

// Running time is recorded: an interval opens when the app becomes Running
// and closes when it stops.
func TestUsageIntervalOpenClose(t *testing.T) {
	newTemplate(t, "lab-usage")
	app := newApp(t, "ws-usage", "alice", "lab-usage")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))
	intervals := func() []store.UsageInterval {
		got, err := db.Usage().ForApp(bg, app.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if n := len(intervals()); n != 0 {
		t.Fatalf("%d intervals before the app ran", n)
	}

	makePodReady(t, app.ObjectName())
	eventually(t, "phase Running", phaseIs(app.ID, "Running"))
	got := intervals()
	if len(got) != 1 || !got[0].StoppedAt.IsZero() || got[0].Owner != "alice" || got[0].Template != "lab-usage" {
		t.Fatalf("intervals while running: %+v", got)
	}

	editApp(t, db, app.ID, func(a *model.App) { a.Paused = true })
	eventually(t, "phase Stopped", phaseIs(app.ID, "Stopped"))
	if got = intervals(); len(got) != 1 || got[0].StoppedAt.IsZero() {
		t.Fatalf("intervals after stop: %+v", got)
	}
}

// An app that never picked a size runs with the default size, and its usage
// interval names that size, so a later change of the default cannot make the
// row ambiguous.
func TestUsageIntervalNamesDefaultSize(t *testing.T) {
	newTemplate(t, "lab-usage-size")
	app := newApp(t, "ws-usage-size", "alice", "lab-usage-size")
	if app.Size != "" {
		t.Fatalf("app size = %q, want none", app.Size)
	}
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))
	makePodReady(t, app.ObjectName())
	eventually(t, "phase Running", phaseIs(app.ID, "Running"))
	got, err := db.Usage().ForApp(bg, app.ID)
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
	newTemplate(t, "lab-restart")
	app := newApp(t, "ws-restart", "alice", "lab-restart")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))
	old := &corev1.Pod{}
	if !exists(old, app.ObjectName()) {
		t.Fatal("pod missing")
	}

	editApp(t, db, app.ID, func(a *model.App) { a.RestartRequestedAt = time.Now() })
	var fresh corev1.Pod
	eventually(t, "a new pod", func() bool { return exists(&fresh, app.ObjectName()) && fresh.UID != old.UID })
	eventually(t, "the cache sees the new pod", func() bool {
		p := &corev1.Pod{}
		return rec.Get(bg, key(app.ObjectName()), p) == nil && p.UID == fresh.UID
	})
	for range 3 {
		reconcileNow(t, rec, app.ID)
	}
	time.Sleep(300 * time.Millisecond)
	if cur := (&corev1.Pod{}); !exists(cur, app.ObjectName()) || cur.UID != fresh.UID {
		t.Fatal("the pod was replaced again without a new request")
	}
}

func TestRunningButNotReadyFailsAfterTimeout(t *testing.T) {
	newTemplate(t, "lab-slow")
	app := newApp(t, "ws-slow", "frank", "lab-slow")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))

	pod := &corev1.Pod{}
	if !exists(pod, app.ObjectName()) {
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
	eventually(t, "phase Failed", phaseIs(app.ID, "Failed"))
	if c := condition(getApp(t, app.ID), model.ConditionReady); c == nil || c.Reason != "AppNotAnswering" {
		t.Fatalf("ready condition = %+v", c)
	}
}

func TestPodEventsReachStatus(t *testing.T) {
	newTemplate(t, "lab-ev")
	app := newApp(t, "ws-ev", "gina", "lab-ev")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))

	live := &corev1.Pod{}
	exists(live, app.ObjectName())
	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "ws-ev.fs", Namespace: testNS},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: app.ObjectName(), Namespace: testNS, UID: live.UID},
		Reason:         "FailedAttachVolume", Message: "Multi-Attach error for volume", Type: corev1.EventTypeWarning,
	}
	if err := k8s.Create(bg, ev); err != nil {
		t.Fatal(err)
	}
	eventually(t, "HomeBusy reason", func() bool {
		c := condition(getApp(t, app.ID), model.ConditionHomeReady)
		return c != nil && c.Reason == "HomeBusy"
	})
}

func TestStaleEventFromDeletedPodIsIgnored(t *testing.T) {
	newTemplate(t, "lab-stale")
	app := newApp(t, "ws-stale", "hana", "lab-stale")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))
	old := &corev1.Pod{}
	exists(old, app.ObjectName())

	editApp(t, db, app.ID, func(a *model.App) { a.Paused = true })
	eventually(t, "phase Stopped", phaseIs(app.ID, "Stopped"))
	editApp(t, db, app.ID, func(a *model.App) { a.Paused = false })
	eventually(t, "pod recreated", func() bool {
		p := &corev1.Pod{}
		return exists(p, app.ObjectName()) && p.UID != old.UID
	})
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))

	stale := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "ws-stale.old", Namespace: testNS},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: app.ObjectName(), Namespace: testNS, UID: old.UID},
		Reason:         "FailedAttachVolume", Message: "Multi-Attach error for volume", Type: corev1.EventTypeWarning,
	}
	if err := k8s.Create(bg, stale); err != nil {
		t.Fatal(err)
	}
	// The manager cache must see the event before the direct reconcile below.
	eventually(t, "cache sees event", func() bool {
		var l corev1.EventList
		return rec.List(bg, &l, client.InNamespace(testNS)) == nil && func() bool {
			for _, e := range l.Items {
				if e.Name == "ws-stale.old" {
					return true
				}
			}
			return false
		}()
	})
	reconcileNow(t, rec, app.ID)
	c := condition(getApp(t, app.ID), model.ConditionHomeReady)
	if c == nil || c.Reason == "HomeBusy" {
		t.Fatalf("stale event changed status: %+v", c)
	}
}

func TestTemplateCreatedLaterRecoversApp(t *testing.T) {
	app := newApp(t, "ws-late", "ivy", "lab-late")
	eventually(t, "phase Failed", phaseIs(app.ID, "Failed"))
	newTemplate(t, "lab-late")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))
}

func TestInvalidTemplateFailsWithTemplateInvalid(t *testing.T) {
	newTemplate(t, "lab-bad", func(s *model.TemplateSpec) { s.Command = nil; s.Args = []string{"{{.Nope}}"} })
	app := newApp(t, "ws-bad", "jon", "lab-bad")
	eventually(t, "phase Failed", phaseIs(app.ID, "Failed"))
	c := condition(getApp(t, app.ID), model.ConditionScheduled)
	if c == nil || c.Reason != "TemplateInvalid" {
		t.Fatalf("scheduled condition = %+v", c)
	}
	if exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("pod must not exist for an invalid template")
	}
}

func appResources(t *testing.T, app model.App) (*corev1.Pod, corev1.ResourceList) {
	t.Helper()
	pod := &corev1.Pod{}
	if !exists(pod, app.ObjectName()) {
		t.Fatal("pod missing")
	}
	return pod, pod.Spec.Containers[0].Resources.Limits
}

// An app with no size runs at the default size.
func TestEmptySizeRunsAtDefault(t *testing.T) {
	newTemplate(t, "lab-size-default")
	app := newApp(t, "ws-size-default", "alice", "lab-size-default")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))
	_, lim := appResources(t, app)
	if !lim.Cpu().Equal(resource.MustParse("1")) || !lim.Memory().Equal(resource.MustParse("2Gi")) {
		t.Fatalf("limits = %v, want the small size", lim)
	}
}

// Changing the size replaces the pod with one of the new size.
func TestChangingSizeRestartsThePod(t *testing.T) {
	newTemplate(t, "lab-resize")
	app := newApp(t, "ws-resize", "alice", "lab-resize")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))
	old, _ := appResources(t, app)

	editApp(t, db, app.ID, func(a *model.App) { a.Size = "medium" })
	eventually(t, "pod of the new size", func() bool {
		p := &corev1.Pod{}
		return exists(p, app.ObjectName()) && p.UID != old.UID &&
			p.Spec.Containers[0].Resources.Limits.Memory().Equal(resource.MustParse("8Gi"))
	})
}

// A size that is not configured fails the app with SizeUnknown and builds
// no pod.
func TestUnknownSizeFailsWithSizeUnknown(t *testing.T) {
	newTemplate(t, "lab-size-unknown")
	app := newApp(t, "ws-size-unknown", "bob", "lab-size-unknown", func(a *model.App) { a.Size = "huge" })
	eventually(t, "phase Failed", phaseIs(app.ID, "Failed"))
	c := condition(getApp(t, app.ID), model.ConditionScheduled)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != string(reasons.SizeUnknown) || !strings.Contains(c.Message, "huge") {
		t.Fatalf("scheduled condition = %+v", c)
	}
	if exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("pod built for an unknown size")
	}

	editApp(t, db, app.ID, func(a *model.App) { a.Size = "small" })
	eventually(t, "pod built once the size is known", func() bool { return exists(&corev1.Pod{}, app.ObjectName()) })
}

// A pod from before sizes existed (no size annotation) of an app that never
// picked a size keeps running: upgrading does not restart every app.
func TestPodWithoutSizeAnnotationIsKeptForAppWithoutSize(t *testing.T) {
	newTemplate(t, "lab-legacy")
	app := newApp(t, "ws-legacy", "alice", "lab-legacy")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))
	pod, _ := appResources(t, app)
	delete(pod.Annotations, model.AnnotationSize)
	if err := k8s.Update(bg, pod); err != nil {
		t.Fatal(err)
	}
	// Wait for the cache to see the stripped pod, or for it to be replaced.
	eventually(t, "cache sees the update", func() bool {
		p := &corev1.Pod{}
		err := rec.Get(bg, key(app.ObjectName()), p)
		return apierrors.IsNotFound(err) || err == nil && (p.UID != pod.UID || p.Annotations[model.AnnotationSize] == "")
	})
	reconcileNow(t, rec, app.ID)
	if got := &(corev1.Pod{}); !exists(got, app.ObjectName()) || got.UID != pod.UID {
		t.Fatal("pod without a size annotation was replaced")
	}
}

func TestAllocatedModeWaitsForTheOwnersUID(t *testing.T) {
	s := storetest.New(t)
	person, err := s.People(store.IDRange{}).Ensure(bg, store.Claims{Issuer: "https://idp.example.com", Subject: "dora", Username: "dora"})
	if err != nil {
		t.Fatal(err)
	}
	putTemplateIn(t, s, "lab-alloc")
	app := newAppIn(t, s, allocNS, "ws-alloc", person.Name, "lab-alloc")
	r := newRec(s, func(r *Reconciler) { r.UIDMode = config.UIDModeAllocated })

	reconcileNow(t, r, app.ID)
	c := condition(mustApp(t, s, app.ID), model.ConditionHomeReady)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != string(reasons.IdentityPending) {
		t.Fatalf("HomeReady = %+v", c)
	}
	if err := k8s.Get(bg, types.NamespacedName{Namespace: allocNS, Name: app.ObjectName()}, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("pod exists before a uid was allocated: %v", err)
	}

	storetest.Exec(t, s, "UPDATE people SET uid = ? WHERE id = ?", 100500, person.ID)
	reconcileNow(t, r, app.ID)
	pod := &corev1.Pod{}
	if err := k8s.Get(bg, types.NamespacedName{Namespace: allocNS, Name: app.ObjectName()}, pod); err != nil {
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
	s := storetest.New(t)
	person, err := s.People(store.IDRange{First: 100600, Last: 100699}).Ensure(bg, store.Claims{Issuer: "https://idp.example.com", Subject: "erin", Username: "erin"})
	if err != nil {
		t.Fatal(err)
	}
	putTemplateIn(t, s, "lab-gone")
	app := newAppIn(t, s, allocNS, "ws-gone", person.Name, "lab-gone")
	r := newRec(s, func(r *Reconciler) { r.UIDMode = config.UIDModeAllocated })
	reconcileNow(t, r, app.ID)
	if err := k8s.Get(bg, types.NamespacedName{Namespace: allocNS, Name: app.ObjectName()}, &corev1.Pod{}); err != nil {
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
	s := storetest.New(t)
	putTemplateIn(t, s, "lab-sub")
	app := newAppIn(t, s, allocNS, "sales", "carol", "lab-sub")
	r := newRec(s, func(r *Reconciler) {
		r.Exposure = exposuresubdomain.New(config.URLs{AppsDomain: "apps.example.com", Scheme: "https"})
	})
	reconcileNow(t, r, app.ID)
	if got, want := mustApp(t, s, app.ID).Status.URL, "https://"+app.ObjectName()+".apps.example.com/"; got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
	pod := &corev1.Pod{}
	if err := k8s.Get(bg, types.NamespacedName{Namespace: allocNS, Name: app.ObjectName()}, pod); err != nil || strings.Join(pod.Spec.Containers[0].Args, " ") != "--base-url /" {
		t.Fatalf("pod missing or not served from /: %v %v", err, pod.Spec.Containers)
	}
}

// Where apps may live in many namespaces, an app is built only in a namespace
// the controller labelled as managed: elsewhere there is no Pod Security
// label and no gateway-only NetworkPolicy.
func TestManagedOnlyIgnoresAppsInOtherNamespaces(t *testing.T) {
	s := storetest.New(t)
	putTemplateIn(t, s, "lab-managed")
	for name, labels := range map[string]map[string]string{
		"javo-stray":    nil,
		"javo-labelled": {model.LabelManaged: "true"},
	} {
		if err := k8s.Create(bg, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}); err != nil {
			t.Fatal(err)
		}
	}
	r := newRec(s, func(r *Reconciler) { r.ManagedNamespacesOnly = true })
	build := func(ns string) types.NamespacedName {
		t.Helper()
		app := newAppIn(t, s, ns, "managed-"+ns, "alice", "lab-managed")
		reconcileNow(t, r, app.ID)
		return types.NamespacedName{Namespace: ns, Name: app.ObjectName()}
	}

	if err := k8s.Get(bg, build("javo-stray"), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("pod in an unlabelled namespace: %v", err)
	}
	if err := k8s.Get(bg, build("javo-labelled"), &corev1.Pod{}); err != nil {
		t.Fatalf("pod in a managed namespace: %v", err)
	}
}

// A namespace labelled after its app was created (or not yet visible in the
// cache) must not leave the app without a Pod: the app is looked at again
// until the namespace is managed.
func TestManagedOnlyRetriesUntilTheNamespaceIsLabelled(t *testing.T) {
	s := storetest.New(t)
	putTemplateIn(t, s, "lab-relabel")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "javo-relabel"}}
	if err := k8s.Create(bg, ns); err != nil {
		t.Fatal(err)
	}
	app := newAppIn(t, s, "javo-relabel", "ws-relabel", "alice", "lab-relabel")
	r := newRec(s, func(r *Reconciler) { r.ManagedNamespacesOnly = true })
	podKey := types.NamespacedName{Namespace: "javo-relabel", Name: app.ObjectName()}

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
			app := newAppIn(t, s, allocNS, tc.name, "frank", "lab-origin")
			r := newRec(s, func(r *Reconciler) { r.PublicHost, r.Exposure = "javo.example.com", tc.exposure })
			reconcileNow(t, r, app.ID)
			pod := &corev1.Pod{}
			if err := k8s.Get(bg, types.NamespacedName{Namespace: allocNS, Name: app.ObjectName()}, pod); err != nil {
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

var policy = &stubPolicy{}

func TestLoginlessAppWaitsForEnforcedNetworkPolicy(t *testing.T) {
	policy.set(netprobe.NotEnforced)
	t.Cleanup(func() { policy.set(netprobe.Enforced) })
	newTemplate(t, "lab-loginless") // appLogin defaults to Disabled
	app := newApp(t, "ws-loginless", "alice", "lab-loginless")

	eventually(t, "phase Failed", phaseIs(app.ID, "Failed"))
	c := condition(getApp(t, app.ID), model.ConditionScheduled)
	if c == nil || c.Reason != "NetworkPolicyNotEnforced" {
		t.Fatalf("scheduled condition = %+v", c)
	}
	if exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("pod created although NetworkPolicy is not enforced")
	}

	policy.set(netprobe.Enforced)
	eventually(t, "pod after the policy is enforced", func() bool { return exists(&corev1.Pod{}, app.ObjectName()) })
}

// A site app has its Build step from the start, before any pod: here the pod
// waits for an enforced NetworkPolicy.
func TestSiteAppHasABuildStepBeforeItsPod(t *testing.T) {
	policy.set(netprobe.NotEnforced)
	t.Cleanup(func() { policy.set(netprobe.Enforced) })
	newTemplate(t, "site-steps", func(s *model.TemplateSpec) {
		s.Kind, s.Command, s.Args, s.FileExtensions = "static", nil, nil, []string{".html"}
		s.Build = &model.TemplateBuild{Image: "example.com/node:1", ServeDir: "/usr/share/nginx/html"}
	})
	app := newApp(t, "ws-site-steps", "alice", "site-steps", func(a *model.App) { a.File = "page.html" })

	eventually(t, "phase Failed", phaseIs(app.ID, "Failed"))
	var got []string
	for _, c := range getApp(t, app.ID).Status.Conditions {
		got = append(got, c.Type)
	}
	if want := []string{"Scheduled", "HomeReady", "ImageReady", "Built", "Started", "Ready"}; !slices.Equal(got, want) {
		t.Fatalf("conditions = %v, want %v", got, want)
	}
}

func TestAppWithItsOwnLoginStartsWithoutEnforcedNetworkPolicy(t *testing.T) {
	policy.set(netprobe.Unknown)
	t.Cleanup(func() { policy.set(netprobe.Enforced) })
	newTemplate(t, "lab-login", func(s *model.TemplateSpec) { s.AppLogin = "Enabled" })
	app := newApp(t, "ws-login", "alice", "lab-login")
	eventually(t, "pod", func() bool { return exists(&corev1.Pod{}, app.ObjectName()) })
}

// Changing the typed command replaces the pod with one running the new one.
func TestChangingCommandRestartsThePod(t *testing.T) {
	newTemplate(t, "lab-recommand")
	app := newApp(t, "ws-recommand", "alice", "lab-recommand")
	editApp(t, db, app.ID, func(a *model.App) { a.Command = "serve --port={{.Port}}" })
	var old corev1.Pod
	eventually(t, "pod with the first command", func() bool {
		return exists(&old, app.ObjectName()) && strings.Join(old.Spec.Containers[0].Args, " ") == "serve --port=8888"
	})
	editApp(t, db, app.ID, func(a *model.App) { a.Command = "serve --port={{.Port}} --debug" })
	eventually(t, "pod with the new command", func() bool {
		p := &corev1.Pod{}
		return exists(p, app.ObjectName()) && p.UID != old.UID && strings.Join(p.Spec.Containers[0].Args, " ") == "serve --port=8888 --debug"
	})
}

// Events last about an hour. What each done step recorded while its events
// existed stays in the app's status after they are gone.
func TestStepFactsOutliveTheirEvents(t *testing.T) {
	newTemplate(t, "lab-facts")
	app := newApp(t, "ws-facts", "ivy", "lab-facts")
	eventually(t, "phase Starting", phaseIs(app.ID, "Starting"))

	pod := &corev1.Pod{}
	exists(pod, app.ObjectName())
	if err := k8s.SubResource("binding").Create(bg, pod, &corev1.Binding{
		ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
		Target:     corev1.ObjectReference{Kind: "Node", Name: "node-1"},
	}); err != nil {
		t.Fatal(err)
	}
	exists(pod, app.ObjectName())
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
		ObjectMeta: metav1.ObjectMeta{Name: "ws-facts.pulled", Namespace: testNS},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: app.ObjectName(), Namespace: testNS, UID: pod.UID,
			FieldPath: "spec.containers{app}"},
		Reason: "Pulled", Message: `Successfully pulled image "example.com/lab:1" in 4.2s (4.2s including waiting)`, Type: corev1.EventTypeNormal,
	}
	if err := k8s.Create(bg, pulled); err != nil {
		t.Fatal(err)
	}
	message := func(typ string) string {
		c := condition(getApp(t, app.ID), typ)
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
		return apierrors.IsNotFound(rec.Get(bg, client.ObjectKeyFromObject(pulled), &corev1.Event{}))
	})
	reconcileNow(t, rec, app.ID)
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
	const ns = "javo-leader"
	if err := k8s.Create(bg, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	s1 := storetest.New(t)
	s2 := storetest.Reopen(t, s1)
	putTemplateIn(t, s1, "lab-leader")
	subdomain := exposuresubdomain.New(config.URLs{AppsDomain: "b.example.com", Scheme: "https"})
	elect := func(o *ctrl.Options) {
		o.LeaderElection, o.LeaderElectionID = true, "javo-leader-test"
		o.LeaderElectionNamespace, o.LeaderElectionReleaseOnCancel = ns, true
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

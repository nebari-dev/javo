package workspace

import (
	"context"
	"slices"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// lockedGroups is group membership a test changes between reconciles.
type lockedGroups struct {
	mu sync.Mutex
	m  map[string][]string
}

func (g *lockedGroups) Groups(_ context.Context, owner string) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.m[owner]), nil
}

func (g *lockedGroups) set(owner string, groups ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.m[owner] = groups
}

// accessOf lets an owner use a template only while allowed says so.
type accessOf struct {
	mu      sync.Mutex
	allowed map[string]bool
}

func (a *accessOf) MayUse(_ context.Context, owner string, _ *model.Template) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.allowed[owner], nil
}

// accessFixture is a reconciler over its own store and namespace, run one
// reconcile at a time, whose group membership and template access the test
// changes; frank is in group research, which has a ready folder.
type accessFixture struct {
	s      *store.Store
	ns     string
	r      *Reconciler
	groups *lockedGroups
	access *accessOf
}

func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	fx := &accessFixture{
		s: storetest.New(t), ns: newNamespace(t, "javo-access", nil),
		groups: &lockedGroups{m: map[string][]string{"frank": {"research"}}},
		access: &accessOf{allowed: map[string]bool{"frank": true}},
	}
	f, err := fx.s.SharedFolders().Create(bg, actor, model.SharedFolder{Namespace: fx.ns, Name: "research", Spec: model.SharedFolderSpec{Group: "research"}})
	if err != nil {
		t.Fatal(err)
	}
	f.Status = model.SharedFolderStatus{ClaimName: "folder-research", GID: 5001, Prepared: true,
		Conditions: []metav1.Condition{{Type: model.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Now()}}}
	if _, err := fx.s.SharedFolders().Update(bg, f, f.Version); err != nil {
		t.Fatal(err)
	}
	fx.r = newRec(fx.s, func(r *Reconciler) {
		st := testStorage(fx.s)
		st.Groups = fx.groups
		r.Storage, r.Access = st, fx.access
	})
	return fx
}

func (fx *accessFixture) pod(t *testing.T, app model.App) *corev1.Pod {
	t.Helper()
	p := &corev1.Pod{}
	if err := k8s.Get(bg, types.NamespacedName{Namespace: fx.ns, Name: app.ObjectName()}, p); err != nil {
		return nil
	}
	return p
}

// settle reconciles until the app's pod exists again (a reconcile that finds
// an outdated pod only deletes it) and returns it.
func (fx *accessFixture) settle(t *testing.T, app model.App) *corev1.Pod {
	t.Helper()
	for range 3 {
		reconcileNow(t, fx.r, app.ID)
	}
	return fx.pod(t, app)
}

func podMount(p *corev1.Pod, path string) (corev1.VolumeMount, bool) {
	i := slices.IndexFunc(p.Spec.Containers[0].VolumeMounts, func(m corev1.VolumeMount) bool { return m.MountPath == path })
	if i < 0 {
		return corev1.VolumeMount{}, false
	}
	return p.Spec.Containers[0].VolumeMounts[i], true
}

// A person who leaves a group loses its folder in the apps already running:
// the pod is replaced without it. Before, only a size, command, token or
// secrets change replaced a pod, so the folder stayed until a restart.
func TestLeavingAGroupReplacesTheAppPodWithoutItsFolder(t *testing.T) {
	t.Parallel()
	fx := newAccessFixture(t)
	putTemplateIn(t, fx.s, "lab-groups")
	app := newAppIn(t, fx.s, fx.ns, "ws-groups", "frank", "lab-groups")
	before := fx.settle(t, app)
	if before == nil {
		t.Fatal("no pod")
	}
	if _, ok := podMount(before, "/shared/research"); !ok {
		t.Fatalf("mounts %+v", before.Spec.Containers[0].VolumeMounts)
	}
	fx.groups.set("frank")
	after := fx.settle(t, app)
	if after == nil || after.UID == before.UID {
		t.Fatal("the pod was not replaced")
	}
	if _, ok := podMount(after, "/shared/research"); ok || len(after.Spec.SecurityContext.SupplementalGroups) != 0 {
		t.Fatalf("new pod still has the folder: %+v %v", after.Spec.Containers[0].VolumeMounts, after.Spec.SecurityContext.SupplementalGroups)
	}
	if again := fx.settle(t, app); again == nil || again.UID != after.UID {
		t.Fatal("the pod was replaced again without a change")
	}
}

// Sharing an app replaces its pod with one that sees the home read-only.
func TestSharingAnAppReplacesItsPodWithAReadOnlyHome(t *testing.T) {
	t.Parallel()
	fx := newAccessFixture(t)
	putTemplateIn(t, fx.s, "lab-share")
	app := newAppIn(t, fx.s, fx.ns, "ws-share", "frank", "lab-share")
	before := fx.settle(t, app)
	if m, ok := podMount(before, "/home/user"); !ok || m.ReadOnly {
		t.Fatalf("private app home mount %+v", m)
	}
	editApp(t, fx.s, app.ID, func(a *model.App) { a.Sharing = model.Sharing{Access: model.AccessPublic} })
	after := fx.settle(t, app)
	if after == nil || after.UID == before.UID {
		t.Fatal("the pod was not replaced")
	}
	if m, ok := podMount(after, "/home/user"); !ok || !m.ReadOnly {
		t.Fatalf("shared app home mount %+v", m)
	}
}

// A template change (a new image, a security fix) reaches the apps already
// running on it.
func TestATemplateChangeReplacesTheAppPod(t *testing.T) {
	t.Parallel()
	fx := newAccessFixture(t)
	putTemplateIn(t, fx.s, "lab-image")
	app := newAppIn(t, fx.s, fx.ns, "ws-image", "frank", "lab-image")
	before := fx.settle(t, app)
	tpl, err := fx.s.Templates().Get(bg, "lab-image")
	if err != nil {
		t.Fatal(err)
	}
	tpl.Spec.Image = "example.com/lab:2"
	if _, err := fx.s.Templates().Put(bg, actor, tpl, tpl.Version); err != nil {
		t.Fatal(err)
	}
	after := fx.settle(t, app)
	if after == nil || after.UID == before.UID || after.Spec.Containers[0].Image != "example.com/lab:2" {
		t.Fatal("the pod was not replaced with one on the new image")
	}
}

// An owner who may no longer use the template (its allowed groups changed,
// or they left the group) loses the running app: the pod goes and the app
// says why. The check the API makes at create is made at every reconcile.
func TestAnAppStopsWhenItsOwnerLosesTheTemplate(t *testing.T) {
	t.Parallel()
	fx := newAccessFixture(t)
	putTemplateIn(t, fx.s, "lab-limited")
	app := newAppIn(t, fx.s, fx.ns, "ws-limited", "frank", "lab-limited")
	if fx.settle(t, app) == nil {
		t.Fatal("no pod")
	}
	fx.access.mu.Lock()
	fx.access.allowed["frank"] = false
	fx.access.mu.Unlock()
	if p := fx.settle(t, app); p != nil {
		t.Fatal("the pod still runs")
	}
	got := mustApp(t, fx.s, app.ID)
	if c := condition(got, model.ConditionScheduled); c == nil || c.Reason != string(reasons.TemplateNotAllowed) || got.Status.Phase != "Failed" {
		t.Fatalf("phase %s, Scheduled %+v", got.Status.Phase, c)
	}
}

// A pod built before the layout was recorded is replaced once, so the rules
// above hold for apps that were running at the upgrade.
func TestAPodWithoutALayoutRecordIsReplacedOnce(t *testing.T) {
	t.Parallel()
	fx := newAccessFixture(t)
	putTemplateIn(t, fx.s, "lab-old")
	app := newAppIn(t, fx.s, fx.ns, "ws-old", "frank", "lab-old")
	before := fx.settle(t, app)
	delete(before.Annotations, model.AnnotationLayout)
	if err := k8s.Update(bg, before); err != nil {
		t.Fatal(err)
	}
	after := fx.settle(t, app)
	if after == nil || after.UID == before.UID {
		t.Fatal("the old pod was kept")
	}
}

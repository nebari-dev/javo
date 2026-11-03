package storage

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

func class(t *testing.T, name, provisioner string, isDefault bool, params map[string]string) {
	t.Helper()
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Provisioner: provisioner, Parameters: params}
	if isDefault {
		sc.Annotations = map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}
	}
	must(t, k8s.Create(ctx, sc))
}

func node(t *testing.T, name string) {
	t.Helper()
	must(t, k8s.Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}))
}

// perUserHome makes a perUser home; onOneNode marks it as made on a
// one-node cluster, the way home creation does.
func perUserHome(t *testing.T, owner string, onOneNode bool) {
	t.Helper()
	_, err := db.Homes().Ensure(ctx, "default", owner, nil)
	must(t, err)
	editHome(t, "default", owner, func(h *model.Home) {
		h.Status.Mode, h.Status.CreatedOnSingleNode = model.HomeModePerUser, onOneNode
	})
}

func run(t *testing.T, r *ClusterReconciler) {
	t.Helper()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "default"}}); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T) *model.ClusterStorage { return storageNow(t) }

func newReconciler() *ClusterReconciler {
	return &ClusterReconciler{Client: k8s, Reader: k8s, Rows: testRows()}
}

func cond(t *testing.T, cs *model.ClusterStorage, typ string) *metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(cs.Status.Conditions, typ)
	if c == nil {
		t.Fatalf("no %s condition in %+v", typ, cs.Status.Conditions)
	}
	return c
}

func TestFillsClasses(t *testing.T) {
	clean(t)
	class(t, "standard", "rancher.io/local-path", true, nil)
	class(t, "longhorn", "driver.longhorn.io", false, nil)
	node(t, "n1")
	run(t, newReconciler())

	cs := get(t)
	if len(cs.Status.Classes) != 2 {
		t.Fatalf("classes = %+v, want 2", cs.Status.Classes)
	}
	// envtest has no snapshot CRD, which means no snapshot support.
	for _, c := range cs.Status.Classes {
		if c.Snapshots != (model.Fact{Value: false, Provenance: model.ProvenanceKnown}) {
			t.Errorf("%s snapshots = %+v, want false/known", c.Name, c.Snapshots)
		}
	}
	if c := cond(t, cs, model.ConditionHomeStorageReady); c.Status != metav1.ConditionTrue {
		t.Errorf("HomeStorageReady = %+v, want True", c)
	}
	if cs.Status.Nodes != 1 {
		t.Errorf("nodes = %d, want 1", cs.Status.Nodes)
	}
	ch := cs.Status.Choice
	if ch == nil || ch.Mode != model.HomeModeShared || ch.HomeClass != "longhorn" || ch.Source != "auto" {
		t.Errorf("choice = %+v, want shared on longhorn, auto", ch)
	}
}

func TestNoStorageClass(t *testing.T) {
	clean(t)
	node(t, "n1")
	run(t, newReconciler())
	c := cond(t, get(t), model.ConditionHomeStorageReady)
	if c.Status != metav1.ConditionFalse || c.Reason != string(reasons.NoStorageClass) {
		t.Errorf("HomeStorageReady = %+v, want False/%s", c, reasons.NoStorageClass)
	}
}

func TestNoDefaultClassToChooseFrom(t *testing.T) {
	clean(t)
	class(t, "fast", "ebs.csi.aws.com", false, nil)
	run(t, newReconciler())
	c := cond(t, get(t), model.ConditionHomeStorageReady)
	if c.Status != metav1.ConditionFalse || c.Reason != string(reasons.NoDefaultStorageClass) {
		t.Errorf("HomeStorageReady = %+v, want False/%s", c, string(reasons.NoDefaultStorageClass))
	}
}

func TestSharedOverrideWithoutAnRWXClass(t *testing.T) {
	clean(t)
	class(t, "standard", "rancher.io/local-path", true, nil)
	run(t, newReconciler())
	cfg.Home.Mode = model.HomeModeShared
	run(t, newReconciler())

	cs := get(t)
	if cs.Status.Choice.Mode != model.HomeModeShared || cs.Status.Choice.Source != "override" {
		t.Errorf("choice = %+v, want the admin's shared", cs.Status.Choice)
	}
	c := cond(t, cs, model.ConditionHomeStorageReady)
	if c.Status != metav1.ConditionFalse || c.Reason != string(reasons.OverrideNotSupported) {
		t.Errorf("HomeStorageReady = %+v, want False/%s", c, string(reasons.OverrideNotSupported))
	}
}

func TestSharedOverrideKeepsTheNamedClassThatCannotShare(t *testing.T) {
	clean(t)
	class(t, "hcloud-volumes", "csi.hetzner.cloud", true, nil)
	class(t, "longhorn", "driver.longhorn.io", false, nil)
	run(t, newReconciler())
	cfg.Home.Mode = model.HomeModeShared
	cfg.Home.StorageClassName = "hcloud-volumes"
	run(t, newReconciler())

	cs := get(t)
	if cs.Status.Choice.HomeClass != "hcloud-volumes" {
		t.Errorf("HomeClass = %q, want the admin's hcloud-volumes", cs.Status.Choice.HomeClass)
	}
	c := cond(t, cs, model.ConditionHomeStorageReady)
	if c.Status != metav1.ConditionFalse || c.Reason != string(reasons.OverrideNotSupported) {
		t.Errorf("HomeStorageReady = %+v, want False/%s", c, string(reasons.OverrideNotSupported))
	}
	if !strings.Contains(c.Message, "hcloud-volumes") {
		t.Errorf("message %q does not name the class", c.Message)
	}
}

func TestSecondNodeAfterPerUserHomesOnOneNode(t *testing.T) {
	clean(t)
	class(t, "standard", "rancher.io/local-path", true, nil)
	node(t, "n1")
	r := newReconciler()
	run(t, r)
	cs := get(t)
	if !cs.Status.Choice.SingleNode {
		t.Fatalf("choice = %+v, want singleNode", cs.Status.Choice)
	}
	perUserHome(t, "alice", true)
	run(t, r)
	if c := cond(t, get(t), model.ConditionSingleNodeAssumption); c.Status != metav1.ConditionFalse {
		t.Fatalf("SingleNodeAssumption = %+v on one node, want False", c)
	}

	node(t, "n2")
	run(t, r)
	cs = get(t)
	if cs.Status.Nodes != 2 {
		t.Errorf("nodes = %d, want 2", cs.Status.Nodes)
	}
	if c := cond(t, cs, model.ConditionSingleNodeAssumption); c.Status != metav1.ConditionTrue || c.Reason != string(reasons.SecondNodeJoined) {
		t.Errorf("SingleNodeAssumption = %+v, want True/%s", c, reasons.SecondNodeJoined)
	}

	// The flag stays up on later reconciles, when the choice no longer says singleNode.
	run(t, r)
	if c := cond(t, get(t), model.ConditionSingleNodeAssumption); c.Status != metav1.ConditionTrue {
		t.Errorf("SingleNodeAssumption = %+v after another reconcile, want True", c)
	}
}

// Nothing survives in the findings: rebuilt findings, or a second node that
// joins before the first write, still raise the flag because the fact lives
// on the homes.
func TestSingleNodeAssumptionIsRebuiltFromHomesWithoutPreviousStatus(t *testing.T) {
	clean(t)
	class(t, "standard", "rancher.io/local-path", true, nil)
	node(t, "n1")
	node(t, "n2")
	perUserHome(t, "alice", true)
	run(t, newReconciler())
	c := cond(t, get(t), model.ConditionSingleNodeAssumption)
	if c.Status != metav1.ConditionTrue || c.Reason != string(reasons.SecondNodeJoined) {
		t.Fatalf("SingleNodeAssumption = %+v on first reconcile, want True/%s", c, reasons.SecondNodeJoined)
	}

	must(t, db.Storage().SetClusterStatus(ctx, model.ClusterStorageStatus{}))
	run(t, newReconciler())
	if c := cond(t, get(t), model.ConditionSingleNodeAssumption); c.Status != metav1.ConditionTrue {
		t.Errorf("SingleNodeAssumption = %+v after the findings were dropped, want True", c)
	}
}

func TestReasonsAndMessagesComeFromTheCatalog(t *testing.T) {
	clean(t)
	node(t, "n1")
	run(t, newReconciler())
	c := cond(t, get(t), model.ConditionHomeStorageReady)
	r, ok := reasons.Lookup(reasons.NoStorageClass)
	if !ok || c.Reason != string(reasons.NoStorageClass) || !strings.HasPrefix(c.Message, r.What) {
		t.Errorf("condition = %+v, want reason NoStorageClass and message starting with the catalog text %q", c, r.What)
	}
}

func TestNamedStorageClassThatDoesNotExist(t *testing.T) {
	clean(t)
	class(t, "standard", "rancher.io/local-path", true, nil)
	node(t, "n1")
	run(t, newReconciler())
	cfg.Home.StorageClassName = "foo"
	run(t, newReconciler())

	c := cond(t, get(t), model.ConditionHomeStorageReady)
	if c.Status != metav1.ConditionFalse || c.Reason != string(reasons.NamedStorageClassMissing) {
		t.Fatalf("HomeStorageReady = %+v, want False/NamedStorageClassMissing", c)
	}
	if !strings.Contains(c.Message, "foo") {
		t.Errorf("message %q does not name the missing class", c.Message)
	}
}

func TestOnlyNodeCreateDeleteAndLabelChangesTriggerAReconcile(t *testing.T) {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{"a": "b"}}}
	statusOnly := n.DeepCopy()
	statusOnly.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady}}
	relabelled := n.DeepCopy()
	relabelled.Labels["topology.kubernetes.io/zone"] = "z"
	p := nodeChanges
	if !p.Create(event.CreateEvent{Object: n}) || !p.Delete(event.DeleteEvent{Object: n}) {
		t.Error("node create and delete must enqueue")
	}
	if p.Update(event.UpdateEvent{ObjectOld: n, ObjectNew: statusOnly}) {
		t.Error("a node status update must not enqueue")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: n, ObjectNew: relabelled}) {
		t.Error("a node label change must enqueue")
	}
}

func TestMultiNodeClusterFromTheStartIsNotASingleNodeAssumption(t *testing.T) {
	clean(t)
	class(t, "standard", "rancher.io/local-path", true, nil)
	node(t, "n1")
	node(t, "n2")
	perUserHome(t, "alice", false)
	run(t, newReconciler())
	if c := cond(t, get(t), model.ConditionSingleNodeAssumption); c.Status != metav1.ConditionFalse {
		t.Errorf("SingleNodeAssumption = %+v, want False", c)
	}
}

func TestNoWriteWhenNothingChanged(t *testing.T) {
	clean(t)
	class(t, "standard", "rancher.io/local-path", true, nil)
	node(t, "n1")
	r := newReconciler()
	run(t, r)
	observedAt := func() time.Time {
		f, err := db.Facts().Get(ctx, "storage")
		must(t, err)
		return f.ObservedAt
	}
	first := observedAt()
	time.Sleep(5 * time.Millisecond)
	run(t, r)
	run(t, r)
	if got := observedAt(); !got.Equal(first) {
		t.Errorf("findings written again at %s (first %s) with nothing changed", got, first)
	}
}

// A home on a mode other than the chosen one is reported, and nothing about
// it changes: moving it is the admin's decision (see migration_test.go).
func TestHomesOnAnotherModeRaiseTheMismatchAndAreLeftAlone(t *testing.T) {
	clean(t)
	class(t, "standard", "rancher.io/local-path", true, nil)
	node(t, "n1")
	r := newReconciler()
	run(t, r)
	if c := cond(t, get(t), model.ConditionHomeModeMismatch); c.Status != metav1.ConditionFalse {
		t.Fatalf("HomeModeMismatch = %+v with no homes, want False", c)
	}

	perUserHome(t, "alice", false)
	size := resource.MustParse("5Gi")
	_, err := db.Homes().Ensure(ctx, "default", "bob", &size)
	must(t, err)
	editHome(t, "default", "bob", func(h *model.Home) { h.Status.Mode = model.HomeModeShared })
	run(t, r)

	cs := get(t)
	m := cs.Status.Migration
	if m == nil || m.Users != 1 || m.From != model.HomeModeShared || m.To != model.HomeModePerUser || m.RequestedBytes != 5<<30 {
		t.Fatalf("status.migration = %+v, want 1 user from shared to perUser, 5Gi", m)
	}
	if c := cond(t, cs, model.ConditionHomeModeMismatch); c.Status != metav1.ConditionTrue || c.Reason != string(reasons.HomeModeMismatch) {
		t.Errorf("HomeModeMismatch = %+v, want True/%s", c, reasons.HomeModeMismatch)
	}
	got := homeOf(t, "default", "bob")
	if got.Status.Migration != nil || got.Status.Mode != model.HomeModeShared {
		t.Errorf("the mismatch alone changed the home: %+v", got.Status)
	}
}

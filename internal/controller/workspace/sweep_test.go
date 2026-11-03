package workspace

import (
	"context"

	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
)

// An app's Pod and Service whose row is gone are deleted. A Pod of a live
// app, a Pod with no app label (a job run, a file pod, anyone else's) and a
// Pod in a namespace the controller does not manage are never touched.
func TestSweepDeletesOrphansOnly(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-sweep")
	live := fx.newApp(t, "live", "alice", "lab-sweep")
	stray := newNamespace(t, "javo-sweep-stray", nil)

	pod := func(ns, name string, labels map[string]string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example.com/lab:1"}}},
		}
	}
	svc := func(name string, labels map[string]string) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: fx.ns, Name: name, Labels: labels},
			Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
		}
	}
	orphan := map[string]string{model.LabelApp: "no-such-row", model.LabelOwner: "alice"}
	objs := map[string]client.Object{
		"orphan pod":         pod(fx.ns, "sweep-orphan", orphan),
		"orphan service":     svc("sweep-orphan", orphan),
		"live pod":           pod(fx.ns, "sweep-live", map[string]string{model.LabelApp: live.ID, model.LabelOwner: "alice"}),
		"job run pod":        pod(fx.ns, "sweep-job", map[string]string{model.LabelJob: "job-1", model.LabelOwner: "alice"}),
		"pod without labels": pod(fx.ns, "sweep-plain", nil),
		"unmanaged orphan":   pod(stray, "sweep-orphan", orphan),
	}
	for what, o := range objs {
		if err := k8s.Create(bg, o); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	// The sweep that must leave unmanaged namespaces alone reads only the
	// managed ones; the other sweeps everything the cache shows. Both see
	// this test's namespaces only: other tests' apps have rows in other
	// databases, so their objects would be orphans here.
	mine := onlyIn{Client: k8s, namespaces: []string{fx.ns, stray}}
	managedOnly := newRec(fx.db, func(r *Reconciler) { r.Client, r.ManagedNamespacesOnly = mine, true })
	all := newRec(fx.db, func(r *Reconciler) { r.Client = mine })

	if err := managedOnly.Sweep(bg); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Get(bg, client.ObjectKeyFromObject(objs["unmanaged orphan"]), &corev1.Pod{}); err != nil {
		t.Fatalf("a pod in a namespace the controller does not manage was deleted: %v", err)
	}
	if err := all.Sweep(bg); err != nil {
		t.Fatal(err)
	}
	for _, what := range []string{"orphan pod", "orphan service", "unmanaged orphan"} {
		if err := k8s.Get(bg, client.ObjectKeyFromObject(objs[what]), objs[what]); err == nil && objs[what].GetDeletionTimestamp() == nil {
			t.Errorf("%s survived the sweep", what)
		}
	}
	for _, what := range []string{"live pod", "job run pod", "pod without labels"} {
		if err := k8s.Get(bg, client.ObjectKeyFromObject(objs[what]), objs[what]); err != nil || objs[what].GetDeletionTimestamp() != nil {
			t.Errorf("%s was touched: %v", what, err)
		}
	}
}

// onlyIn is a client whose lists hold the objects of the given namespaces
// only, as if nothing else were in the cluster.
type onlyIn struct {
	client.Client
	namespaces []string
}

func (c onlyIn) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	var items []runtime.Object
	for _, ns := range c.namespaces {
		l := list.DeepCopyObject().(client.ObjectList)
		if err := c.Client.List(ctx, l, append(opts, client.InNamespace(ns))...); err != nil {
			return err
		}
		got, err := meta.ExtractList(l)
		if err != nil {
			return err
		}
		items = append(items, got...)
	}
	return meta.SetList(list, items)
}

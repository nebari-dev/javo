package workspace

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
)

// An app's Pod and Service whose row is gone are deleted. A Pod of a live
// app, a Pod with no app label (a job run, a file pod, anyone else's) and a
// Pod in a namespace the controller does not manage are never touched.
func TestSweepDeletesOrphansOnly(t *testing.T) {
	// The sweep lists every labelled object, so it reads the database the
	// other tests' apps are in: their Pods are live, not orphans.
	newTemplate(t, "lab-sweep")
	live := newApp(t, "live", "alice", "lab-sweep")
	if err := k8s.Create(bg, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "javo-sweep-stray"}}); err != nil {
		t.Fatal(err)
	}

	pod := func(ns, name string, labels map[string]string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example.com/lab:1"}}},
		}
	}
	svc := func(name string, labels map[string]string) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: name, Labels: labels},
			Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
		}
	}
	orphan := map[string]string{model.LabelApp: "no-such-row", model.LabelOwner: "alice"}
	objs := map[string]client.Object{
		"orphan pod":         pod(testNS, "sweep-orphan", orphan),
		"orphan service":     svc("sweep-orphan", orphan),
		"live pod":           pod(testNS, "sweep-live", map[string]string{model.LabelApp: live.ID, model.LabelOwner: "alice"}),
		"job run pod":        pod(testNS, "sweep-job", map[string]string{model.LabelJob: "job-1", model.LabelOwner: "alice"}),
		"pod without labels": pod(testNS, "sweep-plain", nil),
		"unmanaged orphan":   pod("javo-sweep-stray", "sweep-orphan", orphan),
	}
	for what, o := range objs {
		if err := k8s.Create(bg, o); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	// The sweep that must leave unmanaged namespaces alone reads only the
	// managed ones; the other sweeps everything the cache shows.
	managedOnly := newRec(db, func(r *Reconciler) { r.ManagedNamespacesOnly = true })
	all := newRec(db)

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

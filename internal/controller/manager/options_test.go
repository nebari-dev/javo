package manager

import (
	"reflect"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
)

// Only the elected leader may run reconcilers: the uid allocator keeps its
// assigned set in memory, so two active controllers could give one uid to
// two people.
func TestOptionsElectLeader(t *testing.T) {
	o := Options(runtime.NewScheme(), []string{"javo-apps"}, ":1", ":2")
	if !o.LeaderElection || o.LeaderElectionID == "" {
		t.Fatalf("LeaderElection=%v ID=%q, want election on with an ID", o.LeaderElection, o.LeaderElectionID)
	}
	if o.LeaderElectionResourceLock != "leases" {
		t.Fatalf("lock = %q, want leases", o.LeaderElectionResourceLock)
	}
	if o.Cache.DefaultNamespaces["javo-apps"].LabelSelector != nil || len(o.Cache.DefaultNamespaces) != 1 {
		t.Fatalf("cache namespaces = %v, want only javo-apps", o.Cache.DefaultNamespaces)
	}
}

// Per-user and per-team modes put apps in many namespaces, so the cache
// must not be tied to one.
func TestOptionsWithoutNamespacesCacheEverywhere(t *testing.T) {
	o := Options(runtime.NewScheme(), nil, ":1", ":2")
	if len(o.Cache.DefaultNamespaces) != 0 {
		t.Fatalf("cache namespaces = %v, want all", o.Cache.DefaultNamespaces)
	}
}

// The controller reads every pod, service, claim and event it caches, so in
// a cluster-wide cache it must only hold what it builds: other workloads'
// objects would cost memory and are none of its business.
func TestCacheHoldsOnlyWhatTheControllerBuilds(t *testing.T) {
	o := Options(runtime.NewScheme(), nil, ":1", ":2")
	ours := labels.Set{model.LabelApp: "app-1", model.LabelOwner: "alice"}
	theirs := labels.Set{"app": "web"}
	for _, obj := range []client.Object{&corev1.Pod{}, &corev1.Service{}, &corev1.PersistentVolumeClaim{}} {
		sel := byType(o, obj).Label
		if sel == nil || !sel.Matches(ours) || sel.Matches(theirs) {
			t.Errorf("%T selector = %v, want workspace objects only", obj, sel)
		}
	}
	managed := labels.Set{model.LabelManaged: "true"}
	for _, obj := range []client.Object{&corev1.Namespace{}, &networkingv1.NetworkPolicy{}} {
		sel := byType(o, obj).Label
		if sel == nil || !sel.Matches(managed) || sel.Matches(theirs) {
			t.Errorf("%T selector = %v, want managed objects only", obj, sel)
		}
	}
	// A job run's pod has the owner and job labels but no app label;
	// without it the controller cannot see how the run ended.
	jobPod := labels.Set{model.LabelOwner: "alice", model.LabelJob: "job-1", "batch.kubernetes.io/job-name": "alice-nightly-x1"}
	if sel := byType(o, &corev1.Pod{}).Label; sel == nil || !sel.Matches(jobPod) {
		t.Errorf("pod selector = %v, want job run pods too", sel)
	}
	ownJob := labels.Set{model.LabelJob: "job-1", model.LabelOwner: "alice"}
	for _, obj := range []client.Object{&batchv1.Job{}, &batchv1.CronJob{}} {
		sel := byType(o, obj).Label
		if sel == nil || !sel.Matches(ownJob) || sel.Matches(theirs) {
			t.Errorf("%T selector = %v, want jobs' objects only", obj, sel)
		}
	}
	if f := byType(o, &corev1.Event{}).Field; f == nil || !f.Matches(fields.Set{"involvedObject.kind": "Pod"}) || f.Matches(fields.Set{"involvedObject.kind": "Node"}) {
		t.Errorf("event selector = %v, want pod events only", f)
	}
}

// byType finds the cache rules for the kind of want; the map is keyed by
// object pointers.
func byType(o ctrl.Options, want client.Object) cache.ByObject {
	for obj, rules := range o.Cache.ByObject {
		if reflect.TypeOf(obj) == reflect.TypeOf(want) {
			return rules
		}
	}
	return cache.ByObject{}
}

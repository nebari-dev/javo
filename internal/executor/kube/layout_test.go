package kube

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
)

func laidOut(in podstorage.Input) *corev1.Pod {
	pod := appPod()
	ApplyLayout(pod, &pod.Spec.Containers[0], "alice", podstorage.Build("alice", in))
	return pod
}

func TestCoLocationNeedsTheOwnersOtherPodsOnTheSameNode(t *testing.T) {
	aff := laidOut(podstorage.Input{CoLocate: true}).Spec.Affinity
	if aff == nil || aff.PodAffinity == nil || len(aff.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution) != 1 {
		t.Fatalf("affinity = %+v", aff)
	}
	term := aff.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0]
	if term.TopologyKey != corev1.LabelHostname || term.LabelSelector.MatchLabels[model.LabelOwner] != "alice" {
		t.Errorf("term = %+v", term)
	}
	if got := laidOut(podstorage.Input{}).Spec.Affinity; got != nil {
		t.Errorf("shared mode must not constrain placement, affinity = %+v", got)
	}
}

func TestScratchIsNodeLocalAndSafeToEvict(t *testing.T) {
	pod := laidOut(podstorage.Input{ScratchMax: resource.MustParse("20Gi")})
	if got := pod.Annotations[SafeToEvictAnnotation]; got != "scratch,tmp" {
		t.Errorf("safe-to-evict = %q", got)
	}
	for _, v := range pod.Spec.Volumes {
		if v.Name != "scratch" && v.Name != "tmp" {
			continue
		}
		if v.EmptyDir == nil || v.EmptyDir.SizeLimit == nil || v.EmptyDir.SizeLimit.Cmp(resource.MustParse("20Gi")) != 0 {
			t.Errorf("%s volume = %+v", v.Name, v)
		}
	}
}

// One person's pod cannot fill the node's disk (and with it the database and
// everyone else's pods): the app container has a limit on all it writes to
// node-local disk. The request stays zero so the limit does not reserve disk
// on a small node.
func TestTheContainersDiskIsLimited(t *testing.T) {
	r := laidOut(podstorage.Input{DiskMax: resource.MustParse("30Gi")}).Spec.Containers[0].Resources
	if lim := r.Limits[corev1.ResourceEphemeralStorage]; lim.Cmp(resource.MustParse("30Gi")) != 0 {
		t.Fatalf("ephemeral-storage limit = %s", lim.String())
	}
	if req, ok := r.Requests[corev1.ResourceEphemeralStorage]; !ok || !req.IsZero() {
		t.Fatalf("ephemeral-storage request = %v (set %v), want an explicit 0", req.String(), ok)
	}
}

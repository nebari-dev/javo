package nodes

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// Watch reads the nodes from a cache that holds only their allocatable, so
// a node's image list takes no memory, and follows a node's changes.
func TestWatchFollowsTheNodesTrimmed(t *testing.T) {
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	live, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}

	machines, err := Watch(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cl, err := machines.Cluster(ctx); err != nil || cl.Known() {
		t.Fatalf("no nodes yet: %v, %v", cl, err)
	}

	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status:     corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: q("8"), corev1.ResourceMemory: q("16Gi")}},
	}
	status := n.Status
	n.Status = corev1.NodeStatus{}
	if err := live.Create(ctx, n); err != nil {
		t.Fatal(err)
	}
	n.Status = status
	n.Status.Images = []corev1.ContainerImage{{Names: []string{"example.org/big:1"}, SizeBytes: 1 << 30}}
	if err := live.Status().Update(ctx, n); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		cl, err := machines.Cluster(ctx)
		if err != nil {
			t.Fatalf("nodes from the cache: %v", err)
		}
		if cl.Known() && !cl.Fits(q("9"), q("1Gi")) && cl.Fits(q("8"), q("16Gi")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cache never read the node's allocatable: %v", cl)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var cached corev1.Node
	if err := machines.Reader.Get(ctx, client.ObjectKey{Name: "node-1"}, &cached); err != nil || len(cached.Status.Images) != 0 {
		t.Fatalf("cached node keeps its image list (%v): %+v", err, cached.Status.Images)
	}
	var pods corev1.PodList
	if err := machines.Reader.List(ctx, &pods); err == nil {
		t.Fatal("the node cache also reads pods")
	}
}

func q(s string) resource.Quantity { return resource.MustParse(s) }

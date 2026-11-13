package capacity_test

import (
	"context"
	"testing"

	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/capacity/nodes"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func node(name, cpu, mem string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem),
		}},
	}
}

func clusterOf(t *testing.T, objs ...client.Object) capacity.Cluster {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c, err := nodes.Source{Reader: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()}.Cluster(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func q(s string) resource.Quantity { return resource.MustParse(s) }

// One node with 8 CPU and about 15.6 GiB allocatable.
func TestOneNodeFitsWhatItHasRoomFor(t *testing.T) {
	c := clusterOf(t, node("n1", "8", "16365140Ki"))
	if !c.Fits(q("4"), q("8Gi")) {
		t.Error("medium (4 CPU, 8Gi) should fit")
	}
	if !c.Fits(q("8"), q("15Gi")) {
		t.Error("exactly the node's CPU should fit")
	}
	if c.Fits(q("8"), q("32Gi")) {
		t.Error("large (8 CPU, 32Gi) should not fit")
	}
	if c.Fits(q("9"), q("1Gi")) {
		t.Error("more CPU than the node should not fit")
	}
	if cpu, mem := c.Largest(); cpu != "8" || mem != "15.6 GB" {
		t.Errorf("largest = %s CPU, %s", cpu, mem)
	}
}

// A size fits only when one machine has both the CPU and the memory: the
// CPU of one and the memory of another do not add up.
func TestManyNodesNeedOneWithBoth(t *testing.T) {
	c := clusterOf(t, node("cpu-heavy", "32", "8Gi"), node("mem-heavy", "4", "64Gi"))
	if c.Fits(q("16"), q("32Gi")) {
		t.Error("16 CPU and 32Gi fits on neither node")
	}
	if !c.Fits(q("16"), q("8Gi")) || !c.Fits(q("4"), q("64Gi")) {
		t.Error("each node's own shape should fit")
	}
	if cpu, mem := c.Largest(); cpu != "4" || mem != "64 GB" {
		t.Errorf("largest (most memory) = %s CPU, %s", cpu, mem)
	}
}

// With no nodes to read (cache empty, no rights to list them) nothing is
// known, so everything fits and the scheduler has the last word.
func TestNoNodesIsUnknownAndEverythingFits(t *testing.T) {
	c := clusterOf(t)
	if c.Known() || !c.Fits(q("1000"), q("10Ti")) {
		t.Error("an empty cluster should fit anything")
	}
}

// A pod's request is what its containers ask for together, or its biggest
// init container if that is more.
func TestPodRequests(t *testing.T) {
	p := &corev1.Pod{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceCPU: q("6"), corev1.ResourceMemory: q("1Gi")}}}},
		Containers: []corev1.Container{
			{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: q("4"), corev1.ResourceMemory: q("8Gi")}}},
			{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: q("1"), corev1.ResourceMemory: q("1Gi")}}},
		},
	}}
	cpu, mem := capacity.PodRequests(p)
	if cpu.Cmp(q("6")) != 0 || mem.Cmp(q("9Gi")) != 0 {
		t.Fatalf("requests = %s, %s", cpu.String(), mem.String())
	}
}

// The caches keep only what Cluster reads (allocatable and labels), so a
// node's image list and conditions take no memory; the machine reads the same.
func TestTrimKeepsOnlyAllocatable(t *testing.T) {
	n := node("n1", "8", "16Gi")
	n.Labels = map[string]string{"a": "b"}
	n.Status.Images = []corev1.ContainerImage{{Names: []string{"example.org/big:1"}, SizeBytes: 1 << 30}}
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	got, err := nodes.Trim(n)
	if err != nil {
		t.Fatal(err)
	}
	tn := got.(*corev1.Node)
	if tn.Name != "n1" || len(tn.Status.Images) != 0 || len(tn.Status.Conditions) != 0 || tn.Labels["a"] != "b" {
		t.Fatalf("trimmed node %+v", tn)
	}
	if c := clusterOf(t, tn); !c.Fits(q("8"), q("16Gi")) || c.Fits(q("9"), q("1Gi")) {
		t.Fatal("trimmed node reads a different machine")
	}
}

// The controller's egress policy denies pods every node's addresses and pod
// ranges, read from the same cache: Trim keeps them.
func TestTrimKeepsTheNodesAddresses(t *testing.T) {
	n := node("n1", "8", "16Gi")
	n.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeExternalIP, Address: "203.0.113.7"}}
	n.Spec.PodCIDRs = []string{"198.51.100.0/24"}
	got, err := nodes.Trim(n)
	if err != nil {
		t.Fatal(err)
	}
	tn := got.(*corev1.Node)
	if len(tn.Status.Addresses) != 1 || tn.Status.Addresses[0].Address != "203.0.113.7" || len(tn.Spec.PodCIDRs) != 1 {
		t.Fatalf("trimmed node lost its addresses: %+v", tn)
	}
}

func gpuNode(name, cpu, mem, gpus string, labels map[string]string) *corev1.Node {
	n := node(name, cpu, mem)
	n.Labels = labels
	n.Status.Allocatable["nvidia.com/gpu"] = q(gpus)
	return n
}

// A GPU need fits only a machine with that many of the device, and with a
// node selector only a machine carrying those labels.
func TestHoldsCountsDevicesAndLabels(t *testing.T) {
	c := clusterOf(t, node("cpu", "64", "256Gi"),
		gpuNode("a100", "16", "64Gi", "2", map[string]string{"pool": "a100"}))
	gpus := func(n string) corev1.ResourceList { return corev1.ResourceList{"nvidia.com/gpu": q(n)} }
	if !c.Holds(capacity.Need{CPU: q("8"), Memory: q("32Gi"), Devices: gpus("2")}) {
		t.Error("2 GPUs fit the a100 node")
	}
	if c.Holds(capacity.Need{CPU: q("8"), Memory: q("32Gi"), Devices: gpus("4")}) {
		t.Error("4 GPUs fit no node")
	}
	if c.Holds(capacity.Need{CPU: q("32"), Memory: q("32Gi"), Devices: gpus("1")}) {
		t.Error("the CPU of one node and the GPU of another do not add up")
	}
	if c.Holds(capacity.Need{CPU: q("1"), Memory: q("1Gi"), Devices: corev1.ResourceList{"amd.com/gpu": q("1")}}) {
		t.Error("no node has amd.com/gpu")
	}
	if !c.Holds(capacity.Need{CPU: q("1"), Memory: q("1Gi"), NodeSelector: map[string]string{"pool": "a100"}}) {
		t.Error("the a100 node carries pool=a100")
	}
	if c.Holds(capacity.Need{CPU: q("32"), Memory: q("1Gi"), NodeSelector: map[string]string{"pool": "a100"}}) {
		t.Error("only the a100 node matches the selector, and it has 16 CPU")
	}
	if c.Holds(capacity.Need{CPU: q("1"), Memory: q("1Gi"), NodeSelector: map[string]string{"pool": "h100"}}) {
		t.Error("no node carries pool=h100")
	}
	if !(capacity.Cluster{}).Holds(capacity.Need{CPU: q("1"), Devices: gpus("8")}) {
		t.Error("an unknown cluster holds anything")
	}
}

// Trim keeps what Holds reads: the device counts and the labels.
func TestTrimKeepsLabelsForSelectors(t *testing.T) {
	got, err := nodes.Trim(gpuNode("g", "8", "16Gi", "1", map[string]string{"pool": "gpu"}))
	if err != nil {
		t.Fatal(err)
	}
	c := clusterOf(t, got.(*corev1.Node))
	if !c.Holds(capacity.Need{CPU: q("1"), Memory: q("1Gi"), Devices: corev1.ResourceList{"nvidia.com/gpu": q("1")}, NodeSelector: map[string]string{"pool": "gpu"}}) {
		t.Fatal("a trimmed GPU node no longer holds a GPU need")
	}
}

// A pod's need includes its extended resources and its node selector.
func TestPodNeedHasDevicesAndSelector(t *testing.T) {
	p := &corev1.Pod{Spec: corev1.PodSpec{
		NodeSelector: map[string]string{"pool": "gpu"},
		Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceCPU: q("4"), corev1.ResourceMemory: q("8Gi"), "nvidia.com/gpu": q("1")}}}},
	}}
	n := capacity.PodNeed(p)
	if n.CPU.Cmp(q("4")) != 0 || n.Memory.Cmp(q("8Gi")) != 0 || n.NodeSelector["pool"] != "gpu" {
		t.Fatalf("need %+v", n)
	}
	if g := n.Devices["nvidia.com/gpu"]; g.Cmp(q("1")) != 0 || len(n.Devices) != 1 {
		t.Fatalf("devices %v", n.Devices)
	}
}

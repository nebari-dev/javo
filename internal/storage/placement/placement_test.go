package placement_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/storage/placement"
)

func node(name, cpu, mem string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem),
		}},
	}
}

func pod(name, nodeName, owner, app, cpu, mem string) corev1.Pod {
	labels := map[string]string{}
	if owner != "" {
		labels[model.LabelOwner] = owner
	}
	if app != "" {
		labels[model.LabelApp] = app
	}
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec: corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{{
			Name: "c", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem),
			}},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func req(cpu, mem string) placement.Request {
	return placement.Request{CPU: resource.MustParse(cpu), Memory: resource.MustParse(mem)}
}

// alice has two apps on node-a, which has 8 CPUs of which another tenant's pod
// and her own apps use 3: 5 are free.
func cluster() ([]corev1.Node, []corev1.Pod) {
	nodes := []corev1.Node{node("node-a", "8", "32Gi"), node("node-b", "8", "32Gi")}
	pods := []corev1.Pod{
		pod("a1", "node-a", "alice", "sales", "1", "2Gi"),
		pod("a2", "node-a", "alice", "forecast", "1", "2Gi"),
		pod("b1", "node-a", "bob", "bobs", "1", "4Gi"),
		pod("b2", "node-b", "bob", "bobs-two", "2", "4Gi"),
	}
	return nodes, pods
}

func TestAppFitsNextToTheOwnersOtherApps(t *testing.T) {
	nodes, pods := cluster()
	got := placement.Preview(nodes, pods, "", "alice", req("1", "1Gi"))
	if !got.CoLocated || got.Node != "node-a" || !got.Fits {
		t.Fatalf("got %+v", got)
	}
	if got.FreeCPU.Cmp(resource.MustParse("5")) != 0 {
		t.Errorf("free cpu = %s, want 5", got.FreeCPU.String())
	}
	if got.FreeMemory.Cmp(resource.MustParse("24Gi")) != 0 {
		t.Errorf("free memory = %s, want 24Gi", got.FreeMemory.String())
	}
	if len(got.RunningApps) != 2 || got.RunningApps[0] != "forecast" || got.RunningApps[1] != "sales" {
		t.Errorf("running apps = %v", got.RunningApps)
	}
}

func TestAppTooBigForTheOwnersNodeDoesNotFit(t *testing.T) {
	nodes, pods := cluster()
	if got := placement.Preview(nodes, pods, "", "alice", req("6", "1Gi")); got.Fits || !got.CoLocated {
		t.Fatalf("6 CPUs: %+v", got)
	}
	if got := placement.Preview(nodes, pods, "", "alice", req("1", "25Gi")); got.Fits {
		t.Fatalf("25Gi: %+v", got)
	}
}

func TestOwnerWithoutAppsIsNotCoLocated(t *testing.T) {
	nodes, pods := cluster()
	got := placement.Preview(nodes, pods, "", "carol", req("1", "1Gi"))
	if got.CoLocated || got.Node != "" || len(got.RunningApps) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestFinishedPodsFreeTheirRequests(t *testing.T) {
	nodes, pods := cluster()
	pods[2].Status.Phase = corev1.PodSucceeded // bob's pod on node-a
	got := placement.Preview(nodes, pods, "", "alice", req("1", "1Gi"))
	if got.FreeCPU.Cmp(resource.MustParse("6")) != 0 {
		t.Fatalf("free cpu = %s, want 6", got.FreeCPU.String())
	}
}

func TestSidecarAndInitRequestsCount(t *testing.T) {
	nodes, pods := cluster()
	pods[2].Spec.InitContainers = []corev1.Container{{Name: "init", Resources: corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")},
	}}}
	// The pod needs max(init 3, containers 1) = 3 CPUs while it runs.
	got := placement.Preview(nodes, pods, "", "alice", req("1", "1Gi"))
	if got.FreeCPU.Cmp(resource.MustParse("3")) != 0 {
		t.Fatalf("free cpu = %s, want 3", got.FreeCPU.String())
	}
}

func TestFilePodsAreNotApps(t *testing.T) {
	nodes, pods := cluster()
	pods = append(pods, pod("files", "node-a", "alice", "", "10m", "32Mi"))
	got := placement.Preview(nodes, pods, "", "alice", req("1", "1Gi"))
	if len(got.RunningApps) != 2 {
		t.Fatalf("running apps = %v", got.RunningApps)
	}
}

func TestOnlyThePodsOfTheAskedNamespaceChooseTheNode(t *testing.T) {
	nodes := []corev1.Node{node("node-a", "8", "32Gi"), node("node-b", "8", "32Gi")}
	here, other := pod("h1", "node-a", "alice", "sales", "1", "1Gi"), pod("o1", "node-b", "alice", "elsewhere", "1", "1Gi")
	here.Namespace, other.Namespace = "team-a", "team-b"
	pods := []corev1.Pod{other, here}
	got := placement.Preview(nodes, pods, "team-a", "alice", req("1", "1Gi"))
	if !got.CoLocated || got.Node != "node-a" || len(got.RunningApps) != 1 || got.RunningApps[0] != "sales" {
		t.Fatalf("got %+v", got)
	}
	if got := placement.Preview(nodes, []corev1.Pod{other}, "team-a", "alice", req("1", "1Gi")); got.CoLocated {
		t.Fatalf("a pod in another namespace made the app co-located: %+v", got)
	}
}

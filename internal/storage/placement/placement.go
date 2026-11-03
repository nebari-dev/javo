// Package placement answers, before an app is created, where it would run
// when the owner's home is a per-user disk: next to the owner's other pods, on
// one machine. It is pure; the caller reads the nodes and pods.
package placement

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/model"
)

// Request is what the app would ask for.
type Request struct{ CPU, Memory resource.Quantity }

// Result is what Preview found.
type Result struct {
	// CoLocated is true when the owner has pods on a machine and the new app
	// must run on it. When false nothing else is set except Fits.
	CoLocated bool
	// Node is the machine the owner's pods are on.
	Node string
	// FreeCPU and FreeMemory are that machine's allocatable amounts minus the
	// requests of every pod on it that has not finished.
	FreeCPU, FreeMemory resource.Quantity
	// Fits says whether the request is within the free amounts.
	Fits bool
	// RunningApps are the row ids (model.LabelApp) of the owner's app pods on the machine.
	RunningApps []string
}

// Preview finds the machine of owner's app pods in pods and what it has free.
// pods holds the pods on every node; a pod that has succeeded or failed no
// longer holds its requests. An owner with no app pod placed on a node is not
// co-located: the app goes wherever there is room. Only app pods in namespace
// count as the owner's apps, because pod affinity only matches within the
// app's own namespace; every pod on the node still holds its requests.
func Preview(nodes []corev1.Node, pods []corev1.Pod, namespace, owner string, req Request) Result {
	var res Result
	node := ""
	for i := range pods {
		p := &pods[i]
		if isOwnerApp(p, namespace, owner) && p.Spec.NodeName != "" && holdsRequests(p) {
			node = p.Spec.NodeName
			break
		}
	}
	if node == "" {
		return res
	}
	i := slices.IndexFunc(nodes, func(n corev1.Node) bool { return n.Name == node })
	if i < 0 {
		return res
	}
	res.CoLocated, res.Node = true, node
	res.FreeCPU = nodes[i].Status.Allocatable[corev1.ResourceCPU].DeepCopy()
	res.FreeMemory = nodes[i].Status.Allocatable[corev1.ResourceMemory].DeepCopy()
	for j := range pods {
		p := &pods[j]
		if p.Spec.NodeName != node || !holdsRequests(p) {
			continue
		}
		cpu, mem := capacity.PodRequests(p)
		res.FreeCPU.Sub(cpu)
		res.FreeMemory.Sub(mem)
		if isOwnerApp(p, namespace, owner) {
			res.RunningApps = append(res.RunningApps, p.Labels[model.LabelApp])
		}
	}
	slices.Sort(res.RunningApps)
	res.Fits = res.FreeCPU.Cmp(req.CPU) >= 0 && res.FreeMemory.Cmp(req.Memory) >= 0
	return res
}

func isOwnerApp(p *corev1.Pod, namespace, owner string) bool {
	return p.Namespace == namespace && p.Labels[model.LabelOwner] == owner && p.Labels[model.LabelApp] != ""
}

func holdsRequests(p *corev1.Pod) bool {
	return p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed
}

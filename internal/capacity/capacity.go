// Package capacity says whether an app of a given CPU and memory can ever
// run on this cluster: whether some single node has that much allocatable.
// It answers "never fits", not "no room right now": what other pods use is
// left to the scheduler.
package capacity

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Machine is one node's allocatable CPU and memory, its other allocatable
// resources (Devices, such as nvidia.com/gpu) and its labels.
type Machine struct {
	CPU, Memory resource.Quantity
	Devices     corev1.ResourceList
	Labels      map[string]string
}

// Need is what a pod asks of one machine: CPU, memory, devices (extended
// resources such as nvidia.com/gpu) and the labels it must carry.
type Need struct {
	CPU, Memory  resource.Quantity
	Devices      corev1.ResourceList
	NodeSelector map[string]string
}

// Plain reports whether n is only CPU and memory: no devices, no selector.
func (n Need) Plain() bool { return len(n.Devices) == 0 && len(n.NodeSelector) == 0 }

// Words is n for a person: "4 CPU and 16 GB", "4 CPU, 16 GB and 1
// nvidia.com/gpu on a machine labelled pool=gpu".
func (n Need) Words() string {
	parts := []string{CPU(n.CPU) + " CPU", Memory(n.Memory)}
	for _, name := range slices.Sorted(maps.Keys(n.Devices)) {
		q := n.Devices[name]
		parts = append(parts, q.String()+" "+string(name))
	}
	out := strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	if len(n.NodeSelector) > 0 {
		var labels []string
		for _, k := range slices.Sorted(maps.Keys(n.NodeSelector)) {
			labels = append(labels, k+"="+n.NodeSelector[k])
		}
		out += " on a machine labelled " + strings.Join(labels, ",")
	}
	return out
}

// holds reports whether m has all of n.
func (m Machine) holds(n Need) bool {
	if m.CPU.Cmp(n.CPU) < 0 || m.Memory.Cmp(n.Memory) < 0 {
		return false
	}
	for name, want := range n.Devices {
		if have := m.Devices[name]; have.Cmp(want) < 0 {
			return false
		}
	}
	for k, v := range n.NodeSelector {
		if got, ok := m.Labels[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// Bigger reports whether m has more memory than o, or as much and more CPU.
func (m Machine) Bigger(o Machine) bool {
	if d := m.Memory.Cmp(o.Memory); d != 0 {
		return d > 0
	}
	return m.CPU.Cmp(o.CPU) > 0
}

// Cluster is the machines an app can be placed on. Every node counts,
// including cordoned and tainted ones: a node that cannot take the app now
// may later, and overstating the room only means the scheduler decides (an
// app that waits), while understating it would refuse an app that could run.
// An empty Cluster is unknown, and everything fits it.
type Cluster []Machine

// Source reads the cluster's machines.
type Source interface {
	Cluster(ctx context.Context) (Cluster, error)
}

// Nodes reads the machines from Node objects; Reader is normally an
// informer cache.
type Nodes struct {
	Reader client.Reader
}

// Watch starts an informer cache of the cluster's Nodes that keeps only
// their allocatable (Trim) and reads nothing else, and returns it as Nodes.
// It runs until ctx ends. For the gateway, which has no manager; the
// controller adds Trim to its manager's cache instead.
func Watch(ctx context.Context, cfg *rest.Config) (Nodes, error) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return Nodes{}, err
	}
	c, err := cache.New(cfg, cache.Options{
		Scheme: scheme, ReaderFailOnMissingInformer: true,
		ByObject: map[client.Object]cache.ByObject{&corev1.Node{}: {Transform: Trim}},
	})
	if err != nil {
		return Nodes{}, err
	}
	if _, err := c.GetInformer(ctx, &corev1.Node{}); err != nil {
		return Nodes{}, err
	}
	go func() {
		if err := c.Start(ctx); err != nil {
			slog.Error("node cache stopped", "err", err)
		}
	}()
	if !c.WaitForCacheSync(ctx) {
		return Nodes{}, errors.New("capacity: node cache did not sync")
	}
	return Nodes{Reader: c}, nil
}

// Cluster lists the nodes' allocatable resources and labels.
func (n Nodes) Cluster(ctx context.Context) (Cluster, error) {
	var list corev1.NodeList
	if err := n.Reader.List(ctx, &list); err != nil {
		return nil, err
	}
	out := make(Cluster, 0, len(list.Items))
	for _, nd := range list.Items {
		a := nd.Status.Allocatable
		m := Machine{CPU: a[corev1.ResourceCPU], Memory: a[corev1.ResourceMemory], Labels: nd.Labels}
		m.Devices = devices(a)
		out = append(out, m)
	}
	return out, nil
}

// devices is l without CPU and memory; nil when nothing is left.
func devices(l corev1.ResourceList) corev1.ResourceList {
	var out corev1.ResourceList
	for name, q := range l {
		if name == corev1.ResourceCPU || name == corev1.ResourceMemory {
			continue
		}
		if out == nil {
			out = corev1.ResourceList{}
		}
		out[name] = q
	}
	return out
}

// Known reports whether any machine was read.
func (c Cluster) Known() bool { return len(c) > 0 }

// Fits reports whether one machine has both cpu and mem allocatable.
func (c Cluster) Fits(cpu, mem resource.Quantity) bool {
	return c.Holds(Need{CPU: cpu, Memory: mem})
}

// Holds reports whether one machine has all of n: the CPU, memory and
// devices allocatable, and the selector's labels.
func (c Cluster) Holds(n Need) bool {
	if !c.Known() {
		return true
	}
	for _, m := range c {
		if m.holds(n) {
			return true
		}
	}
	return false
}

// LargestMachine is the Bigger-most machine; the zero Machine when none is known.
func (c Cluster) LargestMachine() Machine {
	var best Machine
	for i, m := range c {
		if i == 0 || m.Bigger(best) {
			best = m
		}
	}
	return best
}

// Largest is LargestMachine as words: "8" and "15.6 GB". Memory is rounded
// down, so a size of the shown amount never looks like it should fit when
// it does not.
func (c Cluster) Largest() (cpu, mem string) {
	m := c.LargestMachine()
	return CPU(m.CPU), Memory(m.Memory)
}

// CPU is cores as words, at most two decimals: "0.5", "8".
func CPU(q resource.Quantity) string {
	return strconv.FormatFloat(math.Floor(float64(q.MilliValue())/10)/100, 'f', -1, 64)
}

// Memory is GB of 1024 (Kubernetes Gi) with one decimal, or MB below
// 1 GB, rounded down: "15.6 GB", "512 MB".
func Memory(q resource.Quantity) string {
	const mib, gib = 1 << 20, 1 << 30
	b := float64(q.Value())
	if b < gib {
		return strconv.FormatFloat(math.Floor(b/mib), 'f', -1, 64) + " MB"
	}
	return strconv.FormatFloat(math.Floor(b/gib*10)/10, 'f', -1, 64) + " GB"
}

// PodRequests is what the pod asks a node for: its containers together, or
// its biggest init container where that is more (init containers run one
// at a time, before the others).
func PodRequests(p *corev1.Pod) (cpu, mem resource.Quantity) {
	for _, c := range p.Spec.Containers {
		cpu.Add(c.Resources.Requests[corev1.ResourceCPU])
		mem.Add(c.Resources.Requests[corev1.ResourceMemory])
	}
	for _, c := range p.Spec.InitContainers {
		if r := c.Resources.Requests[corev1.ResourceCPU]; r.Cmp(cpu) > 0 {
			cpu = r.DeepCopy()
		}
		if r := c.Resources.Requests[corev1.ResourceMemory]; r.Cmp(mem) > 0 {
			mem = r.DeepCopy()
		}
	}
	return cpu, mem
}

// PodNeed is what the pod asks of one machine: PodRequests, its app
// containers' device requests together, and its node selector.
func PodNeed(p *corev1.Pod) Need {
	n := Need{NodeSelector: p.Spec.NodeSelector}
	n.CPU, n.Memory = PodRequests(p)
	for _, c := range p.Spec.Containers {
		for name, q := range devices(c.Resources.Requests) {
			if n.Devices == nil {
				n.Devices = corev1.ResourceList{}
			}
			sum := n.Devices[name]
			sum.Add(q)
			n.Devices[name] = sum
		}
	}
	return n
}

// Trim is a cache transform for Nodes: it keeps the name, resource version,
// labels (node selectors match them), allocatable, and the addresses and pod
// ranges (the egress policy denies them to pods), and drops the rest (image
// lists can be large).
func Trim(obj any) (any, error) {
	n, ok := obj.(*corev1.Node)
	if !ok {
		return obj, nil
	}
	return &corev1.Node{
		TypeMeta:   n.TypeMeta,
		ObjectMeta: metav1.ObjectMeta{Name: n.Name, UID: n.UID, ResourceVersion: n.ResourceVersion, Labels: n.Labels},
		Spec:       corev1.NodeSpec{PodCIDR: n.Spec.PodCIDR, PodCIDRs: n.Spec.PodCIDRs},
		Status:     corev1.NodeStatus{Allocatable: n.Status.Allocatable, Addresses: n.Status.Addresses},
	}, nil
}

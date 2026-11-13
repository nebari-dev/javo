// Package nodes reads a cluster's machines from its Node objects, for
// capacity. Only what runs on Kubernetes imports it.
package nodes

import (
	"context"
	"errors"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/capacity"
)

// Source reads the machines from Node objects; Reader is normally an
// informer cache.
type Source struct {
	Reader client.Reader
}

// Watch starts an informer cache of the cluster's Nodes that keeps only
// their allocatable (Trim) and reads nothing else, and returns it as a Source.
// It runs until ctx ends. For the gateway, which has no manager; the
// controller adds Trim to its manager's cache instead.
func Watch(ctx context.Context, cfg *rest.Config) (Source, error) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return Source{}, err
	}
	c, err := cache.New(cfg, cache.Options{
		Scheme: scheme, ReaderFailOnMissingInformer: true,
		ByObject: map[client.Object]cache.ByObject{&corev1.Node{}: {Transform: Trim}},
	})
	if err != nil {
		return Source{}, err
	}
	if _, err := c.GetInformer(ctx, &corev1.Node{}); err != nil {
		return Source{}, err
	}
	go func() {
		if err := c.Start(ctx); err != nil {
			slog.Error("node cache stopped", "err", err)
		}
	}()
	if !c.WaitForCacheSync(ctx) {
		return Source{}, errors.New("capacity: node cache did not sync")
	}
	return Source{Reader: c}, nil
}

// Cluster lists the nodes' allocatable resources and labels.
func (n Source) Cluster(ctx context.Context) (capacity.Cluster, error) {
	var list corev1.NodeList
	if err := n.Reader.List(ctx, &list); err != nil {
		return nil, err
	}
	out := make(capacity.Cluster, 0, len(list.Items))
	for _, nd := range list.Items {
		out = append(out, capacity.MachineOf(nd.Status.Allocatable, nd.Labels))
	}
	return out, nil
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

// Package clusterview is what the gateway reads of a cluster's own
// objects: the pods of apps and of the Jobs that ready a home, the events
// about them, home claims, nodes and namespaces. The gateway writes none
// of them. Where apps do not run on a cluster it has no View (nil), and
// what would read one answers that there is nothing, or that it is not
// available. clusterview/kubeview reads a cluster with the Kubernetes
// client; nothing here needs one.
package clusterview

import (
	"context"

	corev1 "k8s.io/api/core/v1"
)

// View reads a cluster.
type View interface {
	// Pod is the pod called name, or nil when there is none.
	Pod(ctx context.Context, namespace, name string) (*corev1.Pod, error)
	// Pods lists the pods of namespace that carry every one of labels.
	Pods(ctx context.Context, namespace string, labels map[string]string) ([]corev1.Pod, error)
	// PodsOnNode lists the pods of namespace that run on node; of every
	// namespace when namespace is "".
	PodsOnNode(ctx context.Context, namespace, node string) ([]corev1.Pod, error)
	// Events lists the events of namespace about objects called name,
	// of whatever kind.
	Events(ctx context.Context, namespace, name string) ([]corev1.Event, error)
	// Claim is the volume claim called name, or nil when there is none.
	Claim(ctx context.Context, namespace, name string) (*corev1.PersistentVolumeClaim, error)
	// Nodes lists the cluster's nodes.
	Nodes(ctx context.Context) ([]corev1.Node, error)
	// Namespace is the namespace called name, or nil when there is none.
	Namespace(ctx context.Context, name string) (*corev1.Namespace, error)
	// Namespaces lists the namespaces that carry every one of labels.
	Namespaces(ctx context.Context, labels map[string]string) ([]corev1.Namespace, error)
}

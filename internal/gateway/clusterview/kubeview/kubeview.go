// Package kubeview is the gateway's view of a cluster (clusterview) read
// with the Kubernetes client. Only a gateway on Kubernetes imports it.
package kubeview

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/gateway/clusterview"
)

// Of is the view that reads through r. Events and the pods of a node are
// listed by field, so r must read from the API server, or from a cache
// that indexes involvedObject.name and spec.nodeName.
func Of(r client.Reader) clusterview.View { return view{r} }

type view struct{ r client.Reader }

func (v view) Pod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	var pod corev1.Pod
	if err := v.r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &pod); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	return &pod, nil
}

func (v view) Pods(ctx context.Context, namespace string, labels map[string]string) ([]corev1.Pod, error) {
	var l corev1.PodList
	err := v.r.List(ctx, &l, client.InNamespace(namespace), client.MatchingLabels(labels))
	return l.Items, err
}

func (v view) PodsOnNode(ctx context.Context, namespace, node string) ([]corev1.Pod, error) {
	opts := []client.ListOption{client.MatchingFields{"spec.nodeName": node}}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	var l corev1.PodList
	err := v.r.List(ctx, &l, opts...)
	return l.Items, err
}

func (v view) Events(ctx context.Context, namespace, name string) ([]corev1.Event, error) {
	var l corev1.EventList
	err := v.r.List(ctx, &l, client.InNamespace(namespace), client.MatchingFields{"involvedObject.name": name})
	return l.Items, err
}

func (v view) Claim(ctx context.Context, namespace, name string) (*corev1.PersistentVolumeClaim, error) {
	var claim corev1.PersistentVolumeClaim
	if err := v.r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &claim); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	return &claim, nil
}

func (v view) Nodes(ctx context.Context) ([]corev1.Node, error) {
	var l corev1.NodeList
	err := v.r.List(ctx, &l)
	return l.Items, err
}

func (v view) Namespace(ctx context.Context, name string) (*corev1.Namespace, error) {
	var ns corev1.Namespace
	if err := v.r.Get(ctx, client.ObjectKey{Name: name}, &ns); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	return &ns, nil
}

func (v view) Namespaces(ctx context.Context, labels map[string]string) ([]corev1.Namespace, error) {
	var l corev1.NamespaceList
	err := v.r.List(ctx, &l, client.MatchingLabels(labels))
	return l.Items, err
}

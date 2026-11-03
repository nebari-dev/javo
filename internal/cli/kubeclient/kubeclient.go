// Package kubeclient builds the Kubernetes client the admin commands use.
package kubeclient

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

// Factory builds the Kubernetes client a subcommand talks to.
type Factory func() (client.Client, error)

// Default connects using in-cluster config or the local kubeconfig.
func Default() (client.Client, error) {
	cfg, err := config.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	scheme := runtime.NewScheme()
	if err := addToScheme(scheme); err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{Scheme: scheme})
}

// New builds a client with f, or with Default when f is nil.
func New(f Factory) (client.Client, error) {
	if f == nil {
		f = Default
	}
	return f()
}

// addToScheme registers what the commands read: core and batch objects.
func addToScheme(s *runtime.Scheme) error {
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, batchv1.AddToScheme} {
		if err := add(s); err != nil {
			return err
		}
	}
	return nil
}

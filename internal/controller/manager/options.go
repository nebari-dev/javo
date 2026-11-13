// Package manager holds the controller-runtime manager options shared by the
// controller command.
package manager

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/model"
)

// LeaderElectionID names the Lease the controller replicas compete for.
const LeaderElectionID = "javo-controller"

// Only the elected leader runs reconcilers. The uid allocator remembers the
// uids it assigned in memory, so two active controllers could give one uid to
// two people. The Lease lives in the release namespace (read from the pod's
// service account); the chart's Role grants it.
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;create;update
// Leader election records a "became leader" Event next to the Lease.
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Options returns the manager options for the controller. The cache covers
// namespaces; none means every namespace (apps live in many of them).
func Options(scheme *runtime.Scheme, namespaces []string, metricsAddr, probeAddr string) ctrl.Options {
	var watched map[string]cache.Config
	if len(namespaces) > 0 {
		watched = make(map[string]cache.Config, len(namespaces))
		for _, ns := range namespaces {
			watched[ns] = cache.Config{}
		}
	}
	exists := func(key string) labels.Selector {
		req, err := labels.NewRequirement(key, selection.Exists, nil)
		if err != nil {
			panic(err) // a constant key is always valid
		}
		return labels.NewSelector().Add(*req)
	}
	managed := labels.SelectorFromSet(labels.Set{model.LabelManaged: "true"})
	return ctrl.Options{
		Scheme:                     scheme,
		Metrics:                    metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress:     probeAddr,
		LeaderElection:             true,
		LeaderElectionID:           LeaderElectionID,
		LeaderElectionResourceLock: "leases",
		Cache: cache.Options{
			DefaultNamespaces: watched,
			// Hold only what the controller builds, so a cluster-wide cache
			// does not mirror every pod, service, claim and event.
			ByObject: map[client.Object]cache.ByObject{
				// Owner-labelled pods: apps and job runs (and the few file pods).
				&corev1.Pod{}:                   {Label: exists(model.LabelOwner)},
				&batchv1.Job{}:                  {Label: exists(model.LabelJob)},
				&batchv1.CronJob{}:              {Label: exists(model.LabelJob)},
				&corev1.Service{}:               {Label: exists(model.LabelApp)},
				&corev1.PersistentVolumeClaim{}: {Label: exists(model.LabelOwner)},
				&corev1.Event{}:                 {Field: fields.OneTermEqualSelector("involvedObject.kind", "Pod")},
				&corev1.Namespace{}:             {Label: managed},
				&networkingv1.NetworkPolicy{}:   {Label: managed},
				// Nodes: only their allocatable, for the too-big size check.
				&corev1.Node{}: {Transform: capacity.Trim},
			},
		},
	}
}

package workspace

import (
	"context"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/controller/sweep"
	"github.com/nebari-dev/javo/internal/model"
)

// Sweep deletes the Pods and Services that carry an app id no row has any
// more: what an app left behind when its row was removed without the
// controller running. See sweep.Orphans for what it touches.
func (r *Reconciler) Sweep(ctx context.Context) error {
	return sweep.Orphans{
		Client: r.Client, Label: model.LabelApp, ManagedOnly: r.ManagedNamespacesOnly,
		Live: func(ctx context.Context) (map[string]bool, error) {
			apps, err := r.Store.Apps().ListAll(ctx)
			live := make(map[string]bool, len(apps))
			for _, a := range apps {
				live[a.ID] = true
			}
			return live, err
		},
	}.Delete(ctx, &corev1.PodList{}, &corev1.ServiceList{})
}

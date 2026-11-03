package namespace

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/model"
)

// PullSecrets are the Secrets the platform's image is pulled with. A pod can
// only name a Secret of its own namespace, so each one is copied from
// Namespace (the release namespace, where the controller may get only these
// names) into every namespace kept, and refreshed from it on every pass.
type PullSecrets struct {
	Namespace string
	Names     []string
}

// ensurePullSecrets makes or refreshes the copies in ns. A Secret of the
// same name that is not a copy (no LabelPullSecret) is left as it is.
func (r *Reconciler) ensurePullSecrets(ctx context.Context, ns string) error {
	if ns == r.PullSecrets.Namespace {
		return nil
	}
	for _, name := range r.PullSecrets.Names {
		var src corev1.Secret
		if err := r.reader().Get(ctx, client.ObjectKey{Namespace: r.PullSecrets.Namespace, Name: name}, &src); err != nil {
			return fmt.Errorf("pull secret %s/%s: %w", r.PullSecrets.Namespace, name, err)
		}
		got := &corev1.Secret{}
		err := r.reader().Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, got)
		switch {
		case apierrors.IsNotFound(err):
			copied := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: pullSecretLabels()},
				Type:       src.Type, Data: src.Data,
			}
			if err := r.Create(ctx, copied); err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("copy pull secret into %s: %w", ns, err)
			}
		case err != nil:
			return err
		case got.Labels[model.LabelPullSecret] != "true":
			ctrl.Log.Info("a Secret not made by the controller has the pull secret's name; left as it is", "namespace", ns, "name", name)
		case got.Type != src.Type:
			// The type cannot change: the copy is replaced on the next pass.
			if err := r.Delete(ctx, got); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			return fmt.Errorf("pull secret %s/%s changed type; replaced on retry", ns, name)
		case !equality.Semantic.DeepEqual(got.Data, src.Data):
			got.Data = src.Data
			if err := r.Update(ctx, got); err != nil {
				return fmt.Errorf("update pull secret %s/%s: %w", ns, name, err)
			}
		}
	}
	return nil
}

func pullSecretLabels() map[string]string {
	return map[string]string{model.LabelManaged: "true", model.LabelPullSecret: "true"}
}

// RefreshPullSecrets refreshes the copies in the namespaces kept up front
// (the placement's and Extra). People's namespaces are refreshed by their
// own resync.
func (r *Reconciler) RefreshPullSecrets(ctx context.Context) error {
	for _, spec := range r.upFrontSpecs() {
		if err := r.ensurePullSecrets(ctx, spec.Name); err != nil {
			return err
		}
	}
	return nil
}

// refreshPullSecretsEvery runs RefreshPullSecrets every ResyncEvery until
// ctx ends, so a rotated registry credential reaches the namespaces kept up
// front; a failed pass is logged and tried again on the next tick.
func (r *Reconciler) refreshPullSecretsEvery(ctx context.Context) error {
	every := r.ResyncEvery
	if every <= 0 {
		every = dbsource.DefaultResync
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if err := r.RefreshPullSecrets(ctx); err != nil {
				ctrl.Log.Error(err, "refresh pull secrets")
			}
		}
	}
}

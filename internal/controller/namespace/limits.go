package namespace

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
)

// limitsName names the ResourceQuota and the LimitRange of limits.namespace;
// the storage quota (javo-storage) is apart.
const limitsName = model.ProductName + "-limits"

// Limits is limits.namespace of the platform config: what one per-user or
// per-team namespace may use. Each empty part is none, and an object of
// ours left from an earlier setting is removed.
type Limits struct {
	// Quota is the hard limits of the namespace's ResourceQuota, such as
	// requests.cpu, limits.memory, pods or requests.ephemeral-storage.
	Quota corev1.ResourceList
	// ContainerDefaults and ContainerDefaultRequests are the limits and
	// requests a LimitRange gives a container that names none of its own.
	ContainerDefaults        corev1.ResourceList
	ContainerDefaultRequests corev1.ResourceList
}

// +kubebuilder:rbac:groups="",resources=limitranges,verbs=get;list;watch;create;update;delete

// ensureLimits makes, updates or removes the namespace's ResourceQuota and
// LimitRange to match r.Limits. Objects of that name we did not make are left alone.
func (r *Reconciler) ensureLimits(ctx context.Context, ns string) error {
	l := r.Limits
	q := &corev1.ResourceQuota{ObjectMeta: limitsMeta(ns), Spec: corev1.ResourceQuotaSpec{Hard: l.Quota}}
	if err := r.keep(ctx, q, &corev1.ResourceQuota{}, len(l.Quota) > 0, func(have client.Object) bool {
		h := have.(*corev1.ResourceQuota)
		if equality.Semantic.DeepEqual(h.Spec.Hard, q.Spec.Hard) {
			return false
		}
		h.Spec.Hard = q.Spec.Hard
		return true
	}); err != nil {
		return err
	}
	lr := &corev1.LimitRange{ObjectMeta: limitsMeta(ns), Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
		Type: corev1.LimitTypeContainer, Default: l.ContainerDefaults, DefaultRequest: l.ContainerDefaultRequests,
	}}}}
	return r.keep(ctx, lr, &corev1.LimitRange{}, len(l.ContainerDefaults) > 0 || len(l.ContainerDefaultRequests) > 0, func(have client.Object) bool {
		h := have.(*corev1.LimitRange)
		if equality.Semantic.DeepEqual(h.Spec, lr.Spec) {
			return false
		}
		h.Spec = lr.Spec
		return true
	})
}

func limitsMeta(ns string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: ns, Name: limitsName, Labels: map[string]string{model.LabelRole: model.RoleNamespaceLimits}}
}

// keep makes want when wanted and missing, brings ours up to date with
// update (true: it changed have), or deletes ours when not wanted.
func (r *Reconciler) keep(ctx context.Context, want, have client.Object, wanted bool, update func(have client.Object) bool) error {
	err := r.reader().Get(ctx, client.ObjectKeyFromObject(want), have)
	switch {
	case apierrors.IsNotFound(err):
		if !wanted {
			return nil
		}
		if err := r.Create(ctx, want); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create %s/%s: %w", want.GetNamespace(), want.GetName(), err)
		}
		return nil
	case err != nil:
		return err
	case have.GetLabels()[model.LabelRole] != model.RoleNamespaceLimits:
		return fmt.Errorf("%s/%s exists and is not ours", want.GetNamespace(), want.GetName())
	case !wanted:
		uid := have.GetUID()
		return client.IgnoreNotFound(r.Delete(ctx, have, client.Preconditions{UID: &uid}))
	case update(have):
		return r.Update(ctx, have)
	}
	return nil
}

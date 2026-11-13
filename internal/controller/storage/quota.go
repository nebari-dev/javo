package storage

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/loop/managed"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// storageQuotaName is the ResourceQuota the controller keeps in a team namespace.
const storageQuotaName = "javo-storage"

// TeamNamespace is a team's namespace and the storage its claims may request
// in total. A nil Limit means the default (storage.teamStorage.limit); with
// that unset too, the team has no allowance.
type TeamNamespace struct {
	Namespace string
	Limit     *resource.Quantity
}

// QuotaReconciler gives each team namespace a ResourceQuota for storage, so
// the API server refuses a claim that goes over the team's allowance. It is
// used only in per-team namespace mode (Teams is empty otherwise): in the
// shared and per-user modes a ResourceQuota cannot cap one person, and the
// per-home cap is storage.home.maxSize, enforced when the home is made.
type QuotaReconciler struct {
	client.Client
	Rows
	Teams []TeamNamespace
}

// +kubebuilder:rbac:groups="",resources=resourcequotas,verbs=get;list;watch;create;update;delete

// Reconcile brings every team's quota to the current limits.
func (r *QuotaReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	cs, err := r.Cluster(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	var errs []error
	for _, t := range r.Teams {
		limit := t.Limit
		if limit == nil {
			limit = cs.Spec.TeamStorage.Limit
		}
		if err := r.ensure(ctx, t.Namespace, limit, cs); err != nil {
			errs = append(errs, fmt.Errorf("storage quota in %s: %w", t.Namespace, err))
		}
	}
	return ctrl.Result{}, errors.Join(errs...)
}

// ensure makes, updates or (with no limit) removes the quota of one namespace.
// A quota of that name the controller did not make is never changed or deleted.
func (r *QuotaReconciler) ensure(ctx context.Context, ns string, limit *resource.Quantity, cs *model.ClusterStorage) error {
	have := &corev1.ResourceQuota{}
	err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: storageQuotaName}, have)
	exists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if exists && have.Labels[model.LabelRole] != model.RoleStorageQuota {
		return fmt.Errorf("ResourceQuota %s exists and is not ours", storageQuotaName)
	}
	if limit == nil {
		if !exists {
			return nil
		}
		return client.IgnoreNotFound(r.Delete(ctx, have, client.Preconditions{UID: &have.UID}))
	}
	hard := corev1.ResourceList{corev1.ResourceRequestsStorage: *limit}
	if cs.Status.Choice != nil && cs.Status.Choice.HomeClass != "" {
		hard[corev1.ResourceName(cs.Status.Choice.HomeClass+".storageclass.storage.k8s.io/requests.storage")] = *limit
	}
	if !exists {
		return r.Create(ctx, &corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: storageQuotaName, Labels: map[string]string{model.LabelRole: model.RoleStorageQuota}},
			Spec:       corev1.ResourceQuotaSpec{Hard: hard},
		})
	}
	if equality.Semantic.DeepEqual(have.Spec.Hard, hard) {
		return nil
	}
	have.Spec.Hard = hard
	return r.Update(ctx, have)
}

// SetupWithManager reconciles at start, when the findings change (a new
// chosen class) and when one of our quotas is edited or deleted.
func (r *QuotaReconciler) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	if r.Store == nil {
		return errors.New("storage.QuotaReconciler: Store is required")
	}
	ours := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == storageQuotaName && o.GetLabels()[model.LabelRole] == model.RoleStorageQuota
	})
	all := func(context.Context) ([]string, error) { return singletonID, nil }
	return ctrl.NewControllerManagedBy(mgr).Named("storage-quota").
		WatchesRawSource(managed.Source(dbsource.For(feed.Subscribe(), store.TableStorageStatus, 0, all))).
		Watches(&corev1.ResourceQuota{}, toSingleton, builder.WithPredicates(ours)).
		Complete(r)
}

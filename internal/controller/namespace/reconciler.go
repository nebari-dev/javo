// Package namespace keeps the namespaces workspaces live in: it creates each
// one the placement wants, labels it, and gives it the policy that keeps apps
// reachable only through the gateway. It never deletes a namespace: they hold
// people's home volumes.
package namespace

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/ext/placement"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

const (
	policyAllowGateway = model.PolicyAllowGateway
	policyDenyEgress   = "deny-egress-files"
	roleFiles          = "files"
)

// GatewayPods says which pods may reach apps.
type GatewayPods struct {
	Namespace string
	Labels    map[string]string
}

// Binding grants a ClusterRole to a service account inside each managed
// namespace (a RoleBinding named after the ClusterRole), so a component can
// write only where apps live instead of in every namespace.
type Binding struct {
	ClusterRole    string
	ServiceAccount types.NamespacedName
}

// Reconciler keeps namespaces. A namespace is wanted when the placement lists
// it (Start) or names it for a person (Reconcile).
type Reconciler struct {
	client.Client
	// Store holds the people a namespace is kept for. Required by
	// Reconcile and SetupWithManager.
	Store *store.Store
	// ResyncEvery is how often every person is looked at again regardless of
	// announcements. Zero means dbsource.DefaultResync.
	ResyncEvery time.Duration
	// Reader reads straight from the API server, so a namespace created a
	// moment ago is seen. Nil means Client.
	Reader    client.Reader
	Placement placement.Placement
	Gateway   GatewayPods
	// NetworkPolicy adds the policies below to every namespace.
	NetworkPolicy bool
	// Bindings are made in every namespace kept.
	Bindings []Binding
	// Extra are namespaces kept besides the placement's, like them (labels,
	// policies, bindings): the NetworkPolicy probe's own namespace.
	Extra []string
	// PullSecrets are copied into every namespace kept; none: no Secret is
	// read or made.
	PullSecrets PullSecrets
}

// RBAC: see internal/controller/workspace/reconciler.go for how the chart's
// Roles are checked against these markers.
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;create;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update
// RoleBindings to the components' app ClusterRoles; the chart limits bind to
// those ClusterRoles by name.
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;create;update;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=bind

// IsManaged reports whether the controller manages namespace name, so builds
// apps and jobs there: only those namespaces carry the Pod Security labels and
// the gateway-only NetworkPolicy. A missing namespace is not managed.
func IsManaged(ctx context.Context, r client.Reader, name string) (bool, error) {
	var ns corev1.Namespace
	if err := r.Get(ctx, client.ObjectKey{Name: name}, &ns); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return ns.Labels[model.LabelManaged] == "true", nil
}

func (r *Reconciler) reader() client.Reader {
	if r.Reader != nil {
		return r.Reader
	}
	return r.Client
}

// Reconcile keeps the namespace the placement names for one person (the
// request names the person's row id). A person who is gone, or that the
// placement has no namespace for, is ignored: their namespace stays.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	person, err := r.Store.People(store.IDRange{}).ByID(ctx, req.Name)
	if errors.Is(err, store.ErrNotFound) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	name, err := r.Placement.NamespaceFor(authz.Principal{ID: person.Name}, "")
	if err != nil {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.ensure(ctx, placement.NamespaceSpec{Name: name})
}

// Start keeps the namespaces the placement lists up front. It runs once, on
// the elected leader.
func (r *Reconciler) Start(ctx context.Context) error {
	for _, spec := range r.upFrontSpecs() {
		if err := r.ensure(ctx, spec); err != nil {
			return err
		}
	}
	return nil
}

// reconcilePolicy puts back a NetworkPolicy that was deleted or edited. These
// policies are all that keeps the unauthenticated apps away from other pods,
// so they are repaired as soon as the change is seen, not on the next
// person change or restart. Namespaces the controller does not manage are
// left alone.
func (r *Reconciler) reconcilePolicy(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Name != policyAllowGateway && req.Name != policyDenyEgress {
		return ctrl.Result{}, nil
	}
	var ns corev1.Namespace
	if err := r.reader().Get(ctx, client.ObjectKey{Name: req.Namespace}, &ns); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if ns.Labels[model.LabelManaged] != "true" {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.ensurePolicies(ctx, req.Namespace)
}

// SetupWithManager reconciles on changes to the people table (announced
// through feed, which the caller runs), repairs NetworkPolicies on their
// events, and runs Start.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	if err := mgr.Add(startFunc(r.Start)); err != nil {
		return err
	}
	if len(r.PullSecrets.Names) > 0 {
		if err := mgr.Add(startFunc(r.refreshPullSecretsEvery)); err != nil {
			return err
		}
	}
	if err := ctrl.NewControllerManagedBy(mgr).Named("namespace-policy").For(&networkingv1.NetworkPolicy{}).
		Complete(reconcile.Func(r.reconcilePolicy)); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).Named("namespace").
		WatchesRawSource(dbsource.For(feed.Subscribe(), "people", r.ResyncEvery, r.allPeople)).
		Complete(r)
}

// allPeople lists every person, so a restart or a missed announcement still
// gets each one a namespace.
func (r *Reconciler) allPeople(ctx context.Context) ([]reconcile.Request, error) {
	ids, err := r.Store.People(store.IDRange{}).IDs(ctx)
	reqs := make([]reconcile.Request, len(ids))
	for i, id := range ids {
		reqs[i] = reconcile.Request{NamespacedName: types.NamespacedName{Name: id}}
	}
	return reqs, err
}

type startFunc func(context.Context) error

func (f startFunc) Start(ctx context.Context) error { return f(ctx) }

func (r *Reconciler) ensure(ctx context.Context, spec placement.NamespaceSpec) error {
	if err := r.ensureNamespace(ctx, spec); err != nil {
		return err
	}
	if err := r.ensureBindings(ctx, spec.Name); err != nil {
		return err
	}
	if err := r.ensurePullSecrets(ctx, spec.Name); err != nil {
		return err
	}
	return r.ensurePolicies(ctx, spec.Name)
}

// upFrontSpecs are the namespaces kept from the start: the placement's and Extra.
func (r *Reconciler) upFrontSpecs() []placement.NamespaceSpec {
	specs := r.Placement.Desired()
	for _, name := range r.Extra {
		specs = append(specs, placement.NamespaceSpec{Name: name})
	}
	return specs
}

// ensureBindings creates or repairs the RoleBindings of one namespace.
func (r *Reconciler) ensureBindings(ctx context.Context, ns string) error {
	for _, b := range r.Bindings {
		want := rbacv1.RoleBinding{
			RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: b.ClusterRole},
			Subjects: []rbacv1.Subject{{
				Kind: rbacv1.ServiceAccountKind, Namespace: b.ServiceAccount.Namespace, Name: b.ServiceAccount.Name,
			}},
		}
		got := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: b.ClusterRole, Labels: policyLabels()}}
		err := r.reader().Get(ctx, client.ObjectKeyFromObject(got), got)
		switch {
		case apierrors.IsNotFound(err):
			got.RoleRef, got.Subjects = want.RoleRef, want.Subjects
			if err := r.Create(ctx, got); err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("create role binding %s/%s: %w", ns, b.ClusterRole, err)
			}
		case err != nil:
			return err
		case got.RoleRef != want.RoleRef:
			// roleRef cannot change: the binding is replaced.
			if err := r.Delete(ctx, got); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			return fmt.Errorf("role binding %s/%s pointed elsewhere; replaced on retry", ns, b.ClusterRole)
		case !equality.Semantic.DeepEqual(got.Subjects, want.Subjects):
			got.Subjects = want.Subjects
			if err := r.Update(ctx, got); err != nil {
				return fmt.Errorf("update role binding %s/%s: %w", ns, b.ClusterRole, err)
			}
		}
	}
	return nil
}

// ensurePolicies creates or repairs the NetworkPolicies of one namespace.
func (r *Reconciler) ensurePolicies(ctx context.Context, ns string) error {
	if !r.NetworkPolicy {
		return nil
	}
	for _, build := range []func(string) *networkingv1.NetworkPolicy{r.allowGatewayOnly, denyFilesEgress} {
		want := build(ns)
		got := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: want.Namespace, Name: want.Name}}
		err := r.reader().Get(ctx, client.ObjectKeyFromObject(got), got)
		switch {
		case apierrors.IsNotFound(err):
			if err := r.Create(ctx, want); err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("create policy %s/%s: %w", want.Namespace, want.Name, err)
			}
		case err != nil:
			return err
		case !equality.Semantic.DeepEqual(got.Spec, want.Spec) || !hasLabels(got.Labels, want.Labels):
			// Labels are merged: others' (Helm's, in shared mode) are kept.
			got.Spec = want.Spec
			if got.Labels == nil {
				got.Labels = map[string]string{}
			}
			maps.Copy(got.Labels, want.Labels)
			if err := r.Update(ctx, got); err != nil {
				return fmt.Errorf("update policy %s/%s: %w", want.Namespace, want.Name, err)
			}
		}
	}
	return nil
}

// hasLabels is true when have carries every label in want.
func hasLabels(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// ensureNamespace creates the namespace or adds the labels it should carry,
// leaving every other label alone.
func (r *Reconciler) ensureNamespace(ctx context.Context, spec placement.NamespaceSpec) error {
	labels := map[string]string{
		model.LabelManaged:                   "true",
		"pod-security.kubernetes.io/enforce": "restricted",
		"pod-security.kubernetes.io/warn":    "restricted",
		"pod-security.kubernetes.io/audit":   "restricted",
	}
	maps.Copy(labels, spec.Labels)

	var ns corev1.Namespace
	err := r.reader().Get(ctx, client.ObjectKey{Name: spec.Name}, &ns)
	if apierrors.IsNotFound(err) {
		ns = corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: spec.Name, Labels: labels, Annotations: spec.Annotations}}
		if err := r.Create(ctx, &ns); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create namespace %s: %w", spec.Name, err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	base := ns.DeepCopy()
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	maps.Copy(ns.Labels, labels)
	if len(spec.Annotations) > 0 && ns.Annotations == nil {
		ns.Annotations = map[string]string{}
	}
	maps.Copy(ns.Annotations, spec.Annotations)
	if equality.Semantic.DeepEqual(base.ObjectMeta, ns.ObjectMeta) {
		return nil
	}
	return r.Patch(ctx, &ns, client.MergeFrom(base))
}

// policyLabels mark the policies the controller keeps, so its cache can
// hold just those and a deleted one is noticed at once.
func policyLabels() map[string]string { return map[string]string{model.LabelManaged: "true"} }

// allowGatewayOnly admits ingress to every pod in the namespace from gateway pods only.
func (r *Reconciler) allowGatewayOnly(ns string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: policyAllowGateway, Labels: policyLabels()},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": r.Gateway.Namespace}},
				PodSelector:       &metav1.LabelSelector{MatchLabels: maps.Clone(r.Gateway.Labels)},
			}}}},
		},
	}
}

// denyFilesEgress gives file picker pods no outbound network: they only answer the gateway.
func denyFilesEgress(ns string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: policyDenyEgress, Labels: policyLabels()},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{model.LabelRole: roleFiles}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      []networkingv1.NetworkPolicyEgressRule{},
		},
	}
}

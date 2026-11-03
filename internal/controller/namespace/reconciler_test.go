package namespace

import (
	"context"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/manager"
	"github.com/nebari-dev/javo/internal/features/placementperteam"
	"github.com/nebari-dev/javo/internal/features/placementperuser"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var (
	k8s     client.Client
	restCfg *rest.Config
	db      *store.Store
)

func TestMain(m *testing.M) {
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		panic(err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	restCfg = cfg
	k8s, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}
	dir, err := os.MkdirTemp("", "javo-namespace-test")
	if err != nil {
		panic(err)
	}
	scfg, cleanup, err := storetest.FreshConfig(context.Background(), storetest.Engine(), os.Getenv("JAVO_TEST_PG_DSN"), dir)
	if err != nil {
		panic(err)
	}
	if db, err = store.Open(context.Background(), scfg); err != nil {
		panic(err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = db.Close()
	cleanup()
	_ = os.RemoveAll(dir)
	_ = env.Stop()
	os.Exit(code)
}

var gateway = GatewayPods{Namespace: "javo-system", Labels: map[string]string{"app.kubernetes.io/component": "gateway"}}

func perUser() *Reconciler {
	return &Reconciler{Client: k8s, Store: db, Placement: placementperuser.New(config.Namespaces{Prefix: "javo"}), Gateway: gateway, NetworkPolicy: true}
}

// newPerson signs a person in (the people table creates them at first login)
// and returns them; their namespace is "javo-" + their name.
func newPerson(t *testing.T, username string) model.Person {
	t.Helper()
	p, err := db.People(store.IDRange{}).Ensure(context.Background(), store.Claims{
		Issuer: "https://idp.example.com", Subject: username, Username: username,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func reconcilePerson(t *testing.T, r *Reconciler, p model.Person) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: p.ID}}); err != nil {
		t.Fatal(err)
	}
}

func getNS(t *testing.T, name string) *corev1.Namespace {
	t.Helper()
	ns := &corev1.Namespace{}
	if err := k8s.Get(context.Background(), client.ObjectKey{Name: name}, ns); err != nil {
		t.Fatalf("namespace %s: %v", name, err)
	}
	return ns
}

func TestNewPersonGetsAHardenedNamespace(t *testing.T) {
	alice := newPerson(t, "alice")
	reconcilePerson(t, perUser(), alice)

	ns := getNS(t, "javo-"+alice.Name)
	want := map[string]string{
		model.LabelManaged:                   "true",
		"pod-security.kubernetes.io/enforce": "restricted",
		"pod-security.kubernetes.io/warn":    "restricted",
		"pod-security.kubernetes.io/audit":   "restricted",
	}
	for k, v := range want {
		if ns.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, ns.Labels[k], v)
		}
	}

	var pol networkingv1.NetworkPolicy
	if err := k8s.Get(context.Background(), client.ObjectKey{Namespace: ns.Name, Name: "allow-gateway-only"}, &pol); err != nil {
		t.Fatal(err)
	}
	from := pol.Spec.Ingress[0].From[0]
	if from.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "javo-system" ||
		from.PodSelector.MatchLabels["app.kubernetes.io/component"] != "gateway" || len(pol.Spec.Ingress[0].From) != 1 {
		t.Fatalf("ingress = %+v, want only gateway pods of javo-system", pol.Spec.Ingress)
	}
	if err := k8s.Get(context.Background(), client.ObjectKey{Namespace: ns.Name, Name: "deny-egress-files"}, &networkingv1.NetworkPolicy{}); err != nil {
		t.Fatal(err)
	}
}

func TestExistingNamespaceIsAdoptedAndKeepsItsLabels(t *testing.T) {
	bob := newPerson(t, "bob")
	pre := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "javo-" + bob.Name, Labels: map[string]string{"team": "blue"}}}
	if err := k8s.Create(context.Background(), pre); err != nil {
		t.Fatal(err)
	}
	reconcilePerson(t, perUser(), bob)

	ns := getNS(t, "javo-"+bob.Name)
	if ns.Labels[model.LabelManaged] != "true" || ns.Labels["team"] != "blue" {
		t.Fatalf("labels = %v", ns.Labels)
	}
}

func TestDeletedPolicyComesBack(t *testing.T) {
	carol := newPerson(t, "carol")
	r := perUser()
	reconcilePerson(t, r, carol)
	pol := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "javo-" + carol.Name, Name: "allow-gateway-only"}}
	if err := k8s.Delete(context.Background(), pol); err != nil {
		t.Fatal(err)
	}
	reconcilePerson(t, r, carol)
	if err := k8s.Get(context.Background(), client.ObjectKeyFromObject(pol), pol); err != nil {
		t.Fatal(err)
	}
}

// A policy Helm rendered (shared mode) keeps Helm's labels when the
// controller repairs it, so a later helm upgrade can still adopt it.
func TestRepairedPolicyKeepsOtherLabels(t *testing.T) {
	erin := newPerson(t, "erin")
	r := perUser()
	reconcilePerson(t, r, erin)
	key := client.ObjectKey{Namespace: "javo-" + erin.Name, Name: "allow-gateway-only"}
	pol := &networkingv1.NetworkPolicy{}
	if err := k8s.Get(context.Background(), key, pol); err != nil {
		t.Fatal(err)
	}
	pol.Labels = map[string]string{"app.kubernetes.io/managed-by": "Helm"}
	pol.Spec.Ingress = nil
	if err := k8s.Update(context.Background(), pol); err != nil {
		t.Fatal(err)
	}
	reconcilePerson(t, r, erin)
	if err := k8s.Get(context.Background(), key, pol); err != nil {
		t.Fatal(err)
	}
	if len(pol.Spec.Ingress) != 1 || pol.Labels[model.LabelManaged] != "true" || pol.Labels["app.kubernetes.io/managed-by"] != "Helm" {
		t.Fatalf("after repair: labels %v, ingress %+v", pol.Labels, pol.Spec.Ingress)
	}
}

func TestNetworkPolicyCanBeOff(t *testing.T) {
	dave := newPerson(t, "dave")
	r := perUser()
	r.NetworkPolicy = false
	reconcilePerson(t, r, dave)
	var l networkingv1.NetworkPolicyList
	if err := k8s.List(context.Background(), &l, client.InNamespace("javo-"+dave.Name)); err != nil || len(l.Items) != 0 {
		t.Fatalf("policies = %v, %v", l.Items, err)
	}
}

// A person who is not in the table (offboarded) is ignored, and the
// namespace made for them earlier stays.
func TestGonePersonLeavesTheirNamespaceAlone(t *testing.T) {
	frida := newPerson(t, "frida")
	r := perUser()
	reconcilePerson(t, r, frida)
	reconcilePerson(t, r, model.Person{ID: "no-such-person"})
	if ns := getNS(t, "javo-"+frida.Name); !ns.DeletionTimestamp.IsZero() {
		t.Fatal("namespace was deleted")
	}
}

func TestStartKeepsTheListedTeamNamespaces(t *testing.T) {
	r := &Reconciler{Client: k8s, Store: db, Gateway: gateway, NetworkPolicy: true,
		Placement: placementperteam.New(config.Namespaces{Prefix: "javo", Teams: []config.Team{{Group: "/org/ds", Slug: "ds"}, {Group: "/org/ml", Slug: "ml"}}})}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"javo-ds", "javo-ml"} {
		if getNS(t, name).Labels[model.LabelManaged] != "true" {
			t.Errorf("%s is not managed", name)
		}
	}
	// A person with no placement-derived namespace in per-team mode is not an error.
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "nobody"}}); err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
}

// An extra namespace (the NetworkPolicy probe's, in per-user mode where the
// placement lists none up front) is kept like an app namespace: managed,
// hardened, with the gateway-only policy the probe tests.
func TestStartKeepsTheExtraNamespaces(t *testing.T) {
	r := perUser()
	r.Extra = []string{"javo-system-netprobe"}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ns := getNS(t, "javo-system-netprobe")
	if ns.Labels[model.LabelManaged] != "true" || ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Fatalf("labels = %v", ns.Labels)
	}
	if err := k8s.Get(context.Background(), client.ObjectKey{Namespace: ns.Name, Name: "allow-gateway-only"}, &networkingv1.NetworkPolicy{}); err != nil {
		t.Fatal(err)
	}
}

// The policies are the only thing between an app and the rest of the
// cluster, so a deleted or emptied one is put back by watching it, not when
// a person change happens to arrive.
func TestWatchedPolicyIsRepairedWithoutAPersonChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	scheme := k8s.Scheme()
	opts := manager.Options(scheme, nil, "0", "0")
	opts.LeaderElection = false
	mgr, err := ctrl.NewManager(restCfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Store: db, Placement: placementperuser.New(config.Namespaces{Prefix: "javo"}), Gateway: gateway, NetworkPolicy: true}
	feed := dbsource.NewFeed(db.Changes())
	if err := r.SetupWithManager(mgr, feed); err != nil {
		t.Fatal(err)
	}
	if err := db.StartFeed(ctx); err != nil {
		t.Fatal(err)
	}
	go feed.Run(ctx)
	go func() { _ = mgr.Start(ctx) }()

	frank := newPerson(t, "frank")
	ns := "javo-" + frank.Name
	allow := client.ObjectKey{Namespace: ns, Name: "allow-gateway-only"}
	deny := client.ObjectKey{Namespace: ns, Name: "deny-egress-files"}
	eventually := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if cond() {
				return
			}
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	eventually("policies created", func() bool {
		return k8s.Get(ctx, allow, &networkingv1.NetworkPolicy{}) == nil && k8s.Get(ctx, deny, &networkingv1.NetworkPolicy{}) == nil
	})

	pol := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "allow-gateway-only"}}
	if err := k8s.Delete(ctx, pol); err != nil {
		t.Fatal(err)
	}
	eventually("deleted policy back", func() bool { return k8s.Get(ctx, allow, &networkingv1.NetworkPolicy{}) == nil })

	got := &networkingv1.NetworkPolicy{}
	if err := k8s.Get(ctx, deny, got); err != nil {
		t.Fatal(err)
	}
	got.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{}}
	if err := k8s.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	eventually("edited policy restored", func() bool {
		return k8s.Get(ctx, deny, got) == nil && len(got.Spec.Egress) == 0
	})
}

// Each managed namespace binds the components' namespaced ClusterRoles there,
// so the gateway and controller can write only where apps live. A binding
// someone edited is put back.
func TestManagedNamespaceBindsTheAppRoles(t *testing.T) {
	r := perUser()
	r.Bindings = []Binding{
		{ClusterRole: "javo-gateway-apps", ServiceAccount: types.NamespacedName{Namespace: "javo-system", Name: "javo-gateway"}},
		{ClusterRole: "javo-controller-apps", ServiceAccount: types.NamespacedName{Namespace: "javo-system", Name: "javo-controller"}},
	}
	grace := newPerson(t, "grace")
	reconcilePerson(t, r, grace)

	ns := "javo-" + grace.Name
	check := func() {
		t.Helper()
		for _, b := range r.Bindings {
			var rb rbacv1.RoleBinding
			if err := k8s.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: b.ClusterRole}, &rb); err != nil {
				t.Fatalf("binding %s: %v", b.ClusterRole, err)
			}
			want := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: b.ServiceAccount.Namespace, Name: b.ServiceAccount.Name}
			if rb.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: b.ClusterRole}) ||
				len(rb.Subjects) != 1 || rb.Subjects[0] != want {
				t.Fatalf("binding %s = %+v %+v", b.ClusterRole, rb.RoleRef, rb.Subjects)
			}
		}
	}
	check()

	var rb rbacv1.RoleBinding
	if err := k8s.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "javo-gateway-apps"}, &rb); err != nil {
		t.Fatal(err)
	}
	rb.Subjects = append(rb.Subjects, rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: "default", Name: "default"})
	if err := k8s.Update(context.Background(), &rb); err != nil {
		t.Fatal(err)
	}
	reconcilePerson(t, r, grace)
	check()
}

// releaseSecret makes (or rewrites) a pull Secret in the release namespace.
func releaseSecret(t *testing.T, name, auth string) {
	t.Helper()
	ctx := context.Background()
	if err := k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "javo-system"}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "javo-system", Name: name}, Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{"ghcr.io":{"auth":"` + auth + `"}}}`)}}
	if err := k8s.Create(ctx, s); apierrors.IsAlreadyExists(err) {
		err = k8s.Update(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
}

func pullSecretIn(t *testing.T, ns, name string) *corev1.Secret {
	t.Helper()
	s := &corev1.Secret{}
	if err := k8s.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, s); err != nil {
		t.Fatalf("secret %s/%s: %v", ns, name, err)
	}
	return s
}

// Pods in an app namespace pull the platform's private image with the
// Secrets named for it, so each namespace gets a copy of them from the
// release namespace, kept equal to the original.
func TestPullSecretsAreCopiedIntoEveryNamespaceAndKeptInSync(t *testing.T) {
	releaseSecret(t, "ghcr-a", "b25l")
	r := perUser()
	r.PullSecrets = PullSecrets{Namespace: "javo-system", Names: []string{"ghcr-a"}}
	r.Extra = []string{"javo-pull-extra"}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	henry := newPerson(t, "henry")
	reconcilePerson(t, r, henry)

	for _, ns := range []string{"javo-pull-extra", "javo-" + henry.Name} {
		got := pullSecretIn(t, ns, "ghcr-a")
		if got.Type != corev1.SecretTypeDockerConfigJson || string(got.Data[corev1.DockerConfigJsonKey]) != `{"auths":{"ghcr.io":{"auth":"b25l"}}}` {
			t.Errorf("%s: copy = %s %s", ns, got.Type, got.Data)
		}
		if got.Labels[model.LabelPullSecret] != "true" {
			t.Errorf("%s: copy labels = %v", ns, got.Labels)
		}
	}

	releaseSecret(t, "ghcr-a", "dHdv")
	reconcilePerson(t, r, henry)
	if err := r.RefreshPullSecrets(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, ns := range []string{"javo-pull-extra", "javo-" + henry.Name} {
		if got := pullSecretIn(t, ns, "ghcr-a"); string(got.Data[corev1.DockerConfigJsonKey]) != `{"auths":{"ghcr.io":{"auth":"dHdv"}}}` {
			t.Errorf("%s: copy not updated: %s", ns, got.Data)
		}
	}
}

// A Secret of the same name the controller did not make is someone else's:
// it is never overwritten.
func TestPullSecretCopyLeavesAnUnrelatedSecretAlone(t *testing.T) {
	releaseSecret(t, "ghcr-b", "b25l")
	ivy := newPerson(t, "ivy")
	ns := "javo-" + ivy.Name
	if err := k8s.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	theirs := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "ghcr-b"}, StringData: map[string]string{"k": "theirs"}}
	if err := k8s.Create(context.Background(), theirs); err != nil {
		t.Fatal(err)
	}
	r := perUser()
	r.PullSecrets = PullSecrets{Namespace: "javo-system", Names: []string{"ghcr-b"}}
	reconcilePerson(t, r, ivy)
	if got := pullSecretIn(t, ns, "ghcr-b"); string(got.Data["k"]) != "theirs" || got.Labels[model.LabelPullSecret] != "" {
		t.Fatalf("unrelated secret changed: %v %s", got.Labels, got.Data)
	}
}

// No pull secrets named: no Secret is read or made (today's behaviour).
func TestNoPullSecretsMakesNoSecrets(t *testing.T) {
	jack := newPerson(t, "jack")
	reconcilePerson(t, perUser(), jack)
	var l corev1.SecretList
	if err := k8s.List(context.Background(), &l, client.InNamespace("javo-"+jack.Name)); err != nil || len(l.Items) != 0 {
		t.Fatalf("secrets = %v, %v", l.Items, err)
	}
}

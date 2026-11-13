package helm

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// vapAPI tells helm template the cluster serves ValidatingAdmissionPolicy, as
// helm install learns from a 1.30+ API server.
var vapAPI = []string{"--api-versions", "admissionregistration.k8s.io/v1/ValidatingAdmissionPolicy"}

// kindNames returns the names of the rendered objects of kind.
func kindNames(t *testing.T, out, kind string) []string {
	t.Helper()
	var names []string
	for _, u := range manifests(t, out) {
		if u.GetKind() == kind {
			names = append(names, u.GetName())
		}
	}
	return names
}

// The policies need admissionregistration.k8s.io/v1 (Kubernetes 1.30+), and
// the chart refuses to render without it in every mode: in per-user and
// per-team modes they keep the controller's cluster-wide RoleBinding rights
// to Javo's namespaces, and in every mode they keep the components from
// creating privileged namespaces and token Secrets, and the gateway from
// creating any pod but a file pod. Shared mode makes no RoleBindings, so its
// RoleBinding policy is not rendered.
func TestControllerAdmissionPoliciesNeedTheAPI(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"shared", "per-user", "per-team"} {
		out, err := renderWithoutVAP(t, "--set", "config.namespaces.mode="+mode)
		if err == nil || !strings.Contains(out, "ValidatingAdmissionPolicy") {
			t.Errorf("%s without the API: err %v, want a failure naming ValidatingAdmissionPolicy\n%s", mode, err, out)
		}
	}
	for _, c := range []struct {
		mode string
		want []string
	}{
		{"per-user", []string{"javo-javo-system-javo-controller-namespaces", "javo-javo-system-javo-controller-rolebindings", "javo-javo-system-javo-no-token-secrets"}},
		{"shared", []string{"javo-javo-system-javo-controller-namespaces", "javo-javo-system-javo-no-token-secrets"}},
	} {
		out, err := render(t, "--set", "config.namespaces.mode="+c.mode)
		if err != nil {
			t.Fatalf("%s: render: %v\n%s", c.mode, err, out)
		}
		for _, kind := range []string{"ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"} {
			if got := kindNames(t, out, kind); strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("%s: %s = %v, want %v", c.mode, kind, got, c.want)
			}
		}
	}
}

// admissionEnv starts an API server with the per-user render's RBAC and
// admission policies applied, and returns an admin client and a client that
// acts as the controller's service account.
func admissionEnv(t *testing.T, engine string) (admin, ctl client.Client) {
	t.Helper()
	admin, as, _ := admissionCluster(t, engine)
	return admin, as("javo-controller")
}

// admissionCluster is admissionEnv returning, besides the admin client, a
// client for each component's service account (javo-gateway or
// javo-controller) and the render applied. The API server is one for all
// the tests of an engine: a test makes namespaces no other test names.
func admissionCluster(t *testing.T, engine string) (admin client.Client, as func(sa string) client.Client, out string) {
	t.Helper()
	if testing.Short() || os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("needs the envtest binaries (KUBEBUILDER_ASSETS); skipped with -short")
	}
	out, err := render(t, append(modeArgs("per-user"), engineArgs(engine)...)...)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	v, _ := clusters.LoadOrStore(engine, &cluster{})
	c := v.(*cluster)
	objects := manifests(t, out)
	c.once.Do(func() { c.err = c.start(objects) })
	if c.err != nil {
		t.Fatal(c.err)
	}
	as = func(sa string) client.Client {
		ic := rest.CopyConfig(c.cfg)
		ic.Impersonate = rest.ImpersonationConfig{
			UserName: "system:serviceaccount:javo-system:" + serviceAccount(engine, sa),
			Groups:   []string{"system:serviceaccounts", "system:serviceaccounts:javo-system", "system:authenticated"},
		}
		c, err := client.New(ic, client.Options{Scheme: c.admin.Scheme()})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	return c.admin, as, out
}

// own is name with an ending no other test, and no earlier run of this
// one (-count), has: the tests share an API server, where a namespace once
// made stays.
func own(name string) string {
	return name + "-" + strconv.FormatInt(names.Add(1), 10)
}

var names atomic.Int64

// removeNamespace removes the empty namespace name for good. No controller
// runs here that would end a deleted namespace, so its last finalizer is
// taken off by hand.
func removeNamespace(admin client.Client, name string) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	_ = admin.Delete(ctx, ns)
	if err := admin.Get(ctx, client.ObjectKeyFromObject(ns), ns); err != nil {
		return
	}
	ns.Spec.Finalizers = nil
	_ = admin.SubResource("finalize").Update(ctx, ns)
}

// clusters holds, by engine, the API server the tests of the admission
// policies share: one takes seconds to start. TestMain stops them.
var clusters sync.Map // string -> *cluster

type cluster struct {
	once  sync.Once
	env   *envtest.Environment
	cfg   *rest.Config
	admin client.Client
	err   error
}

// start runs an API server with the release namespace and the RBAC and
// admission policies of objects, a render of the chart.
func (c *cluster) start(objects []unstructured.Unstructured) error {
	c.env = &envtest.Environment{}
	cfg, err := c.env.Start()
	if err != nil {
		return err
	}
	c.cfg = cfg
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if c.admin, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		return err
	}
	ctx := context.Background()
	if err := c.admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "javo-system"}}); err != nil {
		return err
	}
	apply := map[string]bool{
		"Role": true, "RoleBinding": true, "ClusterRole": true, "ClusterRoleBinding": true,
		"ValidatingAdmissionPolicy": true, "ValidatingAdmissionPolicyBinding": true,
	}
	for _, u := range objects {
		if !apply[u.GetKind()] {
			continue
		}
		if err := c.admin.Create(ctx, &u); err != nil {
			return fmt.Errorf("create %s %s: %w", u.GetKind(), u.GetName(), err)
		}
	}
	return nil
}

func TestMain(m *testing.M) {
	code := m.Run()
	var stopping sync.WaitGroup
	clusters.Range(func(_, v any) bool {
		if c := v.(*cluster); c.env != nil {
			stopping.Go(func() { _ = c.env.Stop() })
		}
		return true
	})
	stopping.Wait()
	os.Exit(code)
}

// deniedByPolicy is true when err is a ValidatingAdmissionPolicy's denial.
func deniedByPolicy(err error) bool {
	return err != nil && strings.Contains(err.Error(), "ValidatingAdmissionPolicy")
}

// waitForPolicy retries try until the policies deny it: the API server
// loads new policies a moment after they are created.
func waitForPolicy(t *testing.T, try func() error) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := try()
		if deniedByPolicy(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the policy never denied the request; last result: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// restrictedLabels are the labels the controller gives a namespace it makes.
func restrictedLabels() map[string]string {
	return map[string]string{
		"javo.nebari.dev/managed":            "true",
		"pod-security.kubernetes.io/enforce": "restricted",
		"pod-security.kubernetes.io/warn":    "restricted",
		"pod-security.kubernetes.io/audit":   "restricted",
	}
}

func appRoleBinding(ns, role string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, GenerateName: "javo-test-"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Namespace: "javo-system", Name: "javo-gateway"}},
	}
}

// RBAC lets the controller bind its app ClusterRoles in any namespace; the
// policy keeps it to managed namespaces and to those roles only.
func TestControllerBindsOnlyItsAppRolesInManagedNamespaces(t *testing.T) {
	t.Parallel()
	admin, ctl := admissionEnv(t, "postgres")
	ctx := context.Background()
	nsName := own("javo-frank")
	appRole := "javo-javo-system-javo-gateway-apps"

	waitForPolicy(t, func() error { return ctl.Create(ctx, appRoleBinding("kube-system", appRole)) })

	managed := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName, Labels: restrictedLabels()}}
	if err := ctl.Create(ctx, managed); err != nil {
		t.Fatalf("create managed namespace: %v", err)
	}
	if err := ctl.Create(ctx, appRoleBinding(nsName, appRole)); err != nil {
		t.Errorf("app role binding in a managed namespace: %v", err)
	}

	// A ClusterRole whose rules the controller already holds, so RBAC's
	// escalation check passes and only the policy can refuse it.
	other := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "javo-test-pod-reader"},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}},
	}
	if err := admin.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Delete(context.Background(), other) })
	if err := ctl.Create(ctx, appRoleBinding(nsName, other.Name)); !deniedByPolicy(err) {
		t.Errorf("binding another ClusterRole in a managed namespace: %v, want a policy denial", err)
	}

	foreign := appRoleBinding("kube-system", appRole)
	if err := admin.Create(ctx, foreign); err != nil {
		t.Fatalf("admin binding in kube-system: %v", err)
	}
	if err := ctl.Delete(ctx, foreign); !deniedByPolicy(err) {
		t.Errorf("deleting a binding in kube-system: %v, want a policy denial", err)
	}
}

// The controller may bind its app roles only to the chart's own service
// account for each role, and only in namespaces Pod Security keeps
// restricted: a binding to system:authenticated would hand every account in
// the cluster the Secrets there, and a privileged namespace would let the
// bound account start a root pod on a node.
func TestControllerBindsAppRolesOnlyToTheChartsAccounts(t *testing.T) {
	t.Parallel()
	admin, ctl := admissionEnv(t, "postgres")
	ctx := context.Background()
	nsName, looseNS := own("javo-grace"), own("javo-loose")
	gwRole, ctlRole := "javo-javo-system-javo-gateway-apps", "javo-javo-system-javo-controller-apps"
	sa := func(name string) rbacv1.Subject {
		return rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: "javo-system", Name: name}
	}
	with := func(ns, role string, subjects ...rbacv1.Subject) *rbacv1.RoleBinding {
		b := appRoleBinding(ns, role)
		b.Subjects = subjects
		return b
	}

	waitForPolicy(t, func() error { return ctl.Create(ctx, appRoleBinding("kube-system", gwRole)) })
	if err := ctl.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName, Labels: restrictedLabels()}}); err != nil {
		t.Fatal(err)
	}
	loose := restrictedLabels()
	loose["pod-security.kubernetes.io/enforce"] = "privileged"
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: looseNS, Labels: loose}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		b     *rbacv1.RoleBinding
		allow bool
	}{
		{"gateway role to the gateway", with(nsName, gwRole, sa("javo-gateway")), true},
		{"controller role to the controller", with(nsName, ctlRole, sa("javo-controller")), true},
		{"gateway role to every account", with(nsName, gwRole, rbacv1.Subject{Kind: rbacv1.GroupKind, APIGroup: rbacv1.GroupName, Name: "system:authenticated"}), false},
		{"controller role to the gateway", with(nsName, ctlRole, sa("javo-gateway")), false},
		{"gateway role to another account", with(nsName, gwRole, sa("default")), false},
		{"gateway role to a second account too", with(nsName, gwRole, sa("javo-gateway"), sa("default")), false},
		{"gateway role to a user", with(nsName, gwRole, rbacv1.Subject{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: "mallory"}), false},
		{"in a managed namespace that is not restricted", with(looseNS, gwRole, sa("javo-gateway")), false},
	} {
		err := ctl.Create(ctx, c.b)
		if c.allow && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if !c.allow && !deniedByPolicy(err) {
			t.Errorf("%s: %v, want a policy denial", c.name, err)
		}
	}
}

// RBAC lets the controller create any namespace. The policy keeps creation to
// the names Javo's placement produces, always managed and Pod Security
// restricted, with Javo's own labels and annotations only: a namespace it
// made privileged would let it bind its app role there and start a root pod
// on a node.
func TestControllerCreatesOnlyRestrictedJavoNamespaces(t *testing.T) {
	t.Parallel()
	admin, ctl := admissionEnv(t, "postgres")
	ctx := context.Background()
	// The one name that is not a test's own: it goes when the test ends.
	t.Cleanup(func() { removeNamespace(admin, "javo-system-netprobe") })
	ns := func(name string, change func(labels, annotations map[string]string)) *corev1.Namespace {
		labels, annotations := restrictedLabels(), map[string]string{}
		if change != nil {
			change(labels, annotations)
		}
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, Annotations: annotations}}
	}
	try := 0
	waitForPolicy(t, func() error {
		try++
		return ctl.Create(ctx, ns("javo-probe-"+strconv.Itoa(try), func(l, _ map[string]string) {
			l["pod-security.kubernetes.io/enforce"] = "privileged"
		}))
	})
	for _, c := range []struct {
		name  string
		ns    *corev1.Namespace
		allow bool
	}{
		{"a person's namespace", ns(own("javo-carol"), nil), true},
		{"with a Javo label and annotation", ns(own("javo-dave"), func(l, a map[string]string) {
			l["javo.nebari.dev/team"] = "blue"
			a["javo.nebari.dev/x"] = "y"
		}), true},
		{"the network probe's namespace", ns("javo-system-netprobe", nil), true},
		{"privileged", ns("javo-erin", func(l, _ map[string]string) { l["pod-security.kubernetes.io/enforce"] = "privileged" }), false},
		{"warn baseline", ns("javo-erin", func(l, _ map[string]string) { l["pod-security.kubernetes.io/warn"] = "baseline" }), false},
		{"no Pod Security labels", ns("javo-erin", func(l, _ map[string]string) {
			delete(l, "pod-security.kubernetes.io/enforce")
			delete(l, "pod-security.kubernetes.io/warn")
			delete(l, "pod-security.kubernetes.io/audit")
		}), false},
		{"not managed", ns("javo-erin", func(l, _ map[string]string) { delete(l, "javo.nebari.dev/managed") }), false},
		{"a foreign label", ns("javo-erin", func(l, _ map[string]string) { l["team"] = "red" }), false},
		{"a foreign annotation", ns("javo-erin", func(_, a map[string]string) { a["example.com/x"] = "y" }), false},
		{"a name not Javo's", ns("evil", nil), false},
	} {
		err := ctl.Create(ctx, c.ns)
		if c.allow && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if !c.allow && !deniedByPolicy(err) {
			t.Errorf("%s: %v, want a policy denial", c.name, err)
		}
	}
}

// Kubernetes fills a Secret of type service-account-token with a token that
// never expires. Both components may create Secrets, and read some by name,
// so either could otherwise mint itself a lasting credential for any service
// account in a namespace it writes to.
func TestComponentsCannotMintServiceAccountTokens(t *testing.T) {
	t.Parallel()
	admin, as, _ := admissionCluster(t, "postgres")
	ctx := context.Background()
	nsName := own("javo-heidi")
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName, Labels: restrictedLabels()}}); err != nil {
		t.Fatal(err)
	}
	bind := appRoleBinding(nsName, "javo-javo-system-javo-controller-apps")
	bind.Subjects[0].Name = "javo-controller"
	if err := admin.Create(ctx, bind); err != nil {
		t.Fatal(err)
	}
	token := func(ns, name string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Annotations: map[string]string{corev1.ServiceAccountNameKey: "javo-gateway"}},
			Type:       corev1.SecretTypeServiceAccountToken,
		}
	}
	try := 0
	waitForPolicy(t, func() error {
		try++
		return as("javo-controller").Create(ctx, token("javo-system", "javo-probe-"+strconv.Itoa(try)))
	})
	for _, c := range []struct {
		name, sa, ns string
	}{
		{"controller, release namespace", "javo-controller", "javo-system"},
		{"controller, app namespace", "javo-controller", nsName},
		{"gateway, release namespace", "javo-gateway", "javo-system"},
	} {
		if err := as(c.sa).Create(ctx, token(c.ns, "javo-notifications")); !deniedByPolicy(err) {
			t.Errorf("%s: %v, want a policy denial", c.name, err)
		}
	}
	plain := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: nsName, Name: "alice-x-javo-secrets"}}
	if err := as("javo-controller").Create(ctx, plain); err != nil {
		t.Errorf("an ordinary Secret: %v", err)
	}
}

// patchLabels merges labels into namespace name as c.
func patchLabels(c client.Client, name string, labels map[string]string) error {
	ctx := context.Background()
	var ns corev1.Namespace
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &ns); err != nil {
		return err
	}
	base := ns.DeepCopy()
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	for k, v := range labels {
		ns.Labels[k] = v
	}
	return c.Patch(ctx, &ns, client.MergeFrom(base))
}

// patchAnnotation sets one annotation on namespace name as c.
func patchAnnotation(c client.Client, name, key, value string) error {
	ctx := context.Background()
	var ns corev1.Namespace
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &ns); err != nil {
		return err
	}
	base := ns.DeepCopy()
	if ns.Annotations == nil {
		ns.Annotations = map[string]string{}
	}
	ns.Annotations[key] = value
	return c.Patch(ctx, &ns, client.MergeFrom(base))
}

// RBAC lets the controller patch any namespace; the policy keeps it to
// managed namespaces (or ones named for Javo, which it adopts) and to Javo's
// own keys and the restricted Pod Security level. SQLite runs the controller
// in the one pod's account, which the policy must match too.
func TestControllerPatchesOnlyJavoKeysOnItsNamespaces(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Parallel()
			admin, ctl := admissionEnv(t, engine)
			ctx := context.Background()
			mine, theirs, other := own("javo-alice"), own("javo-bob"), own("other")

			// Each try sets a new value, so a try made before the policy
			// loaded leaves nothing a later case depends on.
			try := 0
			waitForPolicy(t, func() error {
				try++
				return patchLabels(ctl, "kube-system", map[string]string{"example.com/probe": strconv.Itoa(try)})
			})

			if err := ctl.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: mine, Labels: restrictedLabels()}}); err != nil {
				t.Fatal(err)
			}
			if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: theirs, Labels: map[string]string{"team": "blue"}}}); err != nil {
				t.Fatal(err)
			}
			if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: other}}); err != nil {
				t.Fatal(err)
			}
			restricted := restrictedLabels()
			for _, c := range []struct {
				name  string
				err   error
				allow bool
			}{
				{"harden a managed namespace", patchLabels(ctl, mine, restricted), true},
				{"Javo label on a managed namespace", patchLabels(ctl, mine, map[string]string{"javo.nebari.dev/x": "y"}), true},
				{"Javo annotation on a managed namespace", patchAnnotation(ctl, mine, "javo.nebari.dev/x", "y"), true},
				// A namespace someone else made is never taken over, even
				// one named like Javo's: it may hold their Secrets.
				{"adopt a namespace named for Javo", patchLabels(ctl, theirs, restricted), false},
				{"loosen Pod Security", patchLabels(ctl, mine, map[string]string{"pod-security.kubernetes.io/enforce": "privileged"}), false},
				{"foreign label on a managed namespace", patchLabels(ctl, mine, map[string]string{"team": "red"}), false},
				{"foreign annotation on a managed namespace", patchAnnotation(ctl, mine, "example.com/x", "y"), false},
				{"adopt a namespace not named for Javo", patchLabels(ctl, other, restricted), false},
				// javo-system is named like a Javo namespace but is the
				// release's own: adopting it would let the controller bind
				// app roles next to the components' Secrets.
				{"adopt the release namespace", patchLabels(ctl, "javo-system", restricted), false},
				{"Javo label on kube-system", patchLabels(ctl, "kube-system", map[string]string{"javo.nebari.dev/x": "y"}), false},
				{"admin is not limited", patchLabels(admin, "kube-system", map[string]string{"team": "red"}), true},
			} {
				if c.allow && c.err != nil {
					t.Errorf("%s: %v", c.name, c.err)
				}
				if !c.allow && !deniedByPolicy(c.err) {
					t.Errorf("%s: %v, want a policy denial", c.name, c.err)
				}
			}
		})
	}
}

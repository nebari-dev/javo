package helm

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
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

// noStorage gives a file pod just the owner's home claim, prepared.
type noStorage struct{}

func (s noStorage) Input(ctx context.Context, ns, owner string) (podstorage.Input, error) {
	h, err := s.Home(ctx, ns, owner)
	return podstorage.Input{Home: &h}, err
}

func (noStorage) Home(_ context.Context, _, owner string) (model.Home, error) {
	return model.Home{Spec: model.HomeSpec{Owner: owner}, Status: model.HomeStatus{ClaimName: naming.HomeClaimName(owner), Prepared: true}}, nil
}

// The gateway may create pods in workspace namespaces only for the file
// picker: a pod creator there could otherwise mount any Secret or claim, or
// run any image. Its own file pods (made by the real code) still start;
// anything else from its account is refused.
// fixedUID gives every owner the same uid, as a single-user install would,
// and the people's group, as the gateway's ids.People does.
type fixedUID struct{}

func (fixedUID) UID(context.Context, string) (int64, int64, error) {
	return 100000, podspec.FixedGID, nil
}

func TestGatewayCreatesOnlyFilePods(t *testing.T) {
	t.Parallel()
	admin, as, out := admissionCluster(t, "postgres")
	gw := as("javo-gateway")
	ctx := context.Background()
	image := controllerArg(deploymentOf(t, out, "postgres", "gateway"), "files-image")
	nsName, owner := own("javo-ivan"), "alice"

	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName, Labels: map[string]string{"javo.nebari.dev/managed": "true"}}}); err != nil {
		t.Fatal(err)
	}
	if err := admin.Create(ctx, appRoleBinding(nsName, "javo-javo-system-javo-gateway-apps")); err != nil {
		t.Fatal(err)
	}
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: nsName, Name: naming.HomeClaimName(owner), Annotations: map[string]string{model.AnnotationOwnerID: owner}},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
		},
	}
	if err := admin.Create(ctx, claim); err != nil {
		t.Fatal(err)
	}
	other := func(name string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: nsName, Name: name},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "example.org/app:1"}}},
		}
	}
	waitForPolicy(t, func() error {
		return gw.Create(ctx, other("javo-test-probe-"+strconv.FormatInt(time.Now().UnixNano(), 36)))
	})

	key, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	pods, err := files.NewPods(files.PodsConfig{Client: gw, Namespace: nsName, Image: image, Storage: noStorage{}, UIDs: fixedUID{}, Seal: key})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pods.Ensure(ctx, owner); !errors.Is(err, files.ErrHomeStarting) {
		t.Fatalf("the gateway's own file pod: %v, want it created", err)
	}
	var made corev1.Pod
	if err := admin.Get(ctx, client.ObjectKey{Namespace: nsName, Name: model.FilesPodPrefix + owner}, &made); err != nil {
		t.Fatalf("file pod not created: %v", err)
	}
	var secretList corev1.SecretList
	if err := admin.List(ctx, &secretList, client.InNamespace(nsName)); err != nil || len(secretList.Items) != 1 {
		t.Fatalf("the file pod's password Secret: %v, %d Secrets", err, len(secretList.Items))
	}

	// Each case is the real file pod with one thing changed, made for
	// another owner (its name, owner label, home claim and password Secret
	// follow), so only the change can make the policy refuse it.
	n := 0
	variant := func(change func(*corev1.Pod)) *corev1.Pod {
		n++
		o := "v" + strconv.Itoa(n)
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: nsName, Name: model.FilesPodPrefix + o, Labels: maps.Clone(made.Labels)}, Spec: *made.Spec.DeepCopy()}
		p.Labels[model.LabelOwner] = o
		p.Spec.NodeName = ""
		for i := range p.Spec.Volumes {
			if v := p.Spec.Volumes[i]; v.Name == "home" {
				v.PersistentVolumeClaim.ClaimName = naming.HomeClaimName(o)
			}
		}
		for i, e := range p.Spec.Containers[0].Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
				ref := e.ValueFrom.SecretKeyRef.DeepCopy()
				ref.Name = p.Name + "-rc-0123456789abcdef"
				p.Spec.Containers[0].Env[i].ValueFrom = &corev1.EnvVarSource{SecretKeyRef: ref}
			}
		}
		change(p)
		return p
	}
	ctr := func(p *corev1.Pod) *corev1.Container { return &p.Spec.Containers[0] }
	homeClaim := func(p *corev1.Pod) *corev1.PersistentVolumeClaimVolumeSource {
		for i := range p.Spec.Volumes {
			if p.Spec.Volumes[i].Name == "home" {
				return p.Spec.Volumes[i].PersistentVolumeClaim
			}
		}
		t.Fatal("no home volume")
		return nil
	}
	if err := gw.Create(ctx, variant(func(*corev1.Pod) {})); err != nil {
		t.Fatalf("an unchanged file pod for another owner: %v", err)
	}
	if err := gw.Create(ctx, variant(func(p *corev1.Pod) { homeClaim(p).ClaimName += "-2" })); err != nil {
		t.Fatalf("a home's second generation: %v", err)
	}
	// A person with group folders: the folders' gids, from identity.gidRange.
	if err := gw.Create(ctx, variant(func(p *corev1.Pod) {
		p.Spec.SecurityContext.SupplementalGroups = []int64{200000, 299999}
		p.Spec.SecurityContext.SupplementalGroupsPolicy = ptr.To(corev1.SupplementalGroupsPolicyStrict)
	})); err != nil {
		t.Fatalf("a file pod with group folders: %v", err)
	}
	high := &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: "javo-test-high"}, Value: 1000000}
	if err := admin.Create(ctx, high); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Delete(context.Background(), high) })
	runtimeClass := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "javo-test-runtime"}, Handler: "other"}
	if err := admin.Create(ctx, runtimeClass); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Delete(context.Background(), runtimeClass) })
	for _, c := range []struct {
		name   string
		change func(*corev1.Pod)
	}{
		{"no-role-label", func(p *corev1.Pod) { delete(p.Labels, "javo.nebari.dev/role") }},
		{"other-owner-label", func(p *corev1.Pod) { p.Labels[model.LabelOwner] = owner }},
		// Another app's Service selects its pods by this label.
		{"app-label", func(p *corev1.Pod) { p.Labels[model.LabelApp] = "a-1" }},
		{"not-named-for-an-owner", func(p *corev1.Pod) { p.Name = "javo-test-x" }},
		{"other-image", func(p *corev1.Pod) { ctr(p).Image = "example.org/tool:1" }},
		{"command", func(p *corev1.Pod) { ctr(p).Command = []string{"sh", "-c", "id"} }},
		{"other-args", func(p *corev1.Pod) { ctr(p).Args = []string{"serve", "http", "/"} }},
		{"rcd-without-auth", func(p *corev1.Pod) { ctr(p).Args = append(ctr(p).Args, "--rc-no-auth") }},
		{"second-container", func(p *corev1.Pod) {
			p.Spec.Containers = append(p.Spec.Containers, *ctr(p).DeepCopy())
			p.Spec.Containers[1].Name = "two"
		}},
		{"init-container", func(p *corev1.Pod) {
			p.Spec.InitContainers = []corev1.Container{{Name: "i", Image: image}}
		}},
		{"secret-volume", func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "s", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "javo-db"}}})
		}},
		{"writable-claim", func(p *corev1.Pod) {
			for i := range p.Spec.Volumes {
				if pvc := p.Spec.Volumes[i].PersistentVolumeClaim; pvc != nil && p.Spec.Volumes[i].Name != "home" {
					pvc.ReadOnly = false
				}
			}
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "x", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "folder-x"}}})
		}},
		// The home may be writable only in the file pod of the home's own owner.
		{"writable-other-home", func(p *corev1.Pod) { homeClaim(p).ClaimName = naming.HomeClaimName(owner) }},
		// A person named after another's slug: home-<slug>-<8 hex> is theirs,
		// even when the hex happens to be all digits.
		{"writable-home-of-a-longer-name", func(p *corev1.Pod) { homeClaim(p).ClaimName += "-12345678" }},
		{"writable-folder-claim-named-home", func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "home2", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "home-bob"}}})
		}},
		{"env-from-secret", func(p *corev1.Pod) {
			ctr(p).EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "javo-db"}}}}
		}},
		{"env-value-from-secret", func(p *corev1.Pod) {
			ctr(p).Env = append(ctr(p).Env, corev1.EnvVar{Name: "X", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "javo-db"}, Key: "dsn"},
			}})
		}},
		{"password-from-another-secret", func(p *corev1.Pod) {
			for i, e := range ctr(p).Env {
				if e.ValueFrom != nil {
					ctr(p).Env[i].ValueFrom.SecretKeyRef.Name = "javo-db"
				}
			}
		}},
		{"service-account-token", func(p *corev1.Pod) { p.Spec.AutomountServiceAccountToken = nil }},
		{"root", func(p *corev1.Pod) { p.Spec.SecurityContext.RunAsUser = ptr.To[int64](0) }},
		{"uid-outside-the-range", func(p *corev1.Pod) { p.Spec.SecurityContext.RunAsUser = ptr.To[int64](5000) }},
		{"container-uid", func(p *corev1.Pod) { ctr(p).SecurityContext.RunAsUser = ptr.To[int64](100001) }},
		{"root-group", func(p *corev1.Pod) { p.Spec.SecurityContext.RunAsGroup = ptr.To[int64](0) }},
		{"no-group", func(p *corev1.Pod) { p.Spec.SecurityContext.RunAsGroup = nil }},
		{"container-group", func(p *corev1.Pod) { ctr(p).SecurityContext.RunAsGroup = ptr.To[int64](0) }},
		{"supplemental-group-outside-the-range", func(p *corev1.Pod) { p.Spec.SecurityContext.SupplementalGroups = []int64{200000, 4} }},
		{"supplemental-people-group", func(p *corev1.Pod) { p.Spec.SecurityContext.SupplementalGroups = []int64{100} }},
		{"fs-group", func(p *corev1.Pod) { p.Spec.SecurityContext.FSGroup = ptr.To[int64](200000) }},
		{"priority-class", func(p *corev1.Pod) { p.Spec.PriorityClassName, p.Spec.Priority = "javo-test-high", nil }},
		{"scheduler", func(p *corev1.Pod) { p.Spec.SchedulerName = "other-scheduler" }},
		{"runtime-class", func(p *corev1.Pod) { p.Spec.RuntimeClassName = ptr.To("javo-test-runtime") }},
		{"node-name", func(p *corev1.Pod) { p.Spec.NodeName = "node-1" }},
		{"toleration", func(p *corev1.Pod) {
			p.Spec.Tolerations = append(p.Spec.Tolerations, corev1.Toleration{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.TolerationOpExists})
		}},
	} {
		if err := gw.Create(ctx, variant(c.change)); !deniedByPolicy(err) {
			t.Errorf("%s: %v, want a policy denial", c.name, err)
		}
	}

	// Secrets: only a file pod's own password.
	podRef := func(name string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: name, UID: made.UID}}
	}
	pw := map[string][]byte{"password": []byte("x")}
	for _, c := range []struct {
		name string
		sec  *corev1.Secret
	}{
		{"app-secrets-name", &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alice-1a2b3c4d-lab-javo-secrets", OwnerReferences: podRef(made.Name)}, Data: pw}},
		{"no-owner", &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: made.Name + "-rc-00000000000000aa"}, Data: pw}},
		{"owned-by-another-pod", &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: made.Name + "-rc-00000000000000bb", OwnerReferences: podRef("javo-files-bob")}, Data: pw}},
		{"other-key", &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: made.Name + "-rc-00000000000000cc", OwnerReferences: podRef(made.Name)}, Data: map[string][]byte{"token": []byte("x")}}},
		{"token-type", &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: made.Name + "-rc-00000000000000dd", OwnerReferences: podRef(made.Name),
			Annotations: map[string]string{corev1.ServiceAccountNameKey: "default"}}, Type: corev1.SecretTypeServiceAccountToken}},
	} {
		c.sec.Namespace = nsName
		if err := gw.Create(ctx, c.sec); !deniedByPolicy(err) {
			t.Errorf("secret %s: %v, want a policy denial", c.name, err)
		}
	}
}

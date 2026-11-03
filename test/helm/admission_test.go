package helm

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// The policies need admissionregistration.k8s.io/v1 (Kubernetes 1.30+). Older
// clusters still install the chart, without them. Shared mode makes no
// RoleBindings, so only the namespace policy is rendered there.
func TestControllerAdmissionPoliciesNeedTheAPI(t *testing.T) {
	t.Parallel()
	out, err := render(t, "--set", "config.namespaces.mode=per-user")
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	if n := kindNames(t, out, "ValidatingAdmissionPolicy"); len(n) != 0 {
		t.Fatalf("rendered %v without the API", n)
	}
	for _, c := range []struct {
		mode string
		want []string
	}{
		{"per-user", []string{"javo-javo-system-javo-controller-namespaces", "javo-javo-system-javo-controller-rolebindings"}},
		{"shared", []string{"javo-javo-system-javo-controller-namespaces"}},
	} {
		out, err := render(t, append([]string{"--set", "config.namespaces.mode=" + c.mode}, vapAPI...)...)
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
	if testing.Short() || os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("needs the envtest binaries (KUBEBUILDER_ASSETS); skipped with -short")
	}
	out, err := render(t, append(append([]string{"--set", "config.namespaces.mode=per-user"}, engineArgs(engine)...), vapAPI...)...)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if admin, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "javo-system"}}); err != nil {
		t.Fatal(err)
	}
	apply := map[string]bool{
		"Role": true, "RoleBinding": true, "ClusterRole": true, "ClusterRoleBinding": true,
		"ValidatingAdmissionPolicy": true, "ValidatingAdmissionPolicyBinding": true,
	}
	for _, u := range manifests(t, out) {
		if !apply[u.GetKind()] {
			continue
		}
		if err := admin.Create(ctx, &u); err != nil {
			t.Fatalf("create %s %s: %v", u.GetKind(), u.GetName(), err)
		}
	}
	as := rest.CopyConfig(cfg)
	as.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:javo-system:" + serviceAccount(engine, "javo-controller"),
		Groups:   []string{"system:serviceaccounts", "system:serviceaccounts:javo-system", "system:authenticated"},
	}
	if ctl, err = client.New(as, client.Options{Scheme: scheme}); err != nil {
		t.Fatal(err)
	}
	return admin, ctl
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
	appRole := "javo-javo-system-javo-gateway-apps"

	waitForPolicy(t, func() error { return ctl.Create(ctx, appRoleBinding("kube-system", appRole)) })

	managed := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "javo-alice", Labels: map[string]string{"javo.nebari.dev/managed": "true"}}}
	if err := ctl.Create(ctx, managed); err != nil {
		t.Fatalf("create managed namespace: %v", err)
	}
	if err := ctl.Create(ctx, appRoleBinding("javo-alice", appRole)); err != nil {
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
	if err := ctl.Create(ctx, appRoleBinding("javo-alice", other.Name)); !deniedByPolicy(err) {
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
			managed := map[string]string{"javo.nebari.dev/managed": "true"}

			// Each try sets a new value, so a try made before the policy
			// loaded leaves nothing a later case depends on.
			try := 0
			waitForPolicy(t, func() error {
				try++
				return patchLabels(ctl, "kube-system", map[string]string{"example.com/probe": strconv.Itoa(try)})
			})

			if err := ctl.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "javo-alice", Labels: managed}}); err != nil {
				t.Fatal(err)
			}
			if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "javo-bob", Labels: map[string]string{"team": "blue"}}}); err != nil {
				t.Fatal(err)
			}
			if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other"}}); err != nil {
				t.Fatal(err)
			}
			restricted := map[string]string{
				"javo.nebari.dev/managed":            "true",
				"pod-security.kubernetes.io/enforce": "restricted",
				"pod-security.kubernetes.io/warn":    "restricted",
				"pod-security.kubernetes.io/audit":   "restricted",
			}
			for _, c := range []struct {
				name  string
				err   error
				allow bool
			}{
				{"harden a managed namespace", patchLabels(ctl, "javo-alice", restricted), true},
				{"Javo label on a managed namespace", patchLabels(ctl, "javo-alice", map[string]string{"javo.nebari.dev/x": "y"}), true},
				{"Javo annotation on a managed namespace", patchAnnotation(ctl, "javo-alice", "javo.nebari.dev/x", "y"), true},
				{"adopt a namespace named for Javo", patchLabels(ctl, "javo-bob", restricted), true},
				{"loosen Pod Security", patchLabels(ctl, "javo-alice", map[string]string{"pod-security.kubernetes.io/enforce": "privileged"}), false},
				{"foreign label on a managed namespace", patchLabels(ctl, "javo-alice", map[string]string{"team": "red"}), false},
				{"foreign annotation on a managed namespace", patchAnnotation(ctl, "javo-alice", "example.com/x", "y"), false},
				{"adopt a namespace not named for Javo", patchLabels(ctl, "other", restricted), false},
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

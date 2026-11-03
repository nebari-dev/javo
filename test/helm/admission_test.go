package helm

import (
	"context"
	"errors"
	"maps"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
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

// The policies need admissionregistration.k8s.io/v1 (Kubernetes 1.30+). In
// shared mode older clusters still install the chart, without them. Per-user
// and per-team modes give the controller cluster-wide RoleBinding create and
// bind, which only the policies keep to Javo's namespaces, so the chart
// refuses to render those modes without the API. Shared mode makes no
// RoleBindings, so only the namespace policy is rendered there.
func TestControllerAdmissionPoliciesNeedTheAPI(t *testing.T) {
	t.Parallel()
	out, err := render(t)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	if n := kindNames(t, out, "ValidatingAdmissionPolicy"); len(n) != 0 {
		t.Fatalf("rendered %v without the API", n)
	}
	for _, mode := range []string{"per-user", "per-team"} {
		out, err := render(t, "--set", "config.namespaces.mode="+mode)
		if err == nil || !strings.Contains(out, "ValidatingAdmissionPolicy") {
			t.Errorf("%s without the API: err %v, want a failure naming ValidatingAdmissionPolicy\n%s", mode, err, out)
		}
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
	admin, as, _ := admissionCluster(t, engine)
	return admin, as("javo-controller")
}

// admissionCluster is admissionEnv returning, besides the admin client, a
// client for each component's service account (javo-gateway or
// javo-controller) and the render applied.
func admissionCluster(t *testing.T, engine string) (admin client.Client, as func(sa string) client.Client, out string) {
	t.Helper()
	if testing.Short() || os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("needs the envtest binaries (KUBEBUILDER_ASSETS); skipped with -short")
	}
	out, err := render(t, append(modeArgs("per-user"), engineArgs(engine)...)...)
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
	as = func(sa string) client.Client {
		ic := rest.CopyConfig(cfg)
		ic.Impersonate = rest.ImpersonationConfig{
			UserName: "system:serviceaccount:javo-system:" + serviceAccount(engine, sa),
			Groups:   []string{"system:serviceaccounts", "system:serviceaccounts:javo-system", "system:authenticated"},
		}
		c, err := client.New(ic, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	return admin, as, out
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

// noStorage gives a file pod just the owner's home claim.
type noStorage struct{}

func (noStorage) Input(context.Context, string, string) (podstorage.Input, error) {
	return podstorage.Input{}, nil
}

// The gateway may create pods in workspace namespaces only for the file
// picker: a pod creator there could otherwise mount any Secret or claim, or
// run any image. Its own file pods (made by the real code) still start;
// anything else from its account is refused.
func TestGatewayCreatesOnlyFilePods(t *testing.T) {
	t.Parallel()
	admin, as, out := admissionCluster(t, "postgres")
	gw := as("javo-gateway")
	ctx := context.Background()
	image := controllerArg(deploymentOf(t, out, "postgres", "gateway"), "files-image")
	nsName, owner := "javo-alice", "alice"

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

	pods, err := files.NewPods(files.PodsConfig{Client: gw, Namespace: nsName, Image: image, Storage: noStorage{}})
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

	// Each case is the real file pod with one thing changed.
	variant := func(name string, change func(*corev1.Pod)) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: nsName, Name: "javo-test-" + name, Labels: maps.Clone(made.Labels)}, Spec: *made.Spec.DeepCopy()}
		p.Spec.NodeName = ""
		change(p)
		return p
	}
	ctr := func(p *corev1.Pod) *corev1.Container { return &p.Spec.Containers[0] }
	for _, c := range []struct {
		name   string
		change func(*corev1.Pod)
	}{
		{"no-role-label", func(p *corev1.Pod) { delete(p.Labels, "javo.nebari.dev/role") }},
		{"other-image", func(p *corev1.Pod) { ctr(p).Image = "example.org/tool:1" }},
		{"init-container", func(p *corev1.Pod) {
			p.Spec.InitContainers = []corev1.Container{{Name: "i", Image: image}}
		}},
		{"secret-volume", func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "s", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "javo-db"}}})
		}},
		{"writable-claim", func(p *corev1.Pod) {
			for i := range p.Spec.Volumes {
				if pvc := p.Spec.Volumes[i].PersistentVolumeClaim; pvc != nil {
					pvc.ReadOnly = false
				}
			}
		}},
		{"env-from-secret", func(p *corev1.Pod) {
			ctr(p).EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "javo-db"}}}}
		}},
		{"env-value-from-secret", func(p *corev1.Pod) {
			ctr(p).Env = append(ctr(p).Env, corev1.EnvVar{Name: "X", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "javo-db"}, Key: "dsn"},
			}})
		}},
		{"service-account-token", func(p *corev1.Pod) { p.Spec.AutomountServiceAccountToken = nil }},
	} {
		if err := gw.Create(ctx, variant(c.name, c.change)); !deniedByPolicy(err) {
			t.Errorf("%s: %v, want a policy denial", c.name, err)
		}
	}
}

// Package helm renders the Helm chart with `helm template` and checks the
// result. It needs helm on PATH (skipped with -short).
package helm

import (
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/nebari-dev/javo/internal/templates"
)

var chart = filepath.Join("..", "..", "deploy", "helm", "javo")

// render runs helm template with args and returns the manifest and error
// output. Tests share renders: each set of args is rendered once per run.
func render(t *testing.T, args ...string) (string, error) {
	t.Helper()
	if testing.Short() {
		t.Skip("needs helm; skipped with -short")
	}
	if _, err := exec.LookPath("helm"); err != nil {
		t.Fatal("helm not on PATH (run with -short to skip chart tests)")
	}
	v, _ := renders.LoadOrStore(strings.Join(args, "\x00"), &rendered{})
	r := v.(*rendered)
	r.once.Do(func() {
		var out []byte
		out, r.err = exec.Command("helm", append([]string{"template", "javo", chart, "--namespace", "javo-system"}, args...)...).CombinedOutput()
		r.out = string(out)
	})
	return r.out, r.err
}

// modeArgs select a namespace mode; per-user and per-team need the
// ValidatingAdmissionPolicy API, so the cluster is said to serve it.
func modeArgs(mode string) []string {
	args := []string{"--set", "config.namespaces.mode=" + mode}
	if mode != "shared" {
		args = append(args, vapAPI...)
	}
	return args
}

// renders holds each render by its args joined with NUL.
var renders sync.Map // string -> *rendered

type rendered struct {
	once sync.Once
	out  string
	err  error
}

// The default-deny policies are created by the controller in every workspace
// namespace (so every namespace mode gets them); the chart tells it to.
func TestControllerIsToldToKeepNetworkPolicies(t *testing.T) {
	t.Parallel()
	out, err := render(t)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	args := controllerArgs(t, out)
	if !slices.Contains(args, "--network-policy=true") {
		t.Fatalf("controller args %v lack --network-policy=true", args)
	}
	want := "--gateway-pod-labels=app.kubernetes.io/component=gateway,app.kubernetes.io/instance=javo,app.kubernetes.io/name=javo"
	if !slices.Contains(args, want) {
		t.Fatalf("controller args %v lack %s", args, want)
	}
}

// Upgrading from a chart that rendered the shared namespace's policies would
// make Helm delete them, leaving every app open until the new controller
// wins its election. Shared mode keeps rendering them, marked keep so Helm
// never removes them; the controller then owns and repairs them. Remove this
// one release after every install has upgraded. The other modes never had
// chart-rendered policies.
func TestUpgradeKeepsSharedPoliciesUntilTheControllerOwnsThem(t *testing.T) {
	t.Parallel()
	out, err := render(t)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	for _, name := range []string{"allow-gateway-only", "deny-egress-files"} {
		doc := manifestOf(t, out, "NetworkPolicy", name)
		if !strings.Contains(doc, "helm.sh/resource-policy: keep") {
			t.Errorf("%s is not marked keep:\n%s", name, doc)
		}
	}
	out, err = render(t, modeArgs("per-user")...)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	if strings.Contains(out, "kind: NetworkPolicy") {
		t.Fatal("per-user mode renders a NetworkPolicy; the controller owns them there")
	}
}

// manifestOf returns the rendered document of the given kind and name.
func manifestOf(t *testing.T, out, kind, name string) string {
	t.Helper()
	for _, doc := range strings.Split(out, "\n---\n") {
		if strings.Contains(doc, "kind: "+kind+"\n") && strings.Contains(doc, "\n  name: "+name+"\n") {
			return doc
		}
	}
	t.Fatalf("no %s %s in the rendered chart", kind, name)
	return ""
}

// Apps have no auth of their own; turning the policy off must be explicit.
func TestDisablingNetworkPolicyNeedsAcknowledgement(t *testing.T) {
	t.Parallel()
	out, err := render(t, "--set", "networkPolicy.enabled=false")
	if err == nil {
		t.Fatal("render without the acknowledgement succeeded")
	}
	if !strings.Contains(out, "networkPolicy.iUnderstandAppsAreUnauthenticated") {
		t.Fatalf("error does not name the acknowledgement value:\n%s", out)
	}

	out, err = render(t, "--set", "networkPolicy.enabled=false", "--set", "networkPolicy.iUnderstandAppsAreUnauthenticated=true")
	if err != nil {
		t.Fatalf("acknowledged render: %v\n%s", err, out)
	}
	if args := controllerArgs(t, out); !slices.Contains(args, "--network-policy=false") {
		t.Fatalf("controller args %v lack --network-policy=false", args)
	}
}

// controllerArgs returns the arguments of the container that runs the
// controller: its own Deployment, or the one pod of the SQLite install.
func controllerArgs(t *testing.T, manifest string) []string {
	t.Helper()
	for _, u := range manifests(t, manifest) {
		if u.GetKind() != "Deployment" || (u.GetName() != "javo-controller" && u.GetName() != "javo") {
			continue
		}
		var d appsv1.Deployment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &d); err != nil {
			t.Fatal(err)
		}
		return d.Spec.Template.Spec.Containers[0].Args
	}
	t.Fatal("no controller Deployment in the render")
	return nil
}

// grants maps "group/resource" to the sorted verbs allowed on it.
type grants map[string][]string

func (g grants) add(rules []rbacv1.PolicyRule) {
	for _, r := range rules {
		for _, grp := range r.APIGroups {
			for _, res := range r.Resources {
				k := grp + "/" + res
				g[k] = slices.Compact(slices.Sorted(slices.Values(append(g[k], r.Verbs...))))
			}
		}
	}
}

// docs decodes every YAML document in data into objects of their kind.
func docs(t *testing.T, data string) []runtime.Object {
	t.Helper()
	var out []runtime.Object
	d := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(data), 4096)
	for {
		var u unstructured.Unstructured
		if err := d.Decode(&u.Object); err != nil {
			if err == io.EOF {
				return out
			}
			t.Fatal(err)
		}
		var obj runtime.Object
		switch u.GetKind() {
		case "Role":
			obj = &rbacv1.Role{}
		case "ClusterRole":
			obj = &rbacv1.ClusterRole{}
		case "RoleBinding":
			obj = &rbacv1.RoleBinding{}
		case "ClusterRoleBinding":
			obj = &rbacv1.ClusterRoleBinding{}
		default:
			continue
		}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, obj); err != nil {
			t.Fatal(err)
		}
		out = append(out, obj)
	}
}

// chartGrants returns, per service account, every rule the chart binds to it:
// with its own bindings, and through the RoleBindings the controller makes in
// each managed namespace (--app-role-binding). Grants made only that way are
// also returned per service account in perNamespace.
func chartGrants(t *testing.T, manifest string) map[string]grants {
	all, _ := chartGrantsSplit(t, manifest)
	return all
}

func chartGrantsSplit(t *testing.T, manifest string) (all, perNamespace map[string]grants) {
	t.Helper()
	objs := docs(t, manifest)
	rules := map[string][]rbacv1.PolicyRule{} // "Kind/namespace/name" -> rules
	for _, o := range objs {
		switch r := o.(type) {
		case *rbacv1.Role:
			rules["Role/"+r.Namespace+"/"+r.Name] = r.Rules
		case *rbacv1.ClusterRole:
			rules["ClusterRole//"+r.Name] = r.Rules
		}
	}
	out := map[string]grants{}
	bind := func(ref rbacv1.RoleRef, ns string, subjects []rbacv1.Subject) {
		key := ref.Kind + "/" + ns + "/" + ref.Name
		got, ok := rules[key]
		if !ok {
			t.Fatalf("binding to missing %s", key)
		}
		for _, s := range subjects {
			if s.Kind == "ServiceAccount" {
				if out[s.Name] == nil {
					out[s.Name] = grants{}
				}
				out[s.Name].add(got)
			}
		}
	}
	for _, o := range objs {
		switch b := o.(type) {
		case *rbacv1.RoleBinding:
			bind(b.RoleRef, b.Namespace, b.Subjects)
		case *rbacv1.ClusterRoleBinding:
			bind(b.RoleRef, "", b.Subjects)
		}
	}
	perNamespace = map[string]grants{}
	for _, arg := range controllerArgs(t, manifest) {
		v, ok := strings.CutPrefix(arg, "--app-role-binding=")
		if !ok {
			continue
		}
		role, sa, _ := strings.Cut(v, "=")
		_, name, _ := strings.Cut(sa, "/")
		ref := rbacv1.RoleRef{Kind: "ClusterRole", Name: role}
		bind(ref, "", []rbacv1.Subject{{Kind: "ServiceAccount", Name: name}})
		if perNamespace[name] == nil {
			perNamespace[name] = grants{}
		}
		perNamespace[name].add(rules["ClusterRole//"+role])
	}
	return out, perNamespace
}

// The chart's hand-written Roles must grant exactly what the
// +kubebuilder:rbac markers declare (generated by make rbac). With SQLite one
// service account holds both components' rules.
func TestChartRBACMatchesMarkers(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		for _, mode := range []string{"shared", "per-user", "per-team"} {
			t.Run(engine+"/"+mode, func(t *testing.T) { checkRBACMatchesMarkers(t, engine, mode) })
		}
	}
}

func markerGrants(t *testing.T, component, mode string) grants {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", component, "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := grants{}
	for _, o := range docs(t, string(data)) {
		if r, ok := o.(*rbacv1.ClusterRole); ok {
			want.add(r.Rules)
		}
	}
	if mode == "shared" {
		// One namespace, bound by the chart: the controller makes no RoleBindings.
		delete(want, "rbac.authorization.k8s.io/rolebindings")
		delete(want, "rbac.authorization.k8s.io/clusterroles")
	}
	return want
}

func checkRBACMatchesMarkers(t *testing.T, engine, mode string) {
	// The markers declare every right the code can use, so the chart is
	// rendered with the optional features that add rules turned on.
	args := append(append(modeArgs(mode), engineArgs(engine)...), "--set", "notifications.enabled=true")
	if mode == "per-team" {
		args = append(args, "--set", "config.namespaces.teams[0].group=/org/ds", "--set", "config.namespaces.teams[0].slug=ds")
	}
	out, err := render(t, args...)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	chart := chartGrants(t, out)
	want := map[string]grants{}
	for _, sa := range []string{"javo-controller", "javo-gateway"} {
		account := serviceAccount(engine, sa)
		if want[account] == nil {
			want[account] = grants{}
		}
		for k, v := range markerGrants(t, strings.TrimPrefix(sa, "javo-"), mode) {
			want[account][k] = slices.Compact(slices.Sorted(slices.Values(append(want[account][k], v...))))
		}
	}
	for account, w := range want {
		if !maps.EqualFunc(chart[account], w, slices.Equal[[]string]) {
			t.Errorf("%s: chart grants\n  %v\nmarkers declare\n  %v", account, chart[account], w)
		}
	}
	if engine == "sqlite" && len(chart) != 1 {
		t.Errorf("sqlite: %d service accounts hold rules, want the one account of the pod: %v", len(chart), maps.Keys(chart))
	}
}

// The release namespace holds the gateway's Secrets (users, sessions). Pod
// create there would let its holder mount them, so no Role grants it.
func TestNoRoleCreatesPodsInReleaseNamespace(t *testing.T) {
	t.Parallel()
	out, err := render(t)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	for _, o := range docs(t, out) {
		r, ok := o.(*rbacv1.Role)
		if !ok || r.Namespace != "javo-system" {
			continue
		}
		for _, rule := range r.Rules {
			if slices.Contains(rule.Resources, "pods") && (slices.Contains(rule.Verbs, "create") || slices.Contains(rule.Verbs, "*")) {
				t.Errorf("Role %s grants pod create in the release namespace: %v", r.Name, rule)
			}
		}
	}
}

// API tokens are database rows, so nothing in the release namespace needs a
// Secret changed: the controller's Role there reads the Secrets it is named
// for and may create one (the key of people's secrets, on the first start),
// but never updates or deletes a Secret, so the gateway's stay as they are.
func TestNoRoleChangesSecretsInReleaseNamespace(t *testing.T) {
	t.Parallel()
	out, err := render(t)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	for _, o := range docs(t, out) {
		r, ok := o.(*rbacv1.Role)
		if !ok || r.Namespace != "javo-system" {
			continue
		}
		for _, rule := range r.Rules {
			if !slices.Contains(rule.Resources, "secrets") {
				continue
			}
			for _, verb := range rule.Verbs {
				if verb != "get" && verb != "list" && verb != "watch" && verb != "create" {
					t.Errorf("Role %s may %s Secrets in the release namespace: %v", r.Name, verb, rule)
				}
			}
		}
	}
}

// renderedTemplates returns the built-in templates in manifest by name: the
// files of the ConfigMap the gateway reads them from.
func renderedTemplates(t *testing.T, manifest string) map[string]unstructured.Unstructured {
	t.Helper()
	var cm corev1.ConfigMap
	if !object(t, manifest, "ConfigMap", "javo-builtin-templates", &cm) {
		t.Fatal("no ConfigMap javo-builtin-templates in the render")
	}
	out := map[string]unstructured.Unstructured{}
	for file, text := range cm.Data {
		var u unstructured.Unstructured
		if err := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(text), 4096).Decode(&u.Object); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if u.GetKind() != "WorkspaceTemplate" || file != u.GetName()+".yaml" {
			t.Fatalf("%s holds %s %s", file, u.GetKind(), u.GetName())
		}
		out[u.GetName()] = u
	}
	return out
}

// shippedTemplateNames lists the built-in templates the chart carries, sorted.
func shippedTemplateNames(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(chart, "files", "templates", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no template files: %v", err)
	}
	var names []string
	for _, f := range files {
		// backup.yaml is rendered only with a backup bucket (bucket_test.go).
		if n := strings.TrimSuffix(filepath.Base(f), ".yaml"); n != "backup" {
			names = append(names, n)
		}
	}
	return names
}

func TestDefaultRenderInstallsBuiltInTemplatesWithBaseURLIntact(t *testing.T) {
	t.Parallel()
	out, err := render(t)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	got := renderedTemplates(t, out)
	if want := shippedTemplateNames(t); !slices.Equal(slices.Sorted(maps.Keys(got)), want) {
		t.Fatalf("templates = %v, want every file in files/templates: %v", slices.Sorted(maps.Keys(got)), want)
	}
	args, _, _ := unstructured.NestedStringSlice(got["streamlit"].Object, "spec", "args")
	if !slices.Contains(args, "--server.baseUrlPath={{.BaseURL}}") {
		t.Fatalf("streamlit args lost the controller's {{.BaseURL}}: %v", args)
	}
	// An app template ships a sample, unless the user names the image to run
	// (userImage); a template without one is job-only (not offered as an app) and
	// lists its engines, or cannot have a sample: it requires a repository, or
	// builds one into a site served by an upstream image (nginx), or runs the
	// command its creator types in an environment (node, Go): none of them has
	// a sample to open. Every other template, the Python ones included, ships
	// its sample.
	for name, tpl := range got {
		title, _, _ := unstructured.NestedString(tpl.Object, "spec", "sample", "title")
		engines, _, _ := unstructured.NestedStringSlice(tpl.Object, "spec", "jobs", "engines")
		jobOnly, _, _ := unstructured.NestedBool(tpl.Object, "spec", "jobOnly")
		fromRepo, _, _ := unstructured.NestedString(tpl.Object, "spec", "fromRepo")
		_, builds, _ := unstructured.NestedMap(tpl.Object, "spec", "build")
		userImage, _, _ := unstructured.NestedBool(tpl.Object, "spec", "userImage")
		image, _, _ := unstructured.NestedString(tpl.Object, "spec", "image")
		upstream := strings.Contains(image, "@sha256:")
		env, _, _ := unstructured.NestedString(tpl.Object, "spec", "environment")
		userCommand, _, _ := unstructured.NestedString(tpl.Object, "spec", "userCommand")
		typed := env != "" && userCommand == "Required"
		if title == "" && (!jobOnly || len(engines) == 0) && fromRepo != "Required" && !builds && !userImage && !upstream && !typed {
			t.Errorf("%s has no sample, so it must be jobOnly with job engines, require a repository, build one, run the user's image or an upstream one, or run a typed command in an environment", name)
		}
		if jobOnly && title != "" {
			t.Errorf("%s is jobOnly but ships a sample", name)
		}
	}
}

func TestTemplateCanBeDisabledAndItsImageOverridden(t *testing.T) {
	t.Parallel()
	out, err := render(t, "--set", "workspaceTemplates.streamlit.enabled=false",
		"--set", "workspaceTemplates.jupyterlab.image=registry.example.com/lab:9")
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	got := renderedTemplates(t, out)
	want := slices.DeleteFunc(shippedTemplateNames(t), func(n string) bool { return n == "streamlit" })
	if !slices.Equal(slices.Sorted(maps.Keys(got)), want) {
		t.Fatalf("templates = %v, want %v", slices.Sorted(maps.Keys(got)), want)
	}
	if img, _, _ := unstructured.NestedString(got["jupyterlab"].Object, "spec", "image"); img != "registry.example.com/lab:9" {
		t.Fatalf("image = %q", img)
	}
}

// A built-in template can be limited to groups or identity provider roles
// from the values, and the gateway accepts the result.
func TestTemplateCanBeGatedByGroupsAndProviderRoles(t *testing.T) {
	t.Parallel()
	out, err := render(t, "--set", "workspaceTemplates.jupyterlab.allowedGroups={/org/ds}",
		"--set", "workspaceTemplates.jupyterlab.allowedProviderRoles={gpu-users}")
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	var cm corev1.ConfigMap
	object(t, out, "ConfigMap", "javo-builtin-templates", &cm)
	tpl, err := templates.Parse([]byte(cm.Data["jupyterlab.yaml"]))
	if err != nil {
		t.Fatalf("the gateway would refuse the template: %v", err)
	}
	spec := tpl.Spec
	if !slices.Equal(spec.AllowedGroups, []string{"/org/ds"}) || !slices.Equal(spec.AllowedProviderRoles, []string{"gpu-users"}) {
		t.Fatalf("allowedGroups = %v, allowedProviderRoles = %v", spec.AllowedGroups, spec.AllowedProviderRoles)
	}
	if g, _, _ := unstructured.NestedStringSlice(renderedTemplates(t, out)["streamlit"].Object, "spec", "allowedProviderRoles"); len(g) != 0 {
		t.Fatalf("streamlit gated too: %v", g)
	}
}

// Shared mode keeps the Roles in the one workspace namespace. The other modes
// have apps in namespaces that do not exist at install time, so the same rules
// are granted cluster-wide, and the chart creates no shared namespace.
func TestWorkspaceRulesAreNamespacedOnlyInSharedMode(t *testing.T) {
	t.Parallel()
	kinds := func(mode string) (roles, clusterRoles []string, namespaces int) {
		out, err := render(t, append(modeArgs(mode), "--set", "config.namespaces.teams[0].group=/g", "--set", "config.namespaces.teams[0].slug=ds")...)
		if err != nil {
			t.Fatalf("render %s: %v\n%s", mode, err, out)
		}
		for _, u := range manifests(t, out) {
			switch u.GetKind() {
			case "Role":
				if u.GetNamespace() == "javo-apps" {
					roles = append(roles, u.GetName())
				}
			case "ClusterRole":
				clusterRoles = append(clusterRoles, u.GetName())
			case "Namespace":
				namespaces++
			}
		}
		return
	}
	roles, _, namespaces := kinds("shared")
	if len(roles) != 2 || namespaces != 1 {
		t.Fatalf("shared: roles in javo-apps %v, namespaces %d", roles, namespaces)
	}
	for _, mode := range []string{"per-user", "per-team"} {
		roles, clusterRoles, namespaces := kinds(mode)
		if len(roles) != 0 || namespaces != 0 {
			t.Errorf("%s: roles in javo-apps %v, namespaces %d, want none", mode, roles, namespaces)
		}
		for _, sa := range []string{"gateway", "controller"} {
			if !slices.ContainsFunc(clusterRoles, func(n string) bool { return strings.HasSuffix(n, "javo-"+sa+"-apps") }) {
				t.Errorf("%s: no cluster-wide apps role for %s in %v", mode, sa, clusterRoles)
			}
		}
	}
}

// Outside shared mode the internet-facing gateway, and the controller, can
// write pods, services and Workspaces only in the namespaces the controller
// manages: those rules come from per-namespace RoleBindings, never from a
// cluster-wide binding, and the controller may bind only the two app roles.
func TestAppWritesAreBoundPerNamespace(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ engine, mode string }{{"sqlite", "per-user"}, {"postgres", "per-user"}, {"sqlite", "per-team"}, {"postgres", "per-team"}} {
		engine, mode := c.engine, c.mode
		args := append(append(modeArgs(mode), "--set", "config.namespaces.teams[0].group=/g", "--set", "config.namespaces.teams[0].slug=ds"), engineArgs(engine)...)
		out, err := render(t, args...)
		if err != nil {
			t.Fatalf("render %s %s: %v\n%s", engine, mode, err, out)
		}
		clusterWide := map[string]grants{}
		var bindNames []string
		objs := docs(t, out)
		roles := map[string]*rbacv1.ClusterRole{}
		for _, o := range objs {
			if r, ok := o.(*rbacv1.ClusterRole); ok {
				roles[r.Name] = r
			}
		}
		for _, o := range objs {
			b, ok := o.(*rbacv1.ClusterRoleBinding)
			if !ok {
				continue
			}
			for _, s := range b.Subjects {
				if clusterWide[s.Name] == nil {
					clusterWide[s.Name] = grants{}
				}
				clusterWide[s.Name].add(roles[b.RoleRef.Name].Rules)
				for _, r := range roles[b.RoleRef.Name].Rules {
					if slices.Contains(r.Verbs, "bind") {
						bindNames = append(bindNames, r.ResourceNames...)
					}
				}
			}
		}
		writes := []string{"create", "update", "patch", "delete"}
		for _, sa := range []string{"javo-gateway", "javo-controller"} {
			sa = serviceAccount(engine, sa)
			for _, res := range []string{"/pods", "/services", "/persistentvolumeclaims", "networking.k8s.io/networkpolicies"} {
				for _, v := range clusterWide[sa][res] {
					if slices.Contains(writes, v) {
						t.Errorf("%s: %s may %s %s in every namespace", mode, sa, v, res)
					}
				}
			}
		}
		_, perNS := chartGrantsSplit(t, out)
		if !slices.Contains(perNS[serviceAccount(engine, "javo-gateway")]["/pods"], "create") || !slices.Contains(perNS[serviceAccount(engine, "javo-controller")]["/pods"], "create") {
			t.Errorf("%s: per-namespace grants %v lack pod create", mode, perNS)
		}
		slices.Sort(bindNames)
		if want := []string{"javo-javo-system-javo-controller-apps", "javo-javo-system-javo-gateway-apps"}; !slices.Equal(bindNames, want) {
			t.Errorf("%s: controller may bind %v, want only %v", mode, bindNames, want)
		}
	}
}

// Apps, templates, sharing and jobs are database rows, so the gateway writes
// none of them to Kubernetes: its only write rights are on the file picker
// pods (pods) and making its own key Secrets on the first start (secrets,
// create only). A new write right has to be added to this list on purpose.
func TestGatewayRBACHasNoWrites(t *testing.T) {
	t.Parallel()
	// With SQLite the one pod is also the controller, so only the Postgres
	// install has a gateway account of its own to check.
	writes := []string{"create", "update", "patch", "delete", "deletecollection", "*"}
	allowed := map[string][]string{"/pods": writes, "/secrets": {"create"}}
	for _, mode := range []string{"shared", "per-user", "per-team"} {
		args := append(modeArgs(mode), postgresArgs...)
		if mode == "per-team" {
			args = append(args, "--set", "config.namespaces.teams[0].group=/org/ds", "--set", "config.namespaces.teams[0].slug=ds")
		}
		out, err := render(t, args...)
		if err != nil {
			t.Fatalf("%s: render: %v\n%s", mode, err, out)
		}
		for resource, verbs := range chartGrants(t, out)["javo-gateway"] {
			for _, verb := range verbs {
				if slices.Contains(writes, verb) && !slices.Contains(allowed[resource], verb) {
					t.Errorf("%s: the gateway may %s %s", mode, verb, resource)
				}
			}
		}
	}
}

// canIn reports whether account may do verb on resource (core group) in
// namespace ns, from the rules its RoleBindings there and its
// ClusterRoleBindings give it. A rule limited to names grants only a request
// that names one of them; name "" is a list or watch of the whole kind.
func canIn(t *testing.T, manifest, account, ns, resource, verb, name string) bool {
	t.Helper()
	objs := docs(t, manifest)
	rules := map[string][]rbacv1.PolicyRule{}
	for _, o := range objs {
		switch r := o.(type) {
		case *rbacv1.Role:
			rules["Role/"+r.Namespace+"/"+r.Name] = r.Rules
		case *rbacv1.ClusterRole:
			rules["ClusterRole//"+r.Name] = r.Rules
		}
	}
	has := func(list []string, v string) bool { return slices.Contains(list, v) || slices.Contains(list, "*") }
	allows := func(ref rbacv1.RoleRef, bindingNS string, subjects []rbacv1.Subject) bool {
		if !slices.ContainsFunc(subjects, func(s rbacv1.Subject) bool { return s.Kind == "ServiceAccount" && s.Name == account }) {
			return false
		}
		for _, r := range rules[ref.Kind+"/"+bindingNS+"/"+ref.Name] {
			if has(r.APIGroups, "") && has(r.Resources, resource) && has(r.Verbs, verb) &&
				(len(r.ResourceNames) == 0 || name != "" && slices.Contains(r.ResourceNames, name)) {
				return true
			}
		}
		return false
	}
	for _, o := range objs {
		switch b := o.(type) {
		case *rbacv1.RoleBinding:
			if b.Namespace == ns && allows(b.RoleRef, ns, b.Subjects) {
				return true
			}
		case *rbacv1.ClusterRoleBinding:
			if allows(b.RoleRef, "", b.Subjects) {
				return true
			}
		}
	}
	return false
}

// An unnamed list of Secrets in the release namespace returns every Secret's
// data (OIDC client secret, TLS key, database credentials): the controller's
// account must not have it.
func TestControllerCannotListSecretsInReleaseNamespace(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			account := serviceAccount(engine, "javo-controller")
			out, err := render(t, engineArgs(engine)...)
			if err != nil {
				t.Fatalf("render: %v\n%s", err, out)
			}
			if canIn(t, out, account, "javo-system", "secrets", "list", "") {
				t.Errorf("%s may list (and so read) every Secret in the release namespace", account)
			}
			if canIn(t, out, account, "javo-system", "secrets", "patch", "javo-anything") {
				t.Errorf("%s may patch Secrets in the release namespace", account)
			}
		})
	}
}

// The controller keeps the egress policy from these values: by default it
// denies the cloud metadata addresses, and the allow lists are passed on.
func TestEgressValuesReachTheController(t *testing.T) {
	t.Parallel()
	const metadata = "169.254.169.254/32,fd00:ec2::254/128,100.100.100.200/32,168.63.129.16/32"
	for _, c := range []struct {
		args []string
		want []string
	}{
		{nil, []string{"--egress-deny-cidrs=" + metadata}},
		{[]string{"--set-json", "networkPolicy.egress.denyCIDRs=[]"}, []string{"--egress-deny-cidrs="}},
		{[]string{"--set-json", `networkPolicy.egress.allowCIDRs=["10.0.0.0/8","192.0.2.0/24"]`},
			[]string{"--egress-deny-cidrs=" + metadata, "--egress-allow-cidrs=10.0.0.0/8,192.0.2.0/24"}},
		{[]string{"--set-json", `networkPolicy.egress.allowClusterCIDRs=["172.20.0.0/16"]`, "--set-json", `networkPolicy.egress.allowNamespaces=["monitoring","nebi"]`},
			[]string{"--egress-deny-cidrs=" + metadata, "--egress-allow-cluster-cidrs=172.20.0.0/16", "--egress-allow-namespaces=monitoring,nebi"}},
	} {
		out, err := render(t, c.args...)
		if err != nil {
			t.Fatalf("%v: render: %v\n%s", c.args, err, out)
		}
		var got []string
		for _, a := range controllerArgs(t, out) {
			if strings.HasPrefix(a, "--egress-") {
				got = append(got, a)
			}
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%v: egress args = %v, want %v", c.args, got, c.want)
		}
	}
}

// Apps must not share the release namespace: it holds the components' keys,
// the database and its backups, which the controller's app rights and the
// apps' own-namespace egress would then reach.
func TestWorkspaceNamespaceMustNotBeTheReleaseNamespace(t *testing.T) {
	t.Parallel()
	out, err := render(t, "--set", "workspaceNamespace=javo-system")
	if err == nil || !strings.Contains(out, "workspaceNamespace") {
		t.Fatalf("render with workspaceNamespace = the release namespace: err %v, want a failure naming workspaceNamespace\n%s", err, out)
	}
}

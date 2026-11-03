package helm

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// The two installs the chart can render. The Postgres one needs the Secret
// that holds its connection string.
var (
	sqliteArgs   = []string{"--set", "database.engine=sqlite"}
	postgresArgs = []string{"--set", "database.engine=postgres", "--set", "database.postgres.dsnSecret=javo-db"}
)

func engineArgs(engine string) []string {
	if engine == "postgres" {
		return postgresArgs
	}
	return sqliteArgs
}

// engines are the values of database.engine the chart accepts.
var engines = []string{"sqlite", "postgres"}

// serviceAccount is the account the pod that runs component sa (javo-gateway or
// javo-controller) uses: with SQLite one pod runs both, as the controller's.
func serviceAccount(engine, sa string) string {
	if engine == "sqlite" {
		return "javo-controller"
	}
	return sa
}

// deploymentNames are the Deployments an install has.
func deploymentNames(engine string) []string {
	if engine == "sqlite" {
		return []string{"javo"}
	}
	return []string{"javo-gateway", "javo-controller"}
}

// deploymentOf returns the Deployment that runs the component: the gateway or
// the controller, which with SQLite are the same one.
func deploymentOf(t *testing.T, out, engine, component string) appsv1.Deployment {
	t.Helper()
	name := "javo"
	if engine == "postgres" {
		name = "javo-" + component
	}
	var d appsv1.Deployment
	if !object(t, out, "Deployment", name, &d) {
		t.Fatalf("no Deployment %s in the %s render", name, engine)
	}
	return d
}

func kindsOf(t *testing.T, out string) map[string][]string {
	t.Helper()
	got := map[string][]string{}
	for _, u := range manifests(t, out) {
		got[u.GetKind()] = append(got[u.GetKind()], u.GetName())
	}
	return got
}

func TestSQLiteRendersOneDeploymentWithRecreateAndAKeptClaim(t *testing.T) {
	t.Parallel()
	out, err := render(t, sqliteArgs...)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	kinds := kindsOf(t, out)
	if !slices.Equal(kinds["Deployment"], []string{"javo"}) {
		t.Fatalf("Deployments = %v, want one named javo", kinds["Deployment"])
	}
	var d appsv1.Deployment
	object(t, out, "Deployment", "javo", &d)
	if d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || *d.Spec.Replicas != 1 {
		t.Fatalf("strategy %s, replicas %d; want Recreate and 1", d.Spec.Strategy.Type, *d.Spec.Replicas)
	}
	args := d.Spec.Template.Spec.Containers[0].Args
	for _, want := range []string{"server", "--components=gateway,controller", "--database-engine=sqlite", "--database-path=/var/lib/javo/javo.db"} {
		if !slices.Contains(args, want) {
			t.Errorf("args %v lack %s", args, want)
		}
	}
	// javo-backups holds the nightly database copies (on by default).
	if !slices.Equal(slices.Sorted(slices.Values(kinds["PersistentVolumeClaim"])), []string{"javo-backups", "javo-data"}) {
		t.Fatalf("claims = %v, want javo-backups and javo-data", kinds["PersistentVolumeClaim"])
	}
	if doc := manifestOf(t, out, "PersistentVolumeClaim", "javo-data"); !strings.Contains(doc, "helm.sh/resource-policy: keep") {
		t.Errorf("the database claim is not kept on uninstall:\n%s", doc)
	}
	// The gateway Service still finds the pod, and the apps' NetworkPolicy still lets it in.
	if d.Spec.Template.Labels["app.kubernetes.io/component"] != "gateway" {
		t.Errorf("the pod lacks the gateway label: %v", d.Spec.Template.Labels)
	}
	// The database file is written by the pod's user.
	if sc := d.Spec.Template.Spec.SecurityContext; sc == nil || sc.FSGroup == nil || *sc.FSGroup != 65532 {
		t.Errorf("pod security context %+v does not give the volume to the pod's group", sc)
	}
	if !slices.Equal(kinds["ServiceAccount"], []string{"javo-controller"}) {
		t.Errorf("service accounts = %v, want the one account of the pod", kinds["ServiceAccount"])
	}
}

func TestPostgresRendersTwoDeploymentsAndNoClaim(t *testing.T) {
	t.Parallel()
	out, err := render(t, append(postgresArgs, "--set", "gateway.replicas=3", "--set", "controller.replicas=2")...)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	kinds := kindsOf(t, out)
	if got := slices.Sorted(slices.Values(kinds["Deployment"])); !slices.Equal(got, []string{"javo-controller", "javo-gateway"}) {
		t.Fatalf("Deployments = %v", got)
	}
	if len(kinds["PersistentVolumeClaim"]) != 0 {
		t.Fatalf("claims = %v, want none", kinds["PersistentVolumeClaim"])
	}
	for name, want := range map[string]int32{"javo-gateway": 3, "javo-controller": 2} {
		var d appsv1.Deployment
		object(t, out, "Deployment", name, &d)
		if *d.Spec.Replicas != want {
			t.Errorf("%s replicas = %d, want %d", name, *d.Spec.Replicas, want)
		}
		args := d.Spec.Template.Spec.Containers[0].Args
		if !slices.Contains(args, "--database-engine=postgres") || !slices.Contains(args, "--database-dsn-file=/etc/javo-db/dsn") {
			t.Errorf("%s args %v do not name the Postgres connection file", name, args)
		}
		if !hasVolumeFromSecret(d, "javo-db") {
			t.Errorf("%s does not mount the connection Secret", name)
		}
	}
	if !slices.Equal(kinds["ServiceAccount"], []string{"javo-controller", "javo-gateway"}) {
		t.Errorf("service accounts = %v, want one per component", kinds["ServiceAccount"])
	}
}

func hasVolumeFromSecret(d appsv1.Deployment, secret string) bool {
	return slices.ContainsFunc(d.Spec.Template.Spec.Volumes, func(v corev1.Volume) bool {
		return v.Secret != nil && v.Secret.SecretName == secret
	})
}

func TestSQLiteRefusesMoreThanOneReplica(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"gateway.replicas=2", "controller.replicas=2"} {
		out, err := render(t, append(sqliteArgs, "--set", arg)...)
		if err == nil {
			t.Fatalf("%s rendered with sqlite", arg)
		}
		if !strings.Contains(out, "database.engine=sqlite runs one pod") || !strings.Contains(out, "database.engine=postgres") {
			t.Errorf("%s: the message does not say what to change:\n%s", arg, out)
		}
	}
}

func TestPostgresNeedsItsConnectionSecret(t *testing.T) {
	t.Parallel()
	out, err := render(t, "--set", "database.engine=postgres")
	if err == nil || !strings.Contains(out, "database.postgres.dsnSecret") {
		t.Fatalf("render = %v\n%s; want a message naming database.postgres.dsnSecret", err, out)
	}
	out, err = render(t, "--set", "database.engine=mysql")
	if err == nil || !strings.Contains(out, "sqlite or postgres") {
		t.Fatalf("render = %v\n%s; want a message naming the engines", err, out)
	}
}

// The chart ships no custom resource definitions or resources: the data is in
// the database.
func TestNoCustomResourcesAreRendered(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat(filepath.Join(chart, "crds")); err == nil {
		t.Error("the chart has a crds directory")
	}
	for _, engine := range engines {
		out, err := render(t, engineArgs(engine)...)
		if err != nil {
			t.Fatalf("%s: render: %v\n%s", engine, err, out)
		}
		for _, u := range manifests(t, out) {
			if u.GetKind() == "CustomResourceDefinition" || u.GetKind() == "WorkspaceTemplate" || strings.HasPrefix(u.GetAPIVersion(), "javo.nebari.dev") {
				t.Errorf("%s: the render has %s %s", engine, u.GetKind(), u.GetName())
			}
		}
	}
}

// The pod that owns migrations runs `javo db upgrade` before anything else: the
// one pod with SQLite, the controller pods with Postgres (the gateway pods
// wait for it through /readyz).
func TestUpgradeRunsBeforeTheComponentsStart(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		out, err := render(t, engineArgs(engine)...)
		if err != nil {
			t.Fatalf("%s: render: %v\n%s", engine, err, out)
		}
		owner := deploymentOf(t, out, engine, "controller")
		inits := owner.Spec.Template.Spec.InitContainers
		if len(inits) != 1 || !slices.Equal(inits[0].Args[:2], []string{"db", "upgrade"}) {
			t.Fatalf("%s: init containers %+v, want one running db upgrade", engine, inits)
		}
		if engine == "postgres" {
			gw := deploymentOf(t, out, engine, "gateway")
			if len(gw.Spec.Template.Spec.InitContainers) != 0 {
				t.Errorf("the gateway pods also run the upgrade")
			}
			probe := gw.Spec.Template.Spec.Containers[0].ReadinessProbe
			if probe == nil || probe.HTTPGet == nil || probe.HTTPGet.Path != "/readyz" {
				t.Errorf("gateway readiness probe = %+v, want /readyz", probe)
			}
		}
	}
}

// The gateway installs the built-in templates from the files the chart mounts.
func TestBuiltInTemplatesAreMountedWhereTheGatewayReadsThem(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		out, err := render(t, engineArgs(engine)...)
		if err != nil {
			t.Fatalf("%s: render: %v\n%s", engine, err, out)
		}
		gw := deploymentOf(t, out, engine, "gateway")
		c := gw.Spec.Template.Spec.Containers[0]
		if !slices.Contains(c.Args, "--templates-dir=/etc/javo-templates") {
			t.Fatalf("%s: args %v lack --templates-dir", engine, c.Args)
		}
		mounted := slices.ContainsFunc(c.VolumeMounts, func(m corev1.VolumeMount) bool { return m.MountPath == "/etc/javo-templates" })
		fromConfigMap := slices.ContainsFunc(gw.Spec.Template.Spec.Volumes, func(v corev1.Volume) bool {
			return v.ConfigMap != nil && v.ConfigMap.Name == "javo-builtin-templates"
		})
		if !mounted || !fromConfigMap {
			t.Errorf("%s: templates mounted %v from the ConfigMap %v", engine, mounted, fromConfigMap)
		}
		if gw.Spec.Template.Annotations["checksum/templates"] == "" {
			t.Errorf("%s: editing a template would not roll the pod that installs it", engine)
		}
	}
}

package helm

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
)

// controllerArg is the value of --name on the container of d, or "".
func controllerArg(d appsv1.Deployment, name string) string {
	for _, c := range d.Spec.Template.Spec.Containers {
		for _, a := range c.Args {
			if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
				return v
			}
		}
	}
	return ""
}

// releaseSecretGrants is what the controller's Role in the release namespace
// grants on secrets: verbs by resource name ("" for any name).
func releaseSecretGrants(t *testing.T, out string) map[string][]string {
	t.Helper()
	got := map[string][]string{}
	for _, o := range docs(t, out) {
		r, ok := o.(*rbacv1.Role)
		if !ok || r.Namespace != "javo-system" || r.Name != "javo-controller" {
			continue
		}
		for _, rule := range r.Rules {
			if !slices.Contains(rule.Resources, "secrets") {
				continue
			}
			names := rule.ResourceNames
			if len(names) == 0 {
				names = []string{""}
			}
			for _, n := range names {
				got[n] = append(got[n], rule.Verbs...)
			}
		}
	}
	return got
}

// The chart renders no key of people's secrets: a render without the
// cluster (helm template, Argo CD) cannot look up the live one, and a random
// one would differ on every render. The controller makes it on its first
// start (--secrets-key-create) and reads it through the API, so no pod
// mounts it; the gateway, which takes values from people, never gets it.
func TestSecretsKeyIsMadeByTheControllerNotTheChart(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		out, err := render(t, engineArgs(engine)...)
		if err != nil {
			t.Fatalf("%s render: %v\n%s", engine, err, out)
		}
		if kinds := kindsOf(t, out); slices.Contains(kinds["Secret"], "javo-secrets-key") {
			t.Fatalf("%s: the chart renders the Secret javo-secrets-key", engine)
		}
		ctl := deploymentOf(t, out, engine, "controller")
		if controllerArg(ctl, "secrets-key-secret") != "javo-secrets-key" || controllerArg(ctl, "secrets-key-create") != "true" {
			t.Fatalf("%s: controller args %v", engine, ctl.Spec.Template.Spec.Containers[0].Args)
		}
		if hasVolumeFromSecret(ctl, "javo-secrets-key") || controllerArg(ctl, "secrets-key-file") != "" {
			t.Fatalf("%s: the controller mounts the key: %+v", engine, ctl.Spec.Template.Spec.Volumes)
		}
		g := releaseSecretGrants(t, out)
		if !slices.Contains(g["javo-secrets-key"], "get") || !slices.Contains(g[""], "create") {
			t.Fatalf("%s: release namespace secrets grants %v", engine, g)
		}
		if engine == "postgres" {
			gw := deploymentOf(t, out, engine, "gateway")
			if controllerArg(gw, "secrets-key-secret") != "" || controllerArg(gw, "secrets-key-file") != "" {
				t.Fatal("the gateway gets the key-encryption key")
			}
		}
	}
}

// Nothing in a default render is random, so a GitOps tool sees no change
// between two renders of the same values.
func TestTwoRendersAreIdentical(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("needs helm; skipped with -short")
	}
	for _, engine := range engines {
		var outs [2]string
		for i := range outs {
			out, err := exec.Command("helm", append([]string{"template", "javo", chart, "--namespace", "javo-system"}, engineArgs(engine)...)...).CombinedOutput()
			if err != nil {
				t.Fatalf("%s render: %v\n%s", engine, err, out)
			}
			outs[i] = string(out)
		}
		if outs[0] != outs[1] {
			t.Fatalf("%s: two renders differ", engine)
		}
	}
}

// An admin who keeps the key elsewhere (a restored key, or one made by hand)
// names it; the controller then only reads that Secret and never makes one.
func TestSecretsKeyFromAnExistingSecret(t *testing.T) {
	t.Parallel()
	out, err := render(t, "--set", "secrets.existingKeySecret=my-kek")
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	if kinds := kindsOf(t, out); slices.Contains(kinds["Secret"], "javo-secrets-key") || slices.Contains(kinds["Secret"], "my-kek") {
		t.Fatal("the chart renders a key Secret")
	}
	d := deploymentOf(t, out, "sqlite", "controller")
	if controllerArg(d, "secrets-key-secret") != "my-kek" || controllerArg(d, "secrets-key-create") != "" {
		t.Fatalf("controller args %v", d.Spec.Template.Spec.Containers[0].Args)
	}
	g := releaseSecretGrants(t, out)
	if !slices.Contains(g["my-kek"], "get") || slices.Contains(g[""], "create") || g["javo-secrets-key"] != nil {
		t.Fatalf("release namespace secrets grants %v", g)
	}
}

// Secret files (the backup bucket's key and password) are mounted 0400 and
// owned by root; the controller runs as a non-root user, so it can read them
// only through the pod's fsGroup (kubelet gives that group read on the
// file). Without it the controller cannot start.
func TestControllerCanReadTheSecretsKey(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		out, err := render(t, engineArgs(engine)...)
		if err != nil {
			t.Fatalf("%s render: %v\n%s", engine, err, out)
		}
		pod := deploymentOf(t, out, engine, "controller").Spec.Template.Spec
		var group *int64
		for _, c := range pod.Containers {
			if c.SecurityContext != nil && c.SecurityContext.RunAsGroup != nil {
				group = c.SecurityContext.RunAsGroup
			}
		}
		if group == nil || pod.SecurityContext == nil || pod.SecurityContext.FSGroup == nil || *pod.SecurityContext.FSGroup != *group {
			t.Fatalf("%s: controller pod fsGroup %+v, want the container's group %v", engine, pod.SecurityContext, group)
		}
	}
}

// The gateway seals the identity provider's tokens of each session with a
// key of its own, made once and kept like the secrets key, so a copy of the
// database holds no live session. The controller never needs it.
func TestSessionsKeyIsGeneratedAndOnlyTheGatewayHasIt(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		out, err := render(t, engineArgs(engine)...)
		if err != nil {
			t.Fatalf("%s render: %v\n%s", engine, err, out)
		}
		var sec corev1.Secret
		if !object(t, out, "Secret", "javo-sessions-key", &sec) {
			t.Fatalf("%s: no Secret javo-sessions-key", engine)
		}
		if len(sec.Data["key"]) != 32 || sec.Annotations["helm.sh/resource-policy"] != "keep" {
			t.Fatalf("%s: key is %d bytes, annotations %v", engine, len(sec.Data["key"]), sec.Annotations)
		}
		gw := deploymentOf(t, out, engine, "gateway")
		path := controllerArg(gw, "sessions-key-file")
		if path == "" || !hasVolumeFromSecret(gw, "javo-sessions-key") || !mounts(gw, strings.TrimSuffix(path, "/key")) {
			t.Fatalf("%s: gateway args %v, volumes %+v", engine, gw.Spec.Template.Spec.Containers[0].Args, gw.Spec.Template.Spec.Volumes)
		}
		if engine == "postgres" {
			if ctl := deploymentOf(t, out, engine, "controller"); hasVolumeFromSecret(ctl, "javo-sessions-key") {
				t.Fatal("the controller gets the sessions key")
			}
		}
	}
	out, err := render(t, "--set", "secrets.existingSessionsKeySecret=my-sessions-key")
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	if kinds := kindsOf(t, out); slices.Contains(kinds["Secret"], "javo-sessions-key") {
		t.Fatal("the chart made its own sessions key as well")
	}
	if d := deploymentOf(t, out, "sqlite", "gateway"); !hasVolumeFromSecret(d, "my-sessions-key") {
		t.Fatalf("volumes %+v", d.Spec.Template.Spec.Volumes)
	}
}

package helm

import (
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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

// The key-encryption key of people's secrets is generated once, kept on
// uninstall and upgrade, and mounted only where the controller runs: the
// gateway, which takes values from people, never holds it.
func TestSecretsKeyIsGeneratedKeptAndOnlyTheControllerHasIt(t *testing.T) {
	for _, engine := range engines {
		out, err := render(t, engineArgs(engine)...)
		if err != nil {
			t.Fatalf("%s render: %v\n%s", engine, err, out)
		}
		var sec corev1.Secret
		if !object(t, out, "Secret", "javo-secrets-key", &sec) {
			t.Fatalf("%s: no Secret javo-secrets-key", engine)
		}
		if len(sec.Data["kek"]) != 32 {
			t.Fatalf("%s: kek is %d bytes, want 32 random bytes", engine, len(sec.Data["kek"]))
		}
		if sec.Annotations["helm.sh/resource-policy"] != "keep" {
			t.Fatalf("%s: the key is not kept on uninstall: %v", engine, sec.Annotations)
		}
		ctl := deploymentOf(t, out, engine, "controller")
		path := controllerArg(ctl, "secrets-key-file")
		if path == "" || !hasVolumeFromSecret(ctl, "javo-secrets-key") || !mounts(ctl, strings.TrimSuffix(path, "/kek")) {
			t.Fatalf("%s: controller args %v, volumes %+v", engine, ctl.Spec.Template.Spec.Containers[0].Args, ctl.Spec.Template.Spec.Volumes)
		}
		if engine == "postgres" {
			gw := deploymentOf(t, out, engine, "gateway")
			if hasVolumeFromSecret(gw, "javo-secrets-key") || controllerArg(gw, "secrets-key-file") != "" {
				t.Fatal("the gateway gets the key-encryption key")
			}
		}
	}
}

// An admin who keeps the key elsewhere (a GitOps tool that cannot look up the
// live Secret, or one made by hand) names it; the chart then makes none.
func TestSecretsKeyFromAnExistingSecret(t *testing.T) {
	out, err := render(t, "--set", "secrets.existingKeySecret=my-kek")
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	if kinds := kindsOf(t, out); slices.Contains(kinds["Secret"], "javo-secrets-key") {
		t.Fatal("the chart made its own key as well")
	}
	if d := deploymentOf(t, out, "sqlite", "controller"); !hasVolumeFromSecret(d, "my-kek") {
		t.Fatalf("volumes %+v", d.Spec.Template.Spec.Volumes)
	}
}

// The key file is mounted 0400 and owned by root; the controller runs as a
// non-root user, so it can read the key only through the pod's fsGroup
// (kubelet gives that group read on the file). Without it the controller
// cannot start.
func TestControllerCanReadTheSecretsKey(t *testing.T) {
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

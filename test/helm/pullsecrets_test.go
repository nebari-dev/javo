package helm

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// podSpecs returns, by "Kind/name", the pod spec of every Deployment and
// CronJob in the render.
func podSpecs(t *testing.T, out string) map[string]corev1.PodSpec {
	t.Helper()
	specs := map[string]corev1.PodSpec{}
	for _, u := range manifests(t, out) {
		switch u.GetKind() {
		case "Deployment":
			var d appsv1.Deployment
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &d); err != nil {
				t.Fatal(err)
			}
			specs["Deployment/"+d.Name] = d.Spec.Template.Spec
		case "CronJob":
			var c batchv1.CronJob
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &c); err != nil {
				t.Fatal(err)
			}
			specs["CronJob/"+c.Name] = c.Spec.JobTemplate.Spec.Template.Spec
		}
	}
	return specs
}

// A private image: the chart's own pods pull it with the named Secrets, the
// controller is told to give them to every pod it makes with the image, and
// it may read those Secrets (by name only) to copy them into app namespaces.
func TestImagePullSecretsReachEveryPodAndTheController(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			args := append([]string{"--set", "imagePullSecrets[0].name=ghcr", "--set", "imagePullSecrets[1].name=mirror",
				"--set", "database.backup.schedule=0 3 * * *"}, engineArgs(engine)...)
			out, err := render(t, args...)
			if err != nil {
				t.Fatalf("render: %v\n%s", err, out)
			}
			want := []corev1.LocalObjectReference{{Name: "ghcr"}, {Name: "mirror"}}
			specs := podSpecs(t, out)
			if len(specs) == 0 {
				t.Fatal("no pods in the render")
			}
			for name, spec := range specs {
				if !slices.Equal(spec.ImagePullSecrets, want) {
					t.Errorf("%s: imagePullSecrets = %v, want %v", name, spec.ImagePullSecrets, want)
				}
			}
			ca := controllerArgs(t, out)
			for _, a := range []string{"--image-pull-secret=ghcr", "--image-pull-secret=mirror"} {
				if !slices.Contains(ca, a) {
					t.Errorf("controller args %v lack %s", ca, a)
				}
			}
			account := serviceAccount(engine, "javo-controller")
			for _, name := range []string{"ghcr", "mirror"} {
				if !canIn(t, out, account, "javo-system", "secrets", "get", name) {
					t.Errorf("%s may not get the pull secret %s in the release namespace", account, name)
				}
			}
			if canIn(t, out, account, "javo-system", "secrets", "list", "") || canIn(t, out, account, "javo-system", "secrets", "get", "javo-anything") {
				t.Errorf("%s may read Secrets of the release namespace other than the pull secrets", account)
			}
		})
	}
}

// No pull secrets: no pod names any and the controller gets no flag.
func TestNoImagePullSecretsByDefault(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		out, err := render(t, append([]string{"--set", "database.backup.schedule=0 3 * * *"}, engineArgs(engine)...)...)
		if err != nil {
			t.Fatalf("render: %v\n%s", err, out)
		}
		for name, spec := range podSpecs(t, out) {
			if spec.ImagePullSecrets != nil {
				t.Errorf("%s/%s: imagePullSecrets = %v", engine, name, spec.ImagePullSecrets)
			}
		}
		for _, a := range controllerArgs(t, out) {
			if len(a) > 20 && a[:20] == "--image-pull-secret=" {
				t.Errorf("%s: controller has %s", engine, a)
			}
		}
	}
}

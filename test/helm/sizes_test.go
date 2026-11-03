package helm

import (
	"io"
	"path"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/nebari-dev/javo/internal/sizes"
)

// object decodes the manifest's kind/name object into into.
func object(t *testing.T, manifest, kind, name string, into any) bool {
	t.Helper()
	d := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	for {
		var u unstructured.Unstructured
		if err := d.Decode(&u.Object); err != nil {
			if err == io.EOF {
				return false
			}
			t.Fatal(err)
		}
		if u.GetKind() == kind && u.GetName() == name {
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, into); err != nil {
				t.Fatal(err)
			}
			return true
		}
	}
}

// sizesFileOf returns the ConfigMap data the deployment's --sizes-file flag
// points at, following the flag through the volume mount to the ConfigMap.
func sizesFileOf(t *testing.T, manifest, deployment string) string {
	t.Helper()
	var dep appsv1.Deployment
	if !object(t, manifest, "Deployment", deployment, &dep) {
		t.Fatalf("no deployment %s", deployment)
	}
	pod := dep.Spec.Template.Spec
	c := pod.Containers[0]
	var file string
	for _, a := range c.Args {
		if v, ok := strings.CutPrefix(a, "--sizes-file="); ok {
			file = v
		}
	}
	if file == "" {
		t.Fatalf("%s: no --sizes-file in %v", deployment, c.Args)
	}
	for _, m := range c.VolumeMounts {
		if path.Dir(file) != path.Clean(m.MountPath) {
			continue
		}
		for _, v := range pod.Volumes {
			if v.Name == m.Name && v.ConfigMap != nil {
				var cm corev1.ConfigMap
				if !object(t, manifest, "ConfigMap", v.ConfigMap.Name, &cm) {
					t.Fatalf("%s: no ConfigMap %s", deployment, v.ConfigMap.Name)
				}
				return cm.Data[path.Base(file)]
			}
		}
	}
	t.Fatalf("%s: %s is not on a ConfigMap volume", deployment, file)
	return ""
}

// Both components read the same sizes file, and the default values give
// Small (the default), Medium and Large.
func TestDefaultSizesReachControllerAndGateway(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		out, err := render(t, engineArgs(engine)...)
		if err != nil {
			t.Fatalf("render: %v\n%s", err, out)
		}
		data := sizesFileOf(t, out, deploymentOf(t, out, engine, "controller").Name)
		if gw := sizesFileOf(t, out, deploymentOf(t, out, engine, "gateway").Name); gw != data {
			t.Fatalf("%s: gateway reads other sizes than the controller:\n%s\n%s", engine, gw, data)
		}
		c, err := sizes.Parse([]byte(data))
		if err != nil {
			t.Fatalf("rendered sizes do not parse: %v\n%s", err, data)
		}
		want := []struct{ name, cpu, mem string }{{"small", "1", "2Gi"}, {"medium", "2", "4Gi"}, {"large", "4", "16Gi"}}
		got := c.List()
		if len(got) != len(want) || c.Default() != "small" {
			t.Fatalf("sizes = %+v, default %q", got, c.Default())
		}
		for i, w := range want {
			if got[i].Name != w.name || !got[i].CPU.Equal(resource.MustParse(w.cpu)) || !got[i].Memory.Equal(resource.MustParse(w.mem)) {
				t.Errorf("size %d = %+v, want %+v", i, got[i], w)
			}
		}
	}
}

// Custom amounts reach the sizes file: by default only platform admins may
// ask for one, with no limit but the largest machine.
func TestCustomSizesReachTheSizesFile(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		out, err := render(t, engineArgs(engine)...)
		if err != nil {
			t.Fatalf("render: %v\n%s", err, out)
		}
		c, err := sizes.Parse([]byte(sizesFileOf(t, out, deploymentOf(t, out, engine, "gateway").Name)))
		if err != nil {
			t.Fatal(err)
		}
		p := c.Custom()
		if !p.Enabled || len(p.Rules) != 1 || len(p.Rules[0].Roles) != 1 || p.Rules[0].Roles[0] != "platform-admin" ||
			len(p.Rules[0].Groups) != 0 || !p.Rules[0].MaxCPU.IsZero() || !p.Rules[0].MaxMemory.IsZero() {
			t.Fatalf("%s: default custom policy = %+v", engine, p)
		}

		out, err = render(t, append([]string{"--set", "customSize.enabled=false"}, engineArgs(engine)...)...)
		if err != nil {
			t.Fatalf("render: %v\n%s", err, out)
		}
		if c, err = sizes.Parse([]byte(sizesFileOf(t, out, deploymentOf(t, out, engine, "gateway").Name))); err != nil || c.Custom().Enabled {
			t.Fatalf("%s: customSize.enabled=false: %+v, %v", engine, c.Custom(), err)
		}
	}
}

// Editing the sizes rolls every Deployment, which read the file at start.
func TestChangingSizesRollsThePods(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		before, err := render(t, engineArgs(engine)...)
		if err != nil {
			t.Fatalf("render: %v\n%s", err, before)
		}
		after, err := render(t, append([]string{"--set", "defaultSize=medium"}, engineArgs(engine)...)...)
		if err != nil {
			t.Fatalf("render: %v\n%s", err, after)
		}
		for _, name := range deploymentNames(engine) {
			var a, b appsv1.Deployment
			object(t, before, "Deployment", name, &a)
			object(t, after, "Deployment", name, &b)
			if a.Spec.Template.Annotations["checksum/sizes"] == "" ||
				a.Spec.Template.Annotations["checksum/sizes"] == b.Spec.Template.Annotations["checksum/sizes"] {
				t.Errorf("%s/%s pod template does not change with the sizes: %v", engine, name, b.Spec.Template.Annotations)
			}
		}
	}
}

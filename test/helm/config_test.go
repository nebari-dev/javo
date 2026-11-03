package helm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/nebari-dev/javo/internal/config"
)

// manifests decodes every document of the render into unstructured objects.
func manifests(t *testing.T, out string) []unstructured.Unstructured {
	t.Helper()
	var all []unstructured.Unstructured
	d := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(out), 4096)
	for {
		var u unstructured.Unstructured
		if err := d.Decode(&u.Object); err != nil {
			return all
		}
		if len(u.Object) > 0 {
			all = append(all, u)
		}
	}
}

// platformConfigMap returns the rendered javo-config ConfigMap.
func platformConfigMap(t *testing.T, out string) corev1.ConfigMap {
	t.Helper()
	for _, u := range manifests(t, out) {
		if u.GetKind() == "ConfigMap" && u.GetName() == "javo-config" {
			var cm corev1.ConfigMap
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &cm); err != nil {
				t.Fatal(err)
			}
			return cm
		}
	}
	t.Fatal("no ConfigMap javo-config in the render")
	return corev1.ConfigMap{}
}

func TestDefaultRenderIsALoadableConfig(t *testing.T) {
	out, err := render(t)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(platformConfigMap(t, out).Data["config.yaml"]), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load of the rendered default: %v", err)
	}
	if p.Namespaces.Mode != "shared" || p.URLs.Mode != "path" || !p.Auth.Local.Enabled {
		t.Fatalf("default config = %+v", p)
	}
}

func TestConfigValuesReachTheConfigFile(t *testing.T) {
	out, err := render(t, "--set", "workspaceNamespace=team-apps", "--set", "config.urls.mode=subdomain",
		"--set", "config.urls.appsDomain=apps.example.com", "--set", "ingress.host=javo.example.com")
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(platformConfigMap(t, out).Data["config.yaml"]), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Namespaces.Shared != "team-apps" || p.URLs.Mode != "subdomain" || p.URLs.AppsDomain != "apps.example.com" ||
		p.URLs.UIHost != "javo.example.com" { // uiHost defaults to the ingress host
		t.Fatalf("config = %+v", p)
	}
}

// A config change must roll every Deployment that runs a component, and each
// must read the file.
func TestConfigIsMountedAndRollsTheDeployments(t *testing.T) {
	for _, engine := range engines {
		deployments := func(args ...string) map[string]appsv1.Deployment {
			out, err := render(t, append(args, engineArgs(engine)...)...)
			if err != nil {
				t.Fatalf("render: %v\n%s", err, out)
			}
			got := map[string]appsv1.Deployment{}
			for _, u := range manifests(t, out) {
				if u.GetKind() != "Deployment" {
					continue
				}
				var d appsv1.Deployment
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &d); err != nil {
					t.Fatal(err)
				}
				got[d.Name] = d
			}
			return got
		}
		before := deployments()
		after := deployments("--set", "config.sessions.ttl=1h")
		for _, name := range deploymentNames(engine) {
			d := before[name]
			if !mounts(d, "/etc/javo") {
				t.Errorf("%s/%s does not mount the config at /etc/javo", engine, name)
			}
			const key = "checksum/config"
			if d.Spec.Template.Annotations[key] == "" {
				t.Errorf("%s/%s has no %s annotation", engine, name, key)
			}
			if d.Spec.Template.Annotations[key] == after[name].Spec.Template.Annotations[key] {
				t.Errorf("%s/%s: changing config did not change %s", engine, name, key)
			}
		}
	}
}

func mounts(d appsv1.Deployment, path string) bool {
	for _, c := range d.Spec.Template.Spec.Containers {
		for _, m := range c.VolumeMounts {
			if m.MountPath == path {
				return true
			}
		}
	}
	return false
}

// ingressHosts returns the hosts of the rendered Ingress rules.
func ingressHosts(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := render(t, append([]string{"--set", "ingress.enabled=true", "--set", "ingress.host=javo.example.com"}, args...)...)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	for _, u := range manifests(t, out) {
		if u.GetKind() != "Ingress" {
			continue
		}
		var ing networkingv1.Ingress
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &ing); err != nil {
			t.Fatal(err)
		}
		var hosts []string
		for _, r := range ing.Spec.Rules {
			hosts = append(hosts, r.Host)
		}
		return hosts
	}
	t.Fatal("no Ingress in the render")
	return nil
}

func TestIngressRoutesTheAppsHostOrTheWildcardAppsDomain(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"UI host only":  {nil, "javo.example.com"},
		"apps host":     {[]string{"--set", "config.urls.appsHost=apps.example.com"}, "javo.example.com,apps.example.com"},
		"apps domain":   {[]string{"--set", "config.urls.mode=subdomain", "--set", "config.urls.appsDomain=apps.example.com"}, "javo.example.com,*.apps.example.com"},
		"same host app": {[]string{"--set", "config.urls.appsHost=javo.example.com"}, "javo.example.com"},
	} {
		if got := strings.Join(ingressHosts(t, tc.args...), ","); got != tc.want {
			t.Errorf("%s: hosts %q, want %q", name, got, tc.want)
		}
	}
}

// The OIDC client secret is read from a Secret the gateway mounts; the
// config file names the mounted file.
func TestOIDCClientSecretIsMountedFromASecret(t *testing.T) {
	out, err := render(t, "--set", "gateway.oidcClientSecret=dev-oidc",
		"--set", "config.auth.oidc.issuer=https://idp.example.com/realms/javo", "--set", "config.auth.oidc.clientID=javo")
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(platformConfigMap(t, out).Data["config.yaml"]), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	file := p.Auth.OIDC.ClientSecretFile
	if file == "" {
		t.Fatal("config has no auth.oidc.clientSecretFile")
	}
	dep := deploymentOf(t, out, "sqlite", "gateway")
	pod := dep.Spec.Template.Spec
	for _, m := range pod.Containers[0].VolumeMounts {
		if filepath.Dir(file) != filepath.Clean(m.MountPath) {
			continue
		}
		for _, v := range pod.Volumes {
			if v.Name == m.Name && v.Secret != nil && v.Secret.SecretName == "dev-oidc" {
				for _, it := range v.Secret.Items {
					if it.Path == filepath.Base(file) {
						return
					}
				}
				t.Fatalf("Secret volume %s does not project %s: %+v", v.Name, filepath.Base(file), v.Secret.Items)
			}
		}
	}
	t.Fatalf("%s is not in a mount of Secret dev-oidc: %+v", file, pod.Containers[0].VolumeMounts)
}

// containerImages turns the Container image template on and off and carries
// the allowed registries to the gateway's config.
func TestContainerImagesValues(t *testing.T) {
	load := func(out string) config.Platform {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(platformConfigMap(t, out).Data["config.yaml"]), 0o600); err != nil {
			t.Fatal(err)
		}
		p, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	out, err := render(t)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	if _, ok := renderedTemplates(t, out)["container"]; !ok {
		t.Fatal("default render has no container template")
	}
	if p := load(out); !p.ContainerImages.IsEnabled() || len(p.ContainerImages.AllowedRegistries) != 0 {
		t.Fatalf("default containerImages = %+v", p.ContainerImages)
	}

	out, err = render(t, "--set", "containerImages.enabled=false", "--set", "containerImages.allowedRegistries={ghcr.io/example,registry.example.com}")
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	if _, ok := renderedTemplates(t, out)["container"]; ok {
		t.Fatal("container template rendered while containerImages.enabled=false")
	}
	p := load(out)
	if p.ContainerImages.IsEnabled() || strings.Join(p.ContainerImages.AllowedRegistries, ",") != "ghcr.io/example,registry.example.com" {
		t.Fatalf("containerImages = %+v", p.ContainerImages)
	}
}

// The clone image is pinned by digest in the chart, and an operator can point
// it at a mirror.
func TestCloneImageReachesTheConfigFile(t *testing.T) {
	load := func(args ...string) config.Platform {
		out, err := render(t, args...)
		if err != nil {
			t.Fatalf("render: %v\n%s", err, out)
		}
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(platformConfigMap(t, out).Data["config.yaml"]), 0o600); err != nil {
			t.Fatal(err)
		}
		p, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if got := load().Source.CloneImage; !strings.Contains(got, "git-sync/git-sync:v") || !strings.Contains(got, "@sha256:") {
		t.Fatalf("default clone image %q is not a git-sync tag pinned by digest", got)
	}
	if got := load("--set", "source.cloneImage=registry.example.com/git-sync:1").Source.CloneImage; got != "registry.example.com/git-sync:1" {
		t.Fatalf("clone image = %q", got)
	}
}

// Previews are off by default, and the chart's settings reach the config file.
func TestPreviewSettingsReachTheConfigFile(t *testing.T) {
	load := func(args ...string) config.Platform {
		out, err := render(t, args...)
		if err != nil {
			t.Fatalf("render: %v\n%s", err, out)
		}
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(platformConfigMap(t, out).Data["config.yaml"]), 0o600); err != nil {
			t.Fatal(err)
		}
		p, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := load().Source.Previews; p.Enabled {
		t.Fatalf("previews are on by default: %+v", p)
	}
	p := load("--set", "source.previews.enabled=true", "--set", "source.previews.pollInterval=5m", "--set", "source.previews.maxPerApp=4").Source
	if !p.Previews.Enabled || p.Previews.PollInterval.Duration.Minutes() != 5 || p.Previews.MaxPerApp != 4 || p.CloneImage == "" {
		t.Fatalf("source = %+v", p)
	}
}

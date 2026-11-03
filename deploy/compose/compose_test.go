package compose_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type composeFile struct {
	Services map[string]struct {
		Image     string         `json:"image"`
		Profiles  []string       `json:"profiles"`
		Build     *struct{}      `json:"build"`
		DependsOn map[string]any `json:"depends_on"`
	} `json:"services"`
}

type templateFile struct {
	Spec struct {
		Image  string `json:"image"`
		Sample struct {
			Image string `json:"image"`
		} `json:"sample"`
		Build struct {
			Image string `json:"image"`
		} `json:"build"`
	} `json:"spec"`
}

// `docker compose --profile build up` builds every image the chart's built-in
// templates name (the installer copies them into k3s and stops on the first
// one this Docker host lacks), and the installer waits for each build. An
// upstream image pinned by digest is the exception: the installer leaves it
// as it is and k3s pulls it.
func TestComposeBuildsEveryTemplateImage(t *testing.T) {
	b, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var c composeFile
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	built := map[string]string{} // image name without tag -> service
	for name, s := range c.Services {
		if s.Build == nil || len(s.Profiles) == 0 || s.Profiles[0] != "build" {
			continue
		}
		built[strings.SplitN(s.Image, ":", 2)[0]] = name
	}
	installer := c.Services["installer"]

	files, err := filepath.Glob("../helm/javo/files/templates/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no chart templates: %v", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var tpl templateFile
		if err := yaml.Unmarshal(b, &tpl); err != nil {
			t.Fatal(err)
		}
		for _, img := range []string{tpl.Spec.Image, tpl.Spec.Sample.Image, tpl.Spec.Build.Image} {
			if img == "" || strings.Contains(img, "@sha256:") {
				continue
			}
			base := strings.SplitN(img, ":", 2)[0]
			svc, ok := built[base]
			if !ok {
				t.Errorf("%s names %s, which no build service in compose.yaml builds", filepath.Base(f), img)
				continue
			}
			if _, ok := installer.DependsOn[svc]; !ok {
				t.Errorf("the installer does not wait for build service %s (%s)", svc, img)
			}
		}
	}
}

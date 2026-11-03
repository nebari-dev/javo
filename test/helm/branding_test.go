package helm

import (
	"encoding/base64"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/branding"
)

const brandSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><circle cx="5" cy="5" r="5"/></svg>`

// The start of a PNG: enough for its type to be told by its bytes.
var brandPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00")

// loadedConfig loads the rendered javo-config.
func loadedConfig(t *testing.T, out string) config.Platform {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(platformConfigMap(t, out).Data["config.yaml"]), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return c
}

func TestBrandingValuesReachTheConfigAndTheGatewaysFiles(t *testing.T) {
	for _, engine := range engines {
		out, err := render(t, append(engineArgs(engine),
			"--set", "branding.name=Acme Lab", "--set", "branding.accent=#0a7d55",
			"--set", "branding.logo.light.svg="+brandSVG,
			"--set", "branding.logo.dark.url=https://cdn.example.com/logo-dark.svg",
			"--set", "branding.favicon.base64="+base64.StdEncoding.EncodeToString(brandPNG))...)
		if err != nil {
			t.Fatalf("render: %v\n%s", err, out)
		}
		b := loadedConfig(t, out).Branding
		if b.Name != "Acme Lab" || b.Accent != "#0a7d55" || b.Logo.Dark.URL != "https://cdn.example.com/logo-dark.svg" {
			t.Fatalf("%s: branding = %+v", engine, b)
		}

		// The files the config names are the ConfigMap's keys, mounted where
		// the config says.
		var cm corev1.ConfigMap
		if !object(t, out, "ConfigMap", "javo-branding", &cm) {
			t.Fatalf("%s: no ConfigMap javo-branding", engine)
		}
		d := deploymentOf(t, out, engine, "gateway")
		dir := path.Dir(b.Logo.Light.File)
		if !mounts(d, dir) || path.Dir(b.Favicon.File) != dir {
			t.Fatalf("%s: %s does not mount %s (logo %s, favicon %s)", engine, d.Name, dir, b.Logo.Light.File, b.Favicon.File)
		}
		local := t.TempDir()
		for key, data := range cm.BinaryData {
			if err := os.WriteFile(filepath.Join(local, key), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		b.Logo.Light.File = filepath.Join(local, path.Base(b.Logo.Light.File))
		b.Favicon.File = filepath.Join(local, path.Base(b.Favicon.File))
		if _, err := branding.Load(b); err != nil {
			t.Fatalf("%s: the gateway would refuse the rendered files: %v", engine, err)
		}
		if got, _ := os.ReadFile(b.Logo.Light.File); string(got) != brandSVG {
			t.Fatalf("%s: logo file = %q", engine, got)
		}
	}
}

func TestDefaultRenderHasNoBrandingFiles(t *testing.T) {
	out, err := render(t)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	if b := loadedConfig(t, out).Branding; b.Name != "Javo" || b.Logo.Light.Set() || b.Favicon.Set() || b.Accent != "" {
		t.Fatalf("default branding = %+v", b)
	}
	if object(t, out, "ConfigMap", "javo-branding", &corev1.ConfigMap{}) {
		t.Fatal("a ConfigMap javo-branding without any branding file")
	}
}

func TestBrandingImageTakesOneSource(t *testing.T) {
	out, err := render(t, "--set", "branding.logo.light.svg="+brandSVG, "--set", "branding.logo.light.url=https://cdn.example.com/l.svg")
	if err == nil || !strings.Contains(out, "branding.logo.light") {
		t.Fatalf("render with two sources: err %v\n%s", err, out)
	}
}

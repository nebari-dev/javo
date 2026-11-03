package config

import (
	"strings"
	"testing"
)

const localAuth = "auth:\n  local:\n    enabled: true\n"

func TestBrandingDefaultsToJavo(t *testing.T) {
	p, err := load(t, localAuth)
	if err != nil {
		t.Fatal(err)
	}
	b := p.Branding
	if b.Name != "Javo" || b.Accent != "" || b.Logo.Light.Set() || b.Logo.Dark.Set() || b.Favicon.Set() {
		t.Fatalf("branding = %+v", b)
	}
}

func TestBrandingReadsItsSettings(t *testing.T) {
	p, err := load(t, localAuth+`branding:
  name: Acme Lab
  accent: "#0A7d55"
  logo:
    light: {file: /etc/javo-branding/logo-light}
    dark: {url: "https://cdn.example.com/logo-dark.svg"}
  favicon: {file: /etc/javo-branding/favicon}
`)
	if err != nil {
		t.Fatal(err)
	}
	b := p.Branding
	if b.Name != "Acme Lab" || b.Accent != "#0A7d55" || b.Logo.Light.File != "/etc/javo-branding/logo-light" ||
		b.Logo.Dark.URL != "https://cdn.example.com/logo-dark.svg" || b.Favicon.File != "/etc/javo-branding/favicon" {
		t.Fatalf("branding = %+v", b)
	}
}

func TestBrandingRejectsBadValues(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"accent not hex":        {"accent: red", "branding.accent"},
		"accent with css":       {`accent: "#fff; background: url(x)"`, "branding.accent"},
		"accent too short":      {`accent: "#ff"`, "branding.accent"},
		"logo over http":        {"logo:\n    light: {url: \"http://cdn.example.com/logo.svg\"}", "branding.logo.light.url"},
		"logo javascript url":   {"logo:\n    light: {url: \"javascript:alert(1)\"}", "branding.logo.light.url"},
		"logo url with a login": {"logo:\n    light: {url: \"https://u:p@cdn.example.com/l.svg\"}", "branding.logo.light.url"},
		"favicon both":          {"favicon: {url: \"https://cdn.example.com/f.png\", file: /etc/f}", "branding.favicon"},
		"relative file":         {"logo:\n    light: {file: logo.svg}", "branding.logo.light.file"},
		"dark without light":    {"logo:\n    dark: {file: /etc/javo-branding/logo-dark}", "branding.logo.dark"},
		"name too long":         {"name: " + strings.Repeat("x", 65), "branding.name"},
		"name with newline":     {`name: "Acme\nLab"`, "branding.name"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := load(t, localAuth+"branding:\n  "+tc.yaml+"\n")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %s", err, tc.want)
			}
		})
	}
}

func TestBrandingAcceptsShortHexAccent(t *testing.T) {
	if _, err := load(t, localAuth+"branding:\n  accent: \"#0a5\"\n"); err != nil {
		t.Fatal(err)
	}
}

package helm

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// valuesFile writes a values file for render's -f.
func valuesFile(t *testing.T, yaml string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A parent chart (the Nebari pack) cannot compute values for this chart, so
// the hosts and the OIDC issuer and client ID are templates: one domain in
// global values gives all of them, and the Ingress routes the same hosts.
func TestHostAndOIDCValuesAreTemplates(t *testing.T) {
	t.Parallel()
	out, err := render(t, "-f", valuesFile(t, `
global: {domain: example.com}
ingress: {enabled: true}
gateway: {oidcClientSecret: javo-oidc-client}
config:
  auth:
    oidc:
      issuer: "https://keycloak.{{ .Values.global.domain }}/realms/nebari"
      clientID: "{{ .Release.Namespace }}-javo"
  urls:
    uiHost: "javo.{{ .Values.global.domain }}"
    appsHost: "javo-apps.{{ .Values.global.domain }}"
`))
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	c := loadedConfig(t, out)
	if c.URLs.UIHost != "javo.example.com" || c.URLs.AppsHost != "javo-apps.example.com" {
		t.Errorf("urls = %+v", c.URLs)
	}
	if o := c.Auth.OIDC; o.Issuer != "https://keycloak.example.com/realms/nebari" || o.ClientID != "javo-system-javo" {
		t.Errorf("oidc issuer %q, client ID %q", o.Issuer, o.ClientID)
	}
	if c.MCP.PublicURL != "https://javo.example.com" {
		t.Errorf("mcp.publicURL = %q", c.MCP.PublicURL)
	}
	if got := strings.Join(hostsOfIngress(t, out), ","); got != "javo.example.com,javo-apps.example.com" {
		t.Errorf("ingress hosts %q", got)
	}
	if args := controllerArgs(t, out); !slices.Contains(args, "--public-host=javo.example.com") {
		t.Errorf("controller args %v lack --public-host=javo.example.com", args)
	}
}

// Panel and Bokeh check websocket origins against the public host: it is
// the UI host unless set apart.
func TestPublicHostDefaultsToTheUIHost(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"UI host", []string{"--set", "config.urls.uiHost=javo.example.com"}, "--public-host=javo.example.com"},
		{"ingress host", []string{"--set", "ingress.host=javo.example.com"}, "--public-host=javo.example.com"},
		{"explicit", []string{"--set", "config.urls.uiHost=javo.example.com", "--set", "publicHost=localhost:8080"}, "--public-host=localhost:8080"},
	} {
		out, err := render(t, tc.args...)
		if err != nil {
			t.Fatalf("%s: render: %v\n%s", tc.name, err, out)
		}
		if args := controllerArgs(t, out); !slices.Contains(args, tc.want) {
			t.Errorf("%s: controller args %v lack %s", tc.name, args, tc.want)
		}
	}
	out, err := render(t)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	for _, a := range controllerArgs(t, out) {
		if strings.HasPrefix(a, "--public-host") {
			t.Errorf("no host set, yet the controller gets %s", a)
		}
	}
}

// The gateway shows the command an app runs, which may name the public
// host, so a gateway of its own gets the host the controller does.
func TestTheGatewayGetsThePublicHost(t *testing.T) {
	t.Parallel()
	out, err := render(t, append(postgresArgs, "--set", "config.urls.uiHost=javo.example.com")...)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	d := deploymentOf(t, out, "postgres", "gateway")
	if args := d.Spec.Template.Spec.Containers[0].Args; !slices.Contains(args, "--public-host=javo.example.com") {
		t.Errorf("gateway args %v lack --public-host=javo.example.com", args)
	}
}

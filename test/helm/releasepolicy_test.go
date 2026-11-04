package helm

import (
	"slices"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// openPorts are the ports each ingress rule of pol opens: "*" for a rule
// open to every source, otherwise the namespaces it names.
func openPorts(pol networkingv1.NetworkPolicy) map[string][]string {
	out := map[string][]string{}
	for _, r := range pol.Spec.Ingress {
		var ports []string
		for _, p := range r.Ports {
			ports = append(ports, p.Port.String())
		}
		slices.Sort(ports)
		if len(r.From) == 0 {
			out["*"] = append(out["*"], ports...)
		}
		for _, f := range r.From {
			if f.NamespaceSelector != nil {
				for _, ns := range f.NamespaceSelector.MatchExpressions {
					for _, v := range ns.Values {
						out[v] = append(out[v], ports...)
					}
				}
			}
		}
	}
	return out
}

// The release namespace holds the components' pods. Anyone (the ingress,
// apps calling the API, the network probe, kubelet probes) may reach the
// gateway's HTTP port and the health port; the controller's metrics are
// served on loopback only, unless namespaces are named that may scrape them,
// and then only to those.
func TestReleaseNamespaceAdmitsTheGatewayAndHealthPortsOnly(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		for _, c := range []struct {
			args      []string
			bind      string
			namespace string
		}{
			{nil, "127.0.0.1:8081", ""},
			{[]string{"--set-json", `controller.metricsFrom=["monitoring"]`}, ":8081", "monitoring"},
		} {
			out, err := render(t, append(engineArgs(engine), c.args...)...)
			if err != nil {
				t.Fatalf("%s %v: render: %v\n%s", engine, c.args, err, out)
			}
			if got := controllerArg(deploymentOf(t, out, engine, "controller"), "metrics-bind-address"); got != c.bind {
				t.Errorf("%s %v: metrics bind %q, want %q", engine, c.args, got, c.bind)
			}
			var pol networkingv1.NetworkPolicy
			if !object(t, out, "NetworkPolicy", "javo-ingress", &pol) || pol.Namespace != "javo-system" {
				t.Fatalf("%s %v: no NetworkPolicy javo-ingress in the release namespace", engine, c.args)
			}
			sel, err := metav1.LabelSelectorAsSelector(&pol.Spec.PodSelector)
			if err != nil {
				t.Fatal(err)
			}
			pod := deploymentOf(t, out, engine, "controller").Spec.Template.Labels
			if !sel.Matches(labels.Set(pod)) || sel.Matches(labels.Set{"app": "postgres"}) {
				t.Errorf("%s: selector %v must select Javo's pods only, not others in the namespace", engine, pol.Spec.PodSelector)
			}
			want := map[string][]string{"*": {"8080", "8082"}}
			if c.namespace != "" {
				want[c.namespace] = []string{"8081"}
			}
			got := openPorts(pol)
			slices.Sort(got["*"])
			if len(got) != len(want) || !slices.Equal(got["*"], want["*"]) || !slices.Equal(got[c.namespace], want[c.namespace]) {
				t.Errorf("%s %v: open ports %v, want %v", engine, c.args, got, want)
			}
		}
	}
	out, err := render(t, "--set", "networkPolicy.enabled=false", "--set", "networkPolicy.iUnderstandAppsAreUnauthenticated=true")
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	if object(t, out, "NetworkPolicy", "javo-ingress", &networkingv1.NetworkPolicy{}) {
		t.Error("networkPolicy.enabled=false still renders javo-ingress")
	}
}

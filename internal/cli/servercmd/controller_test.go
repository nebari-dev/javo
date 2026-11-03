package servercmd

import (
	"slices"
	"testing"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/types"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/namespace"
)

func TestParseAppRoleBindings(t *testing.T) {
	got, err := parseAppRoleBindings([]string{"rel-javo-gateway-apps=javo-system/javo-gateway"})
	if err != nil {
		t.Fatal(err)
	}
	want := []namespace.Binding{{ClusterRole: "rel-javo-gateway-apps", ServiceAccount: types.NamespacedName{Namespace: "javo-system", Name: "javo-gateway"}}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v", got)
	}
	for _, bad := range []string{"role", "role=sa", "=ns/sa", "role=/sa", "role=ns/"} {
		if _, err := parseAppRoleBindings([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPublicURLIsTheUIHostElseThePublicHost(t *testing.T) {
	for _, tc := range []struct {
		urls       config.URLs
		publicHost string
		want       string
	}{
		{config.URLs{UIHost: "javo.example.com"}, "ignored.example.com", "https://javo.example.com"},
		{config.URLs{UIHost: "javo.localhost:8080", Scheme: "http"}, "", "http://javo.localhost:8080"},
		{config.URLs{}, "javo.example.org", "https://javo.example.org"},
		{config.URLs{}, "", ""},
	} {
		if got := publicURL(tc.urls, tc.publicHost); got != tc.want {
			t.Errorf("publicURL(%+v, %q) = %q, want %q", tc.urls, tc.publicHost, got, tc.want)
		}
	}
}

// The chart always passes --egress-deny-cidrs, so an empty value must deny
// nothing rather than fall back to the default.
func TestEgressDenyDefaultsToCloudMetadataAndCanBeEmptied(t *testing.T) {
	parse := func(args ...string) []string {
		var o controllerOptions
		f := pflag.NewFlagSet("controller", pflag.ContinueOnError)
		o.register(f)
		if err := f.Parse(args); err != nil {
			t.Fatal(err)
		}
		return o.egressDeny
	}
	if got := parse(); !slices.Equal(got, []string{"169.254.169.254/32", "fd00:ec2::254/128", "100.100.100.200/32", "168.63.129.16/32"}) {
		t.Errorf("default = %v", got)
	}
	if got := parse("--egress-deny-cidrs="); len(got) != 0 {
		t.Errorf("empty flag = %v, want none", got)
	}
}

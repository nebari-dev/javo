//go:build !local

package servercmd

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/types"

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

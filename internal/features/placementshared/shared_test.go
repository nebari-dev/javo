package placementshared_test

import (
	"testing"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/features/placementshared"
)

func TestEveryoneSharesOneNamespace(t *testing.T) {
	pl := placementshared.New(config.Namespaces{Shared: "javo-apps"})
	for _, id := range []string{"alice-1a2b3c4d", "bob-5e6f7a8b"} {
		p := authz.Principal{ID: id}
		if ns, err := pl.NamespaceFor(p, ""); err != nil || ns != "javo-apps" {
			t.Fatalf("%s: NamespaceFor = %q, %v", id, ns, err)
		}
		if got := pl.Namespaces(p); len(got) != 1 || got[0] != "javo-apps" {
			t.Fatalf("%s: Namespaces = %v", id, got)
		}
	}
	if d := pl.Desired(); len(d) != 1 || d[0].Name != "javo-apps" {
		t.Fatalf("Desired = %+v", d)
	}
}

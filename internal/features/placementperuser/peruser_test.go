package placementperuser_test

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/features/placementperuser"
)

func TestEachPersonHasTheirOwnNamespace(t *testing.T) {
	pl := placementperuser.New(config.Namespaces{Prefix: "javo"})
	alice := authz.Principal{ID: "alice-1a2b3c4d"}
	bob := authz.Principal{ID: "bob-5e6f7a8b"}

	ns, err := pl.NamespaceFor(alice, "")
	if err != nil || ns != "javo-alice-1a2b3c4d" {
		t.Fatalf("NamespaceFor(alice) = %q, %v", ns, err)
	}
	if got := pl.Namespaces(alice); len(got) != 1 || got[0] != ns {
		t.Fatalf("Namespaces(alice) = %v", got)
	}
	if got := pl.Namespaces(bob); len(got) != 1 || got[0] == ns {
		t.Fatalf("Namespaces(bob) = %v, must not contain alice's %q", got, ns)
	}
}

func TestSetFollowsTheIdentities(t *testing.T) {
	pl := placementperuser.New(config.Namespaces{Prefix: "javo"})
	if d := pl.Desired(); len(d) != 0 {
		t.Fatalf("Desired = %+v, want none: the controller asks per Identity", d)
	}
}

func TestLongIdentityNameStillGivesADNSLabel(t *testing.T) {
	pl := placementperuser.New(config.Namespaces{Prefix: "javo"})
	ns, err := pl.NamespaceFor(authz.Principal{ID: strings.Repeat("a", 70)}, "")
	if err != nil {
		t.Fatal(err)
	}
	if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
		t.Fatalf("%q: %v", ns, errs)
	}
}

func TestNobodyHasNoNamespace(t *testing.T) {
	pl := placementperuser.New(config.Namespaces{Prefix: "javo"})
	if _, err := pl.NamespaceFor(authz.Principal{Anonymous: true}, ""); err == nil {
		t.Fatal("anonymous got a namespace")
	}
	if got := pl.Namespaces(authz.Principal{}); len(got) != 0 {
		t.Fatalf("Namespaces(anonymous) = %v", got)
	}
}

package registry

import (
	"slices"
	"testing"
)

type item string

func (i item) ID() string { return string(i) }

func TestGetReturnsRegistered(t *testing.T) {
	r := New[item]()
	r.Register("local")
	if got, ok := r.Get("local"); !ok || got != "local" {
		t.Fatalf("Get(local) = %q, %v", got, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("Get(missing) found something")
	}
}

func TestDuplicateRegisterPanics(t *testing.T) {
	r := New[item]()
	r.Register("oidc")
	defer func() {
		if recover() == nil {
			t.Fatal("second Register with the same id did not panic")
		}
	}()
	r.Register("oidc")
}

func TestAllIsSortedByID(t *testing.T) {
	r := New[item]()
	for _, id := range []item{"subdomain", "path", "per-user"} {
		r.Register(id)
	}
	if got, want := r.All(), []item{"path", "per-user", "subdomain"}; !slices.Equal(got, want) {
		t.Fatalf("All() = %v, want %v", got, want)
	}
}

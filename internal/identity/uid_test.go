package identity

import (
	"context"
	"testing"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func TestPodUIDOfReadsThePeopleTable(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)
	alice, err := s.People(store.IDRange{First: 70000, Last: 70999}).Ensure(ctx, store.Claims{Issuer: kcIssuer, Subject: "alice", Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.People(store.IDRange{}).Ensure(ctx, store.Claims{Issuer: kcIssuer, Subject: "bob", Username: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	people := s.People(store.IDRange{})
	for _, tc := range []struct {
		name, mode, owner string
		uid               int64
		ok                bool
	}{
		{"fixed mode ignores the person", config.UIDModeFixed, alice.Name, podspec.FixedUID, true},
		{"allocated uid", config.UIDModeAllocated, alice.Name, alice.UID, true},
		{"not allocated yet", config.UIDModeAllocated, bob.Name, 0, false},
		{"no such person", config.UIDModeAllocated, "carol-1a2b3c4d", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uid, ok, err := PodUIDOf(ctx, people, tc.mode, tc.owner)
			if err != nil || uid != tc.uid || ok != tc.ok {
				t.Fatalf("PodUIDOf = %d, %v, %v; want %d, %v", uid, ok, err, tc.uid, tc.ok)
			}
		})
	}
	if alice.UID < 70000 || alice.UID > 70999 {
		t.Fatalf("alice uid %d is outside the range", alice.UID)
	}
}

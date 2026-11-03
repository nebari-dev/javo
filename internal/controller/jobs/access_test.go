package jobs

import (
	"context"
	"testing"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// A scheduled run has no login, so the owner's access is decided from their
// person row: the groups the provider reported at their last login and their
// name, by the same rule as the UI (authz UseTemplate).
func TestPeopleAccessDecidesFromTheGroupSnapshot(t *testing.T) {
	s := storetest.New(t)
	people := s.People(store.IDRange{})
	person := func(subject string, groups ...string) string {
		t.Helper()
		p, err := people.Ensure(bg, store.Claims{Issuer: "https://idp.example.com", Subject: subject, Username: subject, Groups: groups})
		if err != nil {
			t.Fatal(err)
		}
		return p.Name
	}
	alice, bob, dora := person("alice", "data-team"), person("bob"), person("dora", "admins")
	var roles config.Roles
	roles.PlatformAdmin.Groups = []string{"admins"}
	a := PeopleAccess{People: people, Roles: roles}

	open := &model.Template{}
	byGroup := &model.Template{Spec: model.TemplateSpec{AllowedGroups: []string{"data-team"}}}
	byUser := &model.Template{Spec: model.TemplateSpec{AllowedUsers: []string{alice}}}
	for _, tc := range []struct {
		name, owner string
		tpl         *model.Template
		want        bool
	}{
		{"open template", bob, open, true},
		{"in the allowed group", alice, byGroup, true},
		{"not in the allowed group", bob, byGroup, false},
		{"named in allowedUsers", alice, byUser, true},
		{"not named in allowedUsers", bob, byUser, false},
		{"platform admin", dora, byUser, true},
		{"no such person", "carol-1a2b3c4d", open, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := a.MayUse(context.Background(), tc.owner, tc.tpl)
			if err != nil || got != tc.want {
				t.Fatalf("MayUse = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

// Scheduled runs read the snapshot, not a live lookup: a later login that
// drops a group takes access away from the next evaluation.
func TestScheduledRunUsesGroupSnapshot(t *testing.T) {
	s := storetest.New(t)
	people := s.People(store.IDRange{})
	claims := store.Claims{Issuer: "https://idp.example.com", Subject: "alice", Username: "alice", Groups: []string{"data-team"}}
	p, err := people.Ensure(bg, claims)
	if err != nil {
		t.Fatal(err)
	}
	a := PeopleAccess{People: people}
	gated := &model.Template{Spec: model.TemplateSpec{AllowedGroups: []string{"data-team"}}}
	if ok, err := a.MayUse(bg, p.Name, gated); err != nil || !ok {
		t.Fatalf("with the group: %v, %v", ok, err)
	}
	claims.Groups = nil
	if _, err := people.Ensure(bg, claims); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.MayUse(bg, p.Name, gated); err != nil || ok {
		t.Fatalf("after logging in without the group: %v, %v; want denied", ok, err)
	}
}

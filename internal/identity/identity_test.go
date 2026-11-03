package identity

import (
	"testing"

	"github.com/nebari-dev/javo/internal/model"
)

const kcIssuer = "https://kc.example.com/realms/javo"

func TestPrincipalOfUsesTheGroupsOfTheLastSignIn(t *testing.T) {
	person := model.Person{
		Name:     "alice-1a2b3c4d",
		Subjects: []model.Subject{{Issuer: "https://idp.example.com", Subject: "s-1"}},
		Username: "alice", Email: "alice@example.com", DisplayName: "Alice",
		Groups: []string{"data"}, ProviderRoles: []string{"gpu-users"},
	}
	p := PrincipalOf(person)
	if p.ID != "alice-1a2b3c4d" || p.Username != "alice" || p.Email != "alice@example.com" || p.DisplayName != "Alice" ||
		p.Issuer != "https://idp.example.com" || len(p.Groups) != 1 || p.Groups[0] != "data" || p.Anonymous ||
		len(p.ProviderRoles) != 1 || p.ProviderRoles[0] != "gpu-users" {
		t.Fatalf("principal %+v", p)
	}
}

func TestForStoreKeepsWhatThePeopleTableHolds(t *testing.T) {
	c := Claims{Issuer: kcIssuer, Subject: "s-1", Username: "alice", Email: "alice@example.com", DisplayName: "Alice", Picture: "https://example.com/a.png", Groups: []string{"data"}, ProviderRoles: []string{"gpu-users"}}
	got := c.ForStore()
	if got.Issuer != kcIssuer || got.Subject != "s-1" || got.Username != "alice" || got.Email != "alice@example.com" || got.DisplayName != "Alice" || len(got.Groups) != 1 ||
		len(got.ProviderRoles) != 1 || got.ProviderRoles[0] != "gpu-users" {
		t.Fatalf("store claims %+v", got)
	}
}

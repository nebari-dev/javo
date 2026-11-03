package authoidc_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/nebari-dev/javo/internal/features/authoidc"
)

func TestExtractKeepsGroupsVerbatim(t *testing.T) {
	raw := map[string]any{
		"iss": "https://idp.example.com/realms/javo", "sub": "u-1",
		"preferred_username": "alice", "email": "alice@example.com", "name": "Alice Example",
		"groups": []any{"/org/data-science", "/org/ml"},
	}
	c, err := authoidc.Extract(raw, "preferred_username", "groups", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Issuer != "https://idp.example.com/realms/javo" || c.Subject != "u-1" || c.Username != "alice" ||
		c.Email != "alice@example.com" || c.DisplayName != "Alice Example" {
		t.Fatalf("claims %+v", c)
	}
	if want := []string{"/org/data-science", "/org/ml"}; !reflect.DeepEqual(c.Groups, want) {
		t.Fatalf("groups %v, want %v", c.Groups, want)
	}
}

func TestExtractReadsThePicture(t *testing.T) {
	c, err := authoidc.Extract(map[string]any{"sub": "u-1", "picture": "https://example.com/a.png"}, "preferred_username", "groups", "")
	if err != nil || c.Picture != "https://example.com/a.png" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestExtractWalksADottedGroupsPath(t *testing.T) {
	raw := map[string]any{
		"sub":             "u-1",
		"resource_access": map[string]any{"javo": map[string]any{"roles": []any{"Admin", "admin"}}},
	}
	c, err := authoidc.Extract(raw, "preferred_username", "resource_access.javo.roles", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Admin", "admin"}; !reflect.DeepEqual(c.Groups, want) {
		t.Fatalf("groups %v: values are case-sensitive and verbatim", c.Groups)
	}
}

func TestExtractPrefersAClaimNamedWithDots(t *testing.T) {
	raw := map[string]any{"sub": "u-1", "https://example.com/groups": []any{"g"}}
	c, err := authoidc.Extract(raw, "preferred_username", "https://example.com/groups", "")
	if err != nil || len(c.Groups) != 1 || c.Groups[0] != "g" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestExtractAcceptsOneStringAndToleratesAbsentOrWrongShape(t *testing.T) {
	one, err := authoidc.Extract(map[string]any{"sub": "u-1", "groups": "solo"}, "preferred_username", "groups", "")
	if err != nil || len(one.Groups) != 1 || one.Groups[0] != "solo" {
		t.Fatalf("%+v %v", one, err)
	}
	for name, raw := range map[string]map[string]any{
		"absent":     {"sub": "u-1"},
		"wrong type": {"sub": "u-1", "groups": 7},
		"mixed list": {"sub": "u-1", "groups": []any{"ok", 3}},
	} {
		c, err := authoidc.Extract(raw, "preferred_username", "groups", "")
		if err != nil {
			t.Errorf("%s: %v (sign-in must still succeed, the UI explains the missing groups)", name, err)
		}
		if name != "mixed list" && len(c.Groups) != 0 {
			t.Errorf("%s: groups %v", name, c.Groups)
		}
	}
}

func TestExtractReportsGroupsOverage(t *testing.T) {
	for name, raw := range map[string]map[string]any{
		"_claim_names": {"sub": "u-1", "_claim_names": map[string]any{"groups": "src1"}},
		"hasgroups":    {"sub": "u-1", "hasgroups": true},
	} {
		if _, err := authoidc.Extract(raw, "preferred_username", "groups", ""); !errors.Is(err, authoidc.ErrGroupsOverage) {
			t.Errorf("%s: err %v", name, err)
		}
	}
}

func TestExtractNeedsASubject(t *testing.T) {
	if _, err := authoidc.Extract(map[string]any{"preferred_username": "alice"}, "preferred_username", "groups", ""); err == nil {
		t.Fatal("a token without sub identifies nobody")
	}
}

func TestExtractFallsBackToEmailThenSubjectForTheDisplayName(t *testing.T) {
	c, _ := authoidc.Extract(map[string]any{"sub": "u-1", "email": "bob@example.com"}, "preferred_username", "groups", "")
	if c.Username != "bob@example.com" {
		t.Fatalf("username %q", c.Username)
	}
	c, _ = authoidc.Extract(map[string]any{"sub": "u-2"}, "preferred_username", "groups", "")
	if c.Username != "u-2" {
		t.Fatalf("username %q", c.Username)
	}
}

// Groups and the provider's roles are read from two claims at once: a
// Keycloak client's roles sit under resource_access.<client>.roles.
func TestExtractReadsProviderRolesNextToGroups(t *testing.T) {
	raw := map[string]any{
		"sub":    "u-1",
		"groups": []any{"/org/ds"},
		"resource_access": map[string]any{
			"javo":  map[string]any{"roles": []any{"gpu-users"}},
			"other": map[string]any{"roles": []any{"not-ours"}},
		},
	}
	c, err := authoidc.Extract(raw, "preferred_username", "groups", "resource_access.javo.roles")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Groups, []string{"/org/ds"}) || !reflect.DeepEqual(c.ProviderRoles, []string{"gpu-users"}) {
		t.Fatalf("groups %v, provider roles %v", c.Groups, c.ProviderRoles)
	}
	none, err := authoidc.Extract(raw, "preferred_username", "groups", "")
	if err != nil || none.ProviderRoles != nil {
		t.Fatalf("no roles claim configured, got %v %v", none.ProviderRoles, err)
	}
}

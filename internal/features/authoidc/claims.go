package authoidc

import (
	"errors"
	"strings"

	"github.com/nebari-dev/javo/internal/identity"
)

// ErrGroupsOverage means the provider left the groups out of the token and
// points at another place to fetch them (Entra's `_claim_names.groups` or
// `hasgroups`). Sign-in fails with the GroupsOverage reason.
var ErrGroupsOverage = errors.New("groups claim is an overage reference, not a list")

// Extract reads the person's claims from an ID token's claim set. The
// username is for display only. Group and provider role values are kept
// verbatim; a missing or wrongly shaped claim gives none, not an error. An
// empty rolesClaim reads no provider roles.
func Extract(raw map[string]any, usernameClaim, groupsClaim, rolesClaim string) (identity.Claims, error) {
	sub, _ := raw["sub"].(string)
	if sub == "" {
		return identity.Claims{}, errors.New("token has no sub claim")
	}
	if overage(raw) {
		return identity.Claims{}, ErrGroupsOverage
	}
	c := identity.Claims{Subject: sub, Groups: stringList(lookup(raw, groupsClaim))}
	if rolesClaim != "" {
		c.ProviderRoles = stringList(lookup(raw, rolesClaim))
	}
	c.Issuer, _ = raw["iss"].(string)
	c.Email, _ = raw["email"].(string)
	c.DisplayName, _ = raw["name"].(string)
	c.Picture, _ = raw["picture"].(string)
	c.Username, _ = lookup(raw, usernameClaim).(string)
	if c.Username == "" {
		c.Username = c.Email
	}
	if c.Username == "" {
		c.Username = sub
	}
	return c, nil
}

func overage(raw map[string]any) bool {
	if names, ok := raw["_claim_names"].(map[string]any); ok {
		if _, ok := names["groups"]; ok {
			return true
		}
	}
	has, _ := raw["hasgroups"].(bool)
	return has
}

// lookup finds path in raw: a claim whose name contains dots wins, otherwise
// the path is walked through nested objects (resource_access.javo.roles).
func lookup(raw map[string]any, path string) any {
	if v, ok := raw[path]; ok {
		return v
	}
	var cur any = raw
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		if cur, ok = m[part]; !ok {
			return nil
		}
	}
	return cur
}

// stringList accepts one string or a list of strings; other values are
// dropped.
func stringList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

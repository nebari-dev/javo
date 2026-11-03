// Package identity holds what the rest of javo shares about a signed-in person:
// the Claims an auth provider reports, the Principal built from a stored
// person, and the uid a pod runs as. The people themselves live in the
// database (internal/store).
package identity

import (
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// Claims is what an auth provider learned about a person at sign-in.
type Claims struct {
	Issuer, Subject, Username, Email, DisplayName string
	// Picture is the person's avatar image URL; empty when the provider sends none.
	// It lives on the session, not on the person row.
	Picture string
	// Groups are the provider's group values, verbatim.
	Groups []string
	// ProviderRoles are the provider's role values (config rolesClaim), verbatim.
	ProviderRoles []string
}

// ForStore is the part of the claims the people table keeps.
func (c Claims) ForStore() store.Claims {
	return store.Claims{
		Issuer: c.Issuer, Subject: c.Subject, Username: c.Username, Email: c.Email,
		DisplayName: c.DisplayName, Groups: c.Groups, ProviderRoles: c.ProviderRoles,
	}
}

// PrincipalOf is who a person is when no sign-in is at hand (an API token,
// a scheduled run): the groups and provider roles the provider reported at
// the last sign-in.
func PrincipalOf(p model.Person) authz.Principal {
	out := authz.Principal{ID: p.Name, Username: p.Username, Email: p.Email, DisplayName: p.DisplayName, Groups: p.Groups, ProviderRoles: p.ProviderRoles}
	if len(p.Subjects) > 0 {
		out.Issuer = p.Subjects[0].Issuer
	}
	return out
}

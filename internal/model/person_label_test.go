package model

import "testing"

// A person reads as their display name and username, and whether they sign
// in with a local account or through single sign-on: three accounts all
// called alice tell apart.
func TestPersonLabel(t *testing.T) {
	for _, c := range []struct {
		p    Person
		want string
	}{
		{Person{Name: "alice-1a2b3c4d", DisplayName: "Alice Example", Username: "alice", Subjects: []Subject{{Issuer: LocalIssuer, Subject: "alice"}}}, "Alice Example (alice, local)"},
		{Person{Name: "alice-5e6f7a8b", Username: "alice", Subjects: []Subject{{Issuer: "https://idp.example.com", Subject: "x"}}}, "alice (SSO)"},
		{Person{Name: "alice-9c0d1e2f", DisplayName: "alice", Username: "alice", Subjects: []Subject{{Issuer: "https://idp.example.com", Subject: "y"}}}, "alice (SSO)"},
		{Person{Name: "bob-11112222"}, "bob-11112222"},
	} {
		if got := c.p.Label(); got != c.want {
			t.Errorf("%s: %q, want %q", c.p.Name, got, c.want)
		}
	}
}

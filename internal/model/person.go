package model

import "time"

// Subject is one (issuer, subject) pair from an identity provider. The pair,
// never an email or username, is what identifies a person.
type Subject struct {
	Issuer  string
	Subject string
}

// Person is everything about a signed-in person that must survive a restore:
// one row of the people table.
type Person struct {
	ID string
	// Name is the readable, frozen identifier (alice-<hash>); it is the owner
	// value on apps and jobs and the value in sharing lists.
	Name string
	// Subjects lists the provider accounts that are this person. It may grow;
	// the first entry never changes.
	Subjects []Subject
	// Username, Email and DisplayName are for display only and never used in
	// an authorization decision.
	Username    string
	Email       string
	DisplayName string
	// UID is the numeric user id files and processes use; 0 until allocated,
	// then never changed.
	UID int64
	// Groups are the group values the provider reported at last login, kept
	// verbatim. Scheduled runs on group-gated templates read this snapshot.
	Groups []string
	// ProviderRoles are the provider's role values (config rolesClaim) at
	// last login, kept verbatim like Groups.
	ProviderRoles []string
	LastLoginAt   time.Time
}

// Validate checks the rules every saved person satisfies.
func (p *Person) Validate() error {
	var v validator
	if len(p.Subjects) == 0 {
		v.add("spec.subjects", "at least one subject is required")
	}
	for i, s := range p.Subjects {
		if s.Issuer == "" {
			v.add(indexed("spec.subjects", i)+".issuer", "issuer is required")
		}
		if s.Subject == "" {
			v.add(indexed("spec.subjects", i)+".subject", "subject is required")
		}
	}
	v.maxItems("status.groups", len(p.Groups), 200)
	v.maxItems("status.providerRoles", len(p.ProviderRoles), 200)
	return v.err()
}

// ValidateUpdate is Validate plus the rules for changing old into p: the uid
// never changes once set, and the first subject never changes.
func (p *Person) ValidateUpdate(old *Person) error {
	var v validator
	if old.UID != 0 && p.UID != old.UID {
		v.add("spec.posix.uid", "uid is immutable")
	}
	if len(old.Subjects) > 0 && (len(p.Subjects) == 0 || p.Subjects[0] != old.Subjects[0]) {
		v.add("spec.subjects", "the first subject is immutable")
	}
	return v.merge(p.Validate())
}

// LocalIssuer stands in for the identity provider of local accounts.
const LocalIssuer = "local"

// Local is whether the person signs in with a local account.
func (p *Person) Local() bool { return len(p.Subjects) > 0 && p.Subjects[0].Issuer == LocalIssuer }

// Label is how a person reads in a list: "Alice Example (alice, local)",
// "alice (SSO)"; the name when nothing else is known.
func (p *Person) Label() string {
	how := "SSO"
	if p.Local() {
		how = "local"
	}
	switch {
	case p.Username == "" && p.DisplayName == "":
		return p.Name
	case p.DisplayName == "" || p.DisplayName == p.Username:
		return p.Username + " (" + how + ")"
	case p.Username == "":
		return p.DisplayName + " (" + how + ")"
	}
	return p.DisplayName + " (" + p.Username + ", " + how + ")"
}

// Package placement is the extension point for which Kubernetes namespace an
// app lives in: one shared namespace, one per person, or one per team.
// Implementations live in internal/features and are registered in
// internal/cli/wiring.
package placement

import (
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Placement decides the namespace of apps.
type Placement interface {
	ID() string // "shared", "per-user", "per-team"
	// NamespaceFor is where a new app of p goes. team is the team the person
	// picked ("" when they did not). A refusal is a *Refusal.
	NamespaceFor(p authz.Principal, team string) (string, error)
	// Namespaces lists where p's own apps can be.
	Namespaces(p authz.Principal) []string
	// Desired lists the namespaces the controller keeps whatever the
	// identities are. It is empty when the set follows the Identities (per-user):
	// the controller then asks NamespaceFor for each Identity instead.
	Desired() []NamespaceSpec
}

// NamespaceSpec is a namespace the controller keeps.
type NamespaceSpec struct {
	Name                string
	Labels, Annotations map[string]string
}

// Refusal is a NamespaceFor answer the person can act on. Code is the
// catalog message to show next to the team field.
type Refusal struct{ Code reasons.MessageCode }

func (r *Refusal) Error() string { return reasons.Msg(r.Code) }

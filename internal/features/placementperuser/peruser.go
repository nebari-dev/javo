// Package placementperuser gives each person a namespace of their own, named
// from the prefix and the person's Identity name.
package placementperuser

import (
	"errors"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/placement"
	"github.com/nebari-dev/javo/internal/naming"
)

// ID is the placement's registry key and the value of namespaces.mode.
const ID = "per-user"

// Placement is one namespace per Identity.
type Placement struct{ prefix string }

var _ placement.Placement = (*Placement)(nil)

// New returns the placement for cfg.Prefix.
func New(cfg config.Namespaces) *Placement { return &Placement{prefix: cfg.Prefix} }

func (*Placement) ID() string { return ID }

func (p *Placement) NamespaceFor(who authz.Principal, _ string) (string, error) {
	if who.Anonymous || who.ID == "" {
		return "", errors.New("per-user placement: nobody is signed in")
	}
	return naming.NamespaceName(p.prefix, who.ID), nil
}

func (p *Placement) Namespaces(who authz.Principal) []string {
	ns, err := p.NamespaceFor(who, "")
	if err != nil {
		return nil
	}
	return []string{ns}
}

// Desired is empty: the set follows the Identities, which the controller walks.
func (*Placement) Desired() []placement.NamespaceSpec { return nil }

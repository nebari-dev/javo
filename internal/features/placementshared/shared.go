// Package placementshared puts every app in one namespace.
package placementshared

import (
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/placement"
)

// ID is the placement's registry key and the value of namespaces.mode.
const ID = "shared"

// Placement is the shared namespace.
type Placement struct{ ns string }

var _ placement.Placement = (*Placement)(nil)

// New returns the placement for cfg.Shared.
func New(cfg config.Namespaces) *Placement { return &Placement{ns: cfg.Shared} }

func (*Placement) ID() string { return ID }

func (p *Placement) NamespaceFor(authz.Principal, string) (string, error) { return p.ns, nil }

func (p *Placement) Namespaces(authz.Principal) []string { return []string{p.ns} }

func (p *Placement) Desired() []placement.NamespaceSpec {
	return []placement.NamespaceSpec{{Name: p.ns}}
}

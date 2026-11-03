// Package placementperteam gives each configured team a namespace. A person
// is in a team when the team's group is among their groups.
package placementperteam

import (
	"slices"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/placement"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
)

// ID is the placement's registry key and the value of namespaces.mode.
const ID = "per-team"

// Placement is one namespace per team in namespaces.teams.
type Placement struct {
	prefix string
	teams  []config.Team
}

var _ placement.Placement = (*Placement)(nil)

// New returns the placement for cfg.Prefix and cfg.Teams.
func New(cfg config.Namespaces) *Placement {
	return &Placement{prefix: cfg.Prefix, teams: slices.Clone(cfg.Teams)}
}

func (*Placement) ID() string { return ID }

// TeamNamespace is the namespace of team t.
func (p *Placement) TeamNamespace(t config.Team) string {
	return naming.NamespaceName(p.prefix, t.Slug)
}

// teamsOf returns the teams who is in, in the order of the config.
func (p *Placement) teamsOf(who authz.Principal) []config.Team {
	if who.Anonymous || who.ID == "" {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(p.teams), func(t config.Team) bool { return !slices.Contains(who.Groups, t.Group) })
}

func (p *Placement) NamespaceFor(who authz.Principal, team string) (string, error) {
	mine := p.teamsOf(who)
	if team != "" {
		for _, t := range mine {
			if t.Slug == team {
				return p.TeamNamespace(t), nil
			}
		}
		return "", &placement.Refusal{Code: reasons.MsgTeamNotYours}
	}
	switch len(mine) {
	case 0:
		return "", &placement.Refusal{Code: reasons.MsgNoTeam}
	case 1:
		return p.TeamNamespace(mine[0]), nil
	}
	return "", &placement.Refusal{Code: reasons.MsgPickTeam}
}

func (p *Placement) Namespaces(who authz.Principal) []string {
	var out []string
	for _, t := range p.teamsOf(who) {
		out = append(out, p.TeamNamespace(t))
	}
	slices.Sort(out)
	return out
}

func (p *Placement) Desired() []placement.NamespaceSpec {
	out := make([]placement.NamespaceSpec, 0, len(p.teams))
	for _, t := range p.teams {
		out = append(out, placement.NamespaceSpec{Name: p.TeamNamespace(t)})
	}
	return out
}

package actions

import (
	"context"
	"errors"
	"slices"
)

// Change is one field a plan changes, as the review screen shows it.
type Change struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// Planned is a checked proposal: what it does in words and field by field.
type Planned struct {
	Proposal
	// TargetName is the display name of the app the change acts on.
	TargetName string   `json:"targetName,omitempty" doc:"Display name of the app the change acts on"`
	Summary    string   `json:"summary"`
	Changes    []Change `json:"changes"`
	// Destructive is true for a change that cannot be undone (deleting an app).
	Destructive bool `json:"destructive"`
}

// Param describes one parameter an action takes.
type Param struct {
	Name string `json:"name"`
	Doc  string `json:"doc"`
}

// Info describes an action to a model or an agent.
type Info struct {
	ID      string  `json:"id"`
	Summary string  `json:"summary"`
	Params  []Param `json:"params,omitempty"`
}

// Action is one kind of change. Plan and Apply act as the person in ctx,
// through the same checks as the REST API: Plan reads only and fails as the
// API would (an app that is not theirs is not found); Apply makes the change.
type Action interface {
	Info() Info
	Plan(ctx context.Context, p Proposal) (Planned, error)
	Apply(ctx context.Context, p Proposal) error
}

// ErrUnknownAction is returned for a proposal naming no catalog action.
var ErrUnknownAction = errors.New("no such action")

// Catalog is the set of actions, in registration order.
type Catalog struct{ list []Action }

// NewCatalog returns a catalog of as.
func NewCatalog(as ...Action) *Catalog { return &Catalog{list: as} }

// Get returns the action with id.
func (c *Catalog) Get(id string) (Action, bool) {
	i := slices.IndexFunc(c.list, func(a Action) bool { return a.Info().ID == id })
	if i < 0 {
		return nil, false
	}
	return c.list[i], true
}

// Describe lists every action, for a model's or an agent's instructions.
func (c *Catalog) Describe() []Info {
	out := make([]Info, len(c.list))
	for i, a := range c.list {
		out[i] = a.Info()
	}
	return out
}

// Plan checks p against its action.
func (c *Catalog) Plan(ctx context.Context, p Proposal) (Planned, error) {
	a, ok := c.Get(p.Action)
	if !ok {
		return Planned{}, ErrUnknownAction
	}
	return a.Plan(ctx, p)
}

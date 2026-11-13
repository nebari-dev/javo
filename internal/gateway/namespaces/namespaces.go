// Package namespaces finds the namespaces that may hold apps and the one a
// new app of a person goes to.
package namespaces

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/ext/placement"
	"github.com/nebari-dev/javo/internal/gateway/clusterview"
	"github.com/nebari-dev/javo/internal/model"
)

// discoveryTTL is how long the namespaces found by label are reused.
const discoveryTTL = 10 * time.Second

// Finder answers where new apps may go, from the placement and, when the
// placement cannot list its namespaces up front, from the namespaces the
// controller labelled as managed.
type Finder struct {
	pl placement.Placement
	c  clusterview.View

	mu    sync.Mutex
	found []string
	at    time.Time
}

// New returns the finder for pl; c reads Namespaces (only needed
// when pl lists none). Nil where apps do not run on a cluster: then only
// the namespaces pl lists hold apps.
func New(pl placement.Placement, c clusterview.View) *Finder {
	return &Finder{pl: pl, c: c}
}

// All returns every namespace that may hold apps.
func (n *Finder) All(ctx context.Context) ([]string, error) {
	if desired := n.pl.Desired(); len(desired) > 0 {
		names := make([]string, len(desired))
		for i, d := range desired {
			names[i] = d.Name
		}
		return names, nil
	}
	if n.c == nil {
		return nil, nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.found != nil && time.Since(n.at) < discoveryTTL {
		return n.found, nil
	}
	managed, err := n.c.Namespaces(ctx, map[string]string{model.LabelManaged: "true"})
	if err != nil {
		return nil, err
	}
	names := make([]string, len(managed))
	for i := range managed {
		names[i] = managed[i].Name
	}
	slices.Sort(names)
	n.found, n.at = names, time.Now()
	return names, nil
}

// Managed reports whether ns may hold apps. A namespace missing from the
// cached list is read live, so one the controller labelled a moment ago
// counts at once (and is added to the list) instead of after discoveryTTL.
func (n *Finder) Managed(ctx context.Context, ns string) (bool, error) {
	all, err := n.All(ctx)
	if err != nil {
		return false, err
	}
	if slices.Contains(all, ns) {
		return true, nil
	}
	if n.c == nil {
		return false, nil
	}
	switch o, err := n.c.Namespace(ctx, ns); {
	case err != nil:
		return false, err
	case o == nil || o.Labels[model.LabelManaged] != "true":
		return false, nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if i, found := slices.BinarySearch(n.found, ns); n.found != nil && !found {
		// A new slice: callers may still hold the old one.
		n.found = slices.Insert(slices.Clone(n.found), i, ns)
	}
	return true, nil
}

// For is where a new app of p goes; team is the team p picked, if any.
func (n *Finder) For(p authz.Principal, team string) (string, error) {
	return n.pl.NamespaceFor(p, team)
}

// Listed reports whether the placement names its namespaces up front;
// false means they are found by the controller's label.
func (n *Finder) Listed() bool { return len(n.pl.Desired()) > 0 }

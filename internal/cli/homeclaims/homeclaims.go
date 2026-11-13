// Package homeclaims is what the admin commands do to the volume claims
// of people's homes, which only a cluster has: `javo user remove` forgets
// or deletes them and `javo home prune` asks whether any is left. The
// commands need no client of a cluster for it; cli/kubeclient does it
// with one.
package homeclaims

import (
	"context"
	"errors"

	"github.com/nebari-dev/javo/internal/model"
)

// Claims are the claims of homes on a cluster.
type Claims interface {
	// Any reports whether a claim labelled with owner is left in
	// namespace.
	Any(ctx context.Context, namespace, owner string) (bool, error)
	// Forget labels every claim of homes (each claim labelled with their
	// owner in their namespace) model.LabelForgotten.
	Forget(ctx context.Context, homes []model.Home) error
	// Delete deletes the claims of homes: every claim labelled with their
	// owner in their namespace (a home moved between modes has more than
	// one), after the finished storage Jobs that still name them.
	Delete(ctx context.Context, homes []model.Home) error
}

// Factory connects to the cluster a command talks to.
type Factory func() (Claims, error)

// Open connects with f. A command given no way to reach a cluster says
// so.
func Open(f Factory) (Claims, error) {
	if f == nil {
		return nil, errors.New("no Kubernetes client to reach the homes' claims with")
	}
	return f()
}

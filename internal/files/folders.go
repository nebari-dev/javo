package files

import (
	"context"
	"path/filepath"

	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/nebari-dev/javo/internal/model"
)

// HomeFinder returns an owner's home, asking for it as an app does when
// there is none; podstorage.Provisioner is the implementation.
type HomeFinder interface {
	Home(ctx context.Context, namespace, owner string) (model.Home, error)
}

// Folders is the HomePods of homes that are folders of this machine (the
// process executor), for a Lister over Direct. There is no file pod to
// start, keep or delete: Ensure answers with the home's folder as soon as
// the home is ready.
type Folders struct {
	// Homes finds a person's home and asks for a missing one. Required.
	Homes HomeFinder
	// Namespace is where homes are unless a call's context says otherwise
	// (WithNamespace).
	Namespace string
	// Root is the folder every home is a folder of (hosthome.Root). A home
	// whose row names a folder anywhere else is not served. Required.
	Root string
}

var _ HomePods = Folders{}

// Ensure returns the folder of the user's home once the controller has
// made it. Before that it asks for the home if there is none (as starting
// an app does) and returns ErrHomeStarting. A home recorded anywhere but
// directly in Root is ErrHomeConflict.
func (f Folders) Ensure(ctx context.Context, userID string) (string, error) {
	home, err := f.Homes.Home(ctx, namespaceOr(ctx, f.Namespace), userID)
	if err != nil {
		return "", err
	}
	dir := home.Status.ClaimName
	if dir == "" || !home.Status.Prepared || !meta.IsStatusConditionTrue(home.Status.Conditions, model.ConditionReady) {
		return "", ErrHomeStarting
	}
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || filepath.Dir(dir) != filepath.Clean(f.Root) {
		return "", ErrHomeConflict
	}
	return dir, nil
}

// EnsureGroup is Ensure. A host has no group folders, so one asked for is
// ErrNotFound.
func (f Folders) EnsureGroup(ctx context.Context, userID, dir string) (string, error) {
	if dir != "" {
		return "", ErrNotFound
	}
	return f.Ensure(ctx, userID)
}

// Touch does nothing: there is no file pod to keep.
func (Folders) Touch(string) {}

// Release does nothing: a folder is free for an app while it is read.
func (Folders) Release(context.Context, string) error { return nil }

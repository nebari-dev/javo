// Package ids declares how storage learns a person's POSIX ids, the groups
// they belong to and each group's gid. The identity layer provides them; the
// controller wires an implementation in internal/cli/servercmd.
package ids

import (
	"context"
	"errors"

	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/store"
)

// ErrNoUID means the person has no uid yet. It is a state to wait out: the
// identity controller allocates one within seconds of the first sign-in.
var ErrNoUID = errors.New("no uid allocated yet")

// Memberships lists the groups an owner belongs to.
type Memberships interface {
	Groups(ctx context.Context, ownerID string) ([]string, error)
}

// GroupIDs gives a group's gid; false means none is allocated yet.
type GroupIDs interface {
	GID(ctx context.Context, group string) (int64, bool, error)
}

// GIDEnsurer gives a group its gid, allocating one the first time;
// store.Groups implements it.
type GIDEnsurer interface {
	Ensure(ctx context.Context, actor model.Actor, name string) (int64, error)
}

// AllocatedGroups is GroupIDs backed by the groups table: a group gets a gid
// (from identity.gidRange) the first time a folder of it asks, and keeps it,
// since files on disk carry it.
type AllocatedGroups struct{ Groups GIDEnsurer }

var _ GroupIDs = AllocatedGroups{}

// GID returns group's gid, allocating it on first sight.
func (g AllocatedGroups) GID(ctx context.Context, group string) (int64, bool, error) {
	gid, err := g.Groups.Ensure(ctx, model.Actor{System: model.SystemController}, group)
	return gid, err == nil, err
}

// UserIDs gives the uid and gid a person's files and apps use.
type UserIDs interface {
	UID(ctx context.Context, ownerID string) (uid, gid int64, err error)
}

// People is UserIDs and Memberships backed by person rows (store.People).
// With identity.uidMode "fixed" everyone gets the fixed uid; with
// "allocated" the person's allocated uid. The gid is the fixed group in both.
type People struct {
	People identity.PersonByName
	// Mode is identity.uidMode; anything but config.UIDModeAllocated is fixed.
	Mode string
}

var _ UserIDs = People{}

// UID returns ownerID's ids, or ErrNoUID when allocated mode has none yet.
func (p People) UID(ctx context.Context, ownerID string) (int64, int64, error) {
	uid, ok, err := identity.PodUIDOf(ctx, p.People, p.Mode, ownerID)
	switch {
	case err != nil:
		return 0, 0, err
	case !ok:
		return 0, 0, ErrNoUID
	}
	return uid, podspec.FixedGID, nil
}

// PersonGroups is Memberships backed by the groups a person had at last
// sign-in (store.People).
type PersonGroups struct{ People identity.PersonByName }

var _ Memberships = PersonGroups{}

// Groups returns ownerID's groups; someone who never signed in has none.
func (g PersonGroups) Groups(ctx context.Context, ownerID string) ([]string, error) {
	p, err := g.People.ByName(ctx, ownerID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return p.Groups, err
}

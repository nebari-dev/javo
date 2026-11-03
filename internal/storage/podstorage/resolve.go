package podstorage

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/ext/storage"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// defaultScratch applies when no cluster storage view is wired.
const defaultScratch = model.DefaultScratchSize

// Homes finds an owner's home; store.Homes implements it. A missing home is
// store.ErrNotFound.
type Homes interface {
	ByOwner(ctx context.Context, namespace, owner string) (model.Home, error)
}

// Folders lists every shared folder; store.SharedFolders implements it.
type Folders interface {
	List(ctx context.Context) ([]model.SharedFolder, error)
}

// Memberships lists the groups an owner belongs to; ids.PersonGroups
// implements it.
type Memberships interface {
	Groups(ctx context.Context, ownerID string) ([]string, error)
}

// ClusterView is the cluster's storage settings and findings;
// store.StorageView implements it.
type ClusterView interface {
	Cluster(ctx context.Context) (model.ClusterStorage, error)
}

// Resolver reads the state a Layout depends on. App pods, job pods and file
// pods all use it, so they resolve the same home, folders and scratch limit.
type Resolver struct {
	Homes   Homes
	Folders Folders
	// Cluster is the storage view; nil means the defaults and no findings.
	Cluster ClusterView
	// Groups tells which groups an owner is in; nil means none, so no folders.
	Groups Memberships
	// Backends are the claim shapes by mode, for the co-location rule.
	Backends map[model.HomeMode]storage.Backend
}

// HomeMaker makes a home that does not exist yet; store.Homes implements it.
type HomeMaker interface {
	Ensure(ctx context.Context, namespace, owner string, size *resource.Quantity) (model.Home, error)
}

// EnsureHome returns owner's home in ns, making it with the cluster's
// default size when there is none: every pod of a person (an app, a job's
// run) mounts it.
func (r Resolver) EnsureHome(ctx context.Context, m HomeMaker, ns, owner string) (model.Home, error) {
	home, err := r.Homes.ByOwner(ctx, ns, owner)
	if !errors.Is(err, store.ErrNotFound) {
		return home, err
	}
	var size *resource.Quantity
	if r.Cluster != nil {
		cs, err := r.Cluster.Cluster(ctx)
		if err != nil {
			return model.Home{}, err
		}
		size = cs.Spec.Home.Size
	}
	return m.Ensure(ctx, ns, owner, size)
}

// Input returns the Input for owner's pods in namespace ns, without the ids
// (UID, GID) and ReadOnly, which the caller knows.
func (r Resolver) Input(ctx context.Context, ns, owner string) (Input, error) {
	in := Input{}
	switch home, err := r.Homes.ByOwner(ctx, ns, owner); {
	case err == nil:
		in.Home = &home
	case !errors.Is(err, store.ErrNotFound):
		return in, err
	}
	var cs *model.ClusterStorage
	if r.Cluster != nil {
		c, err := r.Cluster.Cluster(ctx)
		if err != nil {
			return in, err
		}
		cs = &c
	}
	in.ScratchMax = resource.MustParse(defaultScratch)
	if cs != nil && cs.Spec.Scratch.SizeLimit != nil {
		in.ScratchMax = *cs.Spec.Scratch.SizeLimit
	}
	if in.Home != nil {
		in.CoLocate = CoLocate(r.Backends, in.Home, cs)
	}
	folders, err := r.folders(ctx, ns, owner)
	in.Folders = folders
	return in, err
}

// CoLocate is true when home's backend needs all of the owner's pods on one
// node: its disk attaches to one node at a time and the cluster has several.
func CoLocate(backends map[model.HomeMode]storage.Backend, home *model.Home, cs *model.ClusterStorage) bool {
	backend, ok := backends[home.Status.Mode]
	return ok && backend.CoLocate(cs != nil && cs.Status.Choice != nil && cs.Status.Choice.SingleNode)
}

// FolderGIDs are the gids of the ready folders of owner's groups: the
// supplemental groups every pod of the owner runs with.
func (r Resolver) FolderGIDs(ctx context.Context, ns, owner string) ([]int64, error) {
	folders, err := r.folders(ctx, ns, owner)
	gids := make([]int64, 0, len(folders))
	for _, f := range folders {
		gids = append(gids, f.Status.GID)
	}
	return gids, err
}

// folders are the ready folders (Ready=True, claim made, prepared, gid
// allocated) of the groups owner is in, ordered by mount directory, one per
// directory as FolderPerDir picks.
func (r Resolver) folders(ctx context.Context, ns, owner string) ([]model.SharedFolder, error) {
	if r.Groups == nil {
		return nil, nil
	}
	groups, err := r.Groups.Groups(ctx, owner)
	if err != nil || len(groups) == 0 {
		return nil, err
	}
	all, err := r.Folders.List(ctx)
	if err != nil {
		return nil, err
	}
	all = slices.DeleteFunc(all, func(f model.SharedFolder) bool { return f.Namespace != ns })
	var ready []model.SharedFolder
	for _, f := range FolderPerDir(all) {
		if slices.Contains(groups, f.Spec.Group) && FolderReady(f) {
			ready = append(ready, f)
		}
	}
	return ready, nil
}

// FolderPerDir is the folder that keeps each mount directory
// (naming.GroupDir), ordered by directory. A second folder there would repeat
// the volume name and mount path and the API would refuse every pod of the
// owner. The oldest folder wins, then the smaller name. The reconciler
// refuses the others, so both agree on the winner.
func FolderPerDir(folders []model.SharedFolder) []model.SharedFolder {
	live := slices.Clone(folders)
	slices.SortFunc(live, func(a, b model.SharedFolder) int {
		return cmp.Or(
			strings.Compare(naming.GroupDir(a.Spec.Group), naming.GroupDir(b.Spec.Group)),
			a.CreatedAt.Compare(b.CreatedAt),
			strings.Compare(a.Name, b.Name),
		)
	})
	return slices.CompactFunc(live, func(a, b model.SharedFolder) bool {
		return naming.GroupDir(a.Spec.Group) == naming.GroupDir(b.Spec.Group)
	})
}

// FolderReady is true when a pod can mount the folder: Ready=True, the claim
// made and prepared, and the group's gid allocated.
func FolderReady(f model.SharedFolder) bool {
	return meta.IsStatusConditionTrue(f.Status.Conditions, model.ConditionReady) &&
		f.Status.Prepared && f.Status.ClaimName != "" && f.Status.GID != 0
}

// FolderReason is why a folder that is not FolderReady cannot be mounted yet:
// the catalog reason on its Ready condition, or FolderPending when it has none
// or names one the catalog does not know.
func FolderReason(f model.SharedFolder) reasons.Code {
	if c := meta.FindStatusCondition(f.Status.Conditions, model.ConditionReady); c != nil && c.Status == metav1.ConditionFalse {
		if _, ok := reasons.Lookup(reasons.Code(c.Reason)); ok {
			return reasons.Code(c.Reason)
		}
	}
	return reasons.FolderPending
}

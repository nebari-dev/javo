package podstorage_test

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/ext/storage"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
)

type backend struct {
	mode     model.HomeMode
	coLocate func(singleNode bool) bool
}

func (b backend) ID() string                { return string(b.mode) }
func (b backend) Mode() model.HomeMode      { return b.mode }
func (backend) Reasons() []reasons.Code     { return nil }
func (b backend) CoLocate(single bool) bool { return b.coLocate(single) }
func (backend) HomeClaim(*model.Home, string, resource.Quantity) *corev1.PersistentVolumeClaim {
	return nil
}
func (backend) FolderClaim(*model.SharedFolder, string, resource.Quantity, bool) (*corev1.PersistentVolumeClaim, error) {
	return nil, nil
}

type groups []string

func (g groups) Groups(context.Context, string) ([]string, error) { return g, nil }

const ns = "javo-apps"

// rows is the database the resolver reads: homes, folders and the cluster's
// storage, any of which may be absent.
type rows struct {
	homes   []model.Home
	folders []model.SharedFolder
	cluster *model.ClusterStorage
}

func (r *rows) ByOwner(_ context.Context, namespace, owner string) (model.Home, error) {
	for _, h := range r.homes {
		if h.Namespace == namespace && h.Spec.Owner == owner {
			return h, nil
		}
	}
	return model.Home{}, store.ErrNotFound
}

func (r *rows) List(context.Context) ([]model.SharedFolder, error) { return r.folders, nil }

func (r *rows) Cluster(context.Context) (model.ClusterStorage, error) {
	if r.cluster == nil {
		var spec model.ClusterStorageSpec
		spec.ApplyDefaults()
		return model.ClusterStorage{Spec: spec}, nil
	}
	return *r.cluster, nil
}

// db holds the given rows: *model.Home, *model.SharedFolder and
// *model.ClusterStorage.
func db(objs ...any) *rows {
	r := &rows{}
	for _, o := range objs {
		switch v := o.(type) {
		case *model.Home:
			r.homes = append(r.homes, *v)
		case *model.SharedFolder:
			r.folders = append(r.folders, *v)
		case *model.ClusterStorage:
			r.cluster = v
		}
	}
	return r
}

// resolver reads src with memberships g (nil: none) and backends.
func resolver(src *rows, g groups, backends map[model.HomeMode]storage.Backend) podstorage.Resolver {
	r := podstorage.Resolver{Homes: src, Folders: src, Cluster: src, Backends: backends}
	if g != nil {
		r.Groups = g
	}
	return r
}

func home(mode model.HomeMode) *model.Home {
	return &model.Home{
		Namespace: ns,
		Spec:      model.HomeSpec{Owner: owner},
		Status:    model.HomeStatus{Mode: mode, ClaimName: naming.HomeClaimName(owner)},
	}
}

func inNS(f model.SharedFolder) *model.SharedFolder {
	f.Namespace = ns
	return &f
}

func storageObj(single bool, scratch string) *model.ClusterStorage {
	cs := &model.ClusterStorage{}
	cs.Status.Choice = &model.StorageChoice{SingleNode: single}
	if scratch != "" {
		cs.Spec.Scratch.SizeLimit = new(resource.Quantity)
		*cs.Spec.Scratch.SizeLimit = resource.MustParse(scratch)
	}
	return cs
}

func TestResolverMountsOnlyReadyFoldersOfTheOwnersGroups(t *testing.T) {
	mine, other := folder("research", 5001), folder("finance", 5003)
	notReady := folder("ops", 5002)
	notReady.Status.Prepared = false
	noGID := folder("lab", 0)
	r := resolver(db(home(model.HomeModePerUser), inNS(mine), inNS(other), inNS(notReady), inNS(noGID)), groups{"research", "ops", "lab"}, nil)
	in, err := r.Input(context.Background(), ns, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Folders) != 1 || in.Folders[0].Spec.Group != "research" {
		t.Fatalf("folders = %+v, want only the ready folder of a group the owner is in", in.Folders)
	}
	if in.Home == nil {
		t.Error("home not resolved")
	}
}

func TestResolverCoLocatesOnlyWhenTheBackendSaysSo(t *testing.T) {
	backends := map[model.HomeMode]storage.Backend{
		model.HomeModePerUser: backend{mode: model.HomeModePerUser, coLocate: func(single bool) bool { return !single }},
		model.HomeModeShared:  backend{mode: model.HomeModeShared, coLocate: func(bool) bool { return false }},
	}
	for _, row := range []struct {
		name   string
		mode   model.HomeMode
		single bool
		want   bool
	}{
		{"per-user disk on several nodes", model.HomeModePerUser, false, true},
		{"per-user disk on one node", model.HomeModePerUser, true, false},
		{"shared drive", model.HomeModeShared, false, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			r := resolver(db(home(row.mode), storageObj(row.single, "")), nil, backends)
			in, err := r.Input(context.Background(), ns, owner)
			if err != nil || in.CoLocate != row.want {
				t.Fatalf("CoLocate = %v, err = %v; want %v", in.CoLocate, err, row.want)
			}
		})
	}
}

func TestResolverScratchLimitComesFromClusterStorageWithADefault(t *testing.T) {
	get := func(objs ...any) resource.Quantity {
		in, err := resolver(db(objs...), nil, nil).Input(context.Background(), ns, owner)
		if err != nil {
			t.Fatal(err)
		}
		return in.ScratchMax
	}
	if got := get(storageObj(true, "5Gi")); got.Cmp(resource.MustParse("5Gi")) != 0 {
		t.Errorf("configured scratch = %s", got.String())
	}
	if got := get(); got.Cmp(resource.MustParse("20Gi")) != 0 {
		t.Errorf("default scratch = %s, want 20Gi", got.String())
	}
}

func TestResolverWithoutAHomeStillLaysOutScratch(t *testing.T) {
	in, err := resolver(db(), nil, nil).Input(context.Background(), ns, owner)
	if err != nil || in.Home != nil || in.CoLocate {
		t.Fatalf("input = %+v, err = %v", in, err)
	}
}

// Two folders for one group (or two groups with one directory name) would give
// the pod duplicate volume names and mount paths, and the API would refuse
// every pod of every member. Only the oldest folder is mounted.
func TestResolverMountsOneFolderPerDirectory(t *testing.T) {
	older, newer := folder("/org/research", 5001), folder("/org/research", 5002)
	older.Name, newer.Name = "a-old", "b-new"
	older.CreatedAt = time.Unix(1000, 0)
	newer.CreatedAt = time.Unix(2000, 0)
	sameDir := folder("/lab/research", 5003)
	sameDir.Name = "c-other"
	sameDir.CreatedAt = time.Unix(3000, 0)
	r := resolver(db(home(model.HomeModePerUser), inNS(newer), inNS(sameDir), inNS(older)), groups{"/org/research", "/lab/research"}, nil)
	in, err := r.Input(context.Background(), ns, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Folders) != 1 || in.Folders[0].Name != "a-old" {
		t.Fatalf("folders = %+v, want only the oldest", in.Folders)
	}
}

// A folder keeps status.prepared after its claim is gone; the reconciler then
// reports Ready=False. Mounting it would leave every pod of every member
// Pending on a missing claim, so only Ready folders are mounted.
func TestResolverSkipsAPreparedFolderThatIsNotReady(t *testing.T) {
	lost := folder("research", 5001)
	meta.SetStatusCondition(&lost.Status.Conditions, metav1.Condition{Type: model.ConditionReady, Status: metav1.ConditionFalse, Reason: string(reasons.FolderPending)})
	r := resolver(db(inNS(lost)), groups{"research"}, nil)
	in, err := r.Input(context.Background(), ns, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Folders) != 0 {
		t.Fatalf("folders = %+v, want none", in.Folders)
	}
}

func folderWith(ready metav1.ConditionStatus, reason string, prepared bool, gid int64) model.SharedFolder {
	f := model.SharedFolder{
		Spec:   model.SharedFolderSpec{Group: "/org/data-science"},
		Status: model.SharedFolderStatus{ClaimName: "folder-x", GID: gid, Prepared: prepared},
	}
	meta.SetStatusCondition(&f.Status.Conditions, metav1.Condition{Type: model.ConditionReady, Status: ready, Reason: reason})
	return f
}

func TestFolderReadyNeedsEveryPieceThePodMountDoes(t *testing.T) {
	if !podstorage.FolderReady(folderWith(metav1.ConditionTrue, "Ready", true, 2001)) {
		t.Error("a ready, prepared folder with a gid is ready")
	}
	for name, f := range map[string]model.SharedFolder{
		"not Ready":    folderWith(metav1.ConditionFalse, string(reasons.FolderPending), true, 2001),
		"not prepared": folderWith(metav1.ConditionTrue, "Ready", false, 2001),
		"no gid":       folderWith(metav1.ConditionTrue, "Ready", true, 0),
	} {
		if podstorage.FolderReady(f) {
			t.Errorf("%s: folder is ready", name)
		}
	}
}

func TestFolderReasonIsTheCatalogReasonOfTheCondition(t *testing.T) {
	f := folderWith(metav1.ConditionFalse, string(reasons.GroupHasNoId), false, 0)
	if got := podstorage.FolderReason(f); got != reasons.GroupHasNoId {
		t.Errorf("got %s", got)
	}
	f = folderWith(metav1.ConditionFalse, "SomethingNotInTheCatalog", false, 0)
	if got := podstorage.FolderReason(f); got != reasons.FolderPending {
		t.Errorf("an unknown reason falls back to FolderPending, got %s", got)
	}
	if got := podstorage.FolderReason(model.SharedFolder{}); got != reasons.FolderPending {
		t.Errorf("no condition yet: got %s", got)
	}
	// Ready but not yet mounted-ready (no gid): the folder is still pending.
	if got := podstorage.FolderReason(folderWith(metav1.ConditionTrue, "Ready", true, 0)); got != reasons.FolderPending {
		t.Errorf("got %s", got)
	}
}

func TestScratchPathsNameWhatIsNotSaved(t *testing.T) {
	got := podstorage.ScratchPaths()
	if len(got) != 2 || got[0] != "~/.cache" || got[1] != podstorage.ScratchDir {
		t.Errorf("got %v", got)
	}
}

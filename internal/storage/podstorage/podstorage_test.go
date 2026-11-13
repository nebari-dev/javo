package podstorage_test

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/workload"
)

const owner = "alice-1a2b3c4d"

func folder(group string, gid int64) model.SharedFolder {
	return model.SharedFolder{
		Name: "f-" + naming.GroupDir(group),
		Spec: model.SharedFolderSpec{Group: group},
		Status: model.SharedFolderStatus{ClaimName: "folder-" + naming.GroupDir(group), GID: gid, Prepared: true,
			Conditions: []metav1.Condition{{Type: model.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready"}}},
	}
}

func input() podstorage.Input {
	return podstorage.Input{
		Home:       &model.Home{Spec: model.HomeSpec{Owner: owner}, Status: model.HomeStatus{ClaimName: "home-" + owner + "-2"}},
		ScratchMax: resource.MustParse("20Gi"),
		UID:        1000, GID: 100,
	}
}

func mountsByPath(l podstorage.Layout) map[string]workload.Mount {
	m := map[string]workload.Mount{}
	for _, v := range l.Mounts {
		m[v.Path] = v
	}
	return m
}

func volumes(l podstorage.Layout) map[string]workload.Volume {
	m := map[string]workload.Volume{}
	for _, v := range l.Volumes {
		m[v.Name] = v
	}
	return m
}

func TestGroupFoldersAreMountedWithSupplementalGroupsNotFSGroup(t *testing.T) {
	in := input()
	in.Folders = []model.SharedFolder{folder("research", 5001), folder("/ops", 5002)}
	l := podstorage.Build(owner, in)

	if g := l.User.Groups; len(g) != 2 || g[0] != 5001 || g[1] != 5002 {
		t.Fatalf("groups = %v", g)
	}
	ms, vs := mountsByPath(l), volumes(l)
	for _, f := range in.Folders {
		m, ok := ms[podstorage.SharedRoot+"/"+naming.GroupDir(f.Spec.Group)]
		if !ok || m.SubPath != "data" || m.ReadOnly {
			t.Fatalf("folder %s mount = %+v (found %v)", f.Spec.Group, m, ok)
		}
		if vs[m.Volume].Source != f.Status.ClaimName {
			t.Errorf("folder %s volume = %+v", f.Spec.Group, vs[m.Volume])
		}
	}
	if strings.Contains(naming.GroupDir("/ops"), "/") {
		t.Errorf("group dir %q is not one path segment", naming.GroupDir("/ops"))
	}
}

func TestReadOnlyLayoutMountsHomeAndFoldersReadOnly(t *testing.T) {
	in := input()
	in.Folders = []model.SharedFolder{folder("research", 5001)}
	in.ReadOnly = true
	l := podstorage.Build(owner, in)
	for path, m := range mountsByPath(l) {
		inShared := strings.HasPrefix(path, podstorage.SharedRoot+"/")
		if (path == podstorage.HomeDir || inShared) && !m.ReadOnly {
			t.Errorf("mount %s is writable", path)
		}
	}
	for name, v := range volumes(l) {
		if v.Source != "" && !v.ReadOnly {
			t.Errorf("claim volume %s is writable", name)
		}
	}
}

func TestScratchIsLocalAndLimited(t *testing.T) {
	l := podstorage.Build(owner, input())
	vs, ms := volumes(l), mountsByPath(l)
	if scratch := vs["scratch"]; scratch.Source != "" || scratch.Limit == nil || scratch.Limit.Cmp(resource.MustParse("20Gi")) != 0 {
		t.Fatalf("scratch volume = %+v", scratch)
	}
	if vs["tmp"].Source != "" || ms["/tmp"].Volume != "tmp" {
		t.Errorf("/tmp = %+v", ms["/tmp"])
	}
	if m := ms[podstorage.ScratchDir]; m.Volume != "scratch" || m.SubPath != "" {
		t.Errorf("/scratch mount = %+v", m)
	}
	if m := ms[podstorage.HomeDir+"/.cache"]; m.Volume != "scratch" || m.SubPath != "cache" {
		t.Errorf("home cache mount = %+v", m)
	}
	env := map[string]string{}
	for _, e := range l.Env {
		env[e.Name] = e.Value
	}
	for _, k := range []string{"XDG_CACHE_HOME", "PIP_CACHE_DIR", "UV_CACHE_DIR", "CONDA_PKGS_DIRS", "CONDA_ENVS_PATH", "PIXI_CACHE_DIR", "npm_config_cache"} {
		if !strings.HasPrefix(env[k], podstorage.ScratchDir+"/") {
			t.Errorf("%s = %q, want a path under %s", k, env[k], podstorage.ScratchDir)
		}
	}
	if env["CONDA_ENVS_PATH"] != "/scratch/envs" {
		t.Errorf("CONDA_ENVS_PATH = %q", env["CONDA_ENVS_PATH"])
	}
}

func TestHomeClaimComesFromTheHomeAndFallsBackToTheFirstGeneration(t *testing.T) {
	claim := func(l podstorage.Layout) string { return volumes(l)["home"].Source }
	if got := claim(podstorage.Build(owner, input())); got != "home-"+owner+"-2" {
		t.Errorf("claim = %q, want the Home's", got)
	}
	in := input()
	in.Home = nil
	if got := claim(podstorage.Build(owner, in)); got != naming.HomeClaimName(owner) {
		t.Errorf("claim = %q, want %q", got, naming.HomeClaimName(owner))
	}
}

func TestRunsAsTheOwnersIds(t *testing.T) {
	in := input()
	in.UID, in.GID = 100500, 100
	l := podstorage.Build(owner, in)
	if l.User.UID != 100500 || l.User.GID != 100 {
		t.Errorf("ids = %d:%d", l.User.UID, l.User.GID)
	}
}

func TestGroupDirIsOneStableDNSLabel(t *testing.T) {
	if got := naming.GroupDir("research"); got != "research" {
		t.Errorf("plain group = %q", got)
	}
	a, b := naming.GroupDir("/Data Team"), naming.GroupDir("data-team")
	if a == b {
		t.Errorf("different groups share a dir: %q", a)
	}
	if a != naming.GroupDir("/Data Team") {
		t.Error("not stable")
	}
	long := naming.GroupDir(strings.Repeat("x", 200))
	if len(long) > 40 {
		t.Errorf("long group dir is %d chars", len(long))
	}
}

// /tmp is the machine's disk too: it has the scratch limit, so filling it
// ends only this workload instead of putting the machine under disk
// pressure. A limit of its own replaces it.
func TestTmpHasTheScratchLimitUnlessItHasItsOwn(t *testing.T) {
	if tmp := volumes(podstorage.Build(owner, input()))["tmp"].Limit; tmp == nil || tmp.Cmp(resource.MustParse("20Gi")) != 0 {
		t.Fatalf("tmp limit = %v, want 20Gi", tmp)
	}
	in := input()
	in.TmpMax = resource.MustParse("2Gi")
	if tmp := volumes(podstorage.Build(owner, in))["tmp"].Limit; tmp == nil || tmp.Cmp(resource.MustParse("2Gi")) != 0 {
		t.Fatalf("tmp limit = %v, want 2Gi", tmp)
	}
}

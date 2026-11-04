package podstorage_test

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
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

func mountsByPath(l podstorage.Layout) map[string]corev1.VolumeMount {
	m := map[string]corev1.VolumeMount{}
	for _, v := range l.Mounts {
		m[v.MountPath] = v
	}
	return m
}

func volumes(l podstorage.Layout) map[string]corev1.Volume {
	m := map[string]corev1.Volume{}
	for _, v := range l.Volumes {
		m[v.Name] = v
	}
	return m
}

func TestGroupFoldersAreMountedWithSupplementalGroupsNotFSGroup(t *testing.T) {
	in := input()
	in.Folders = []model.SharedFolder{folder("research", 5001), folder("/ops", 5002)}
	l := podstorage.Build(owner, in)

	if l.SupplementalGroups[0] != 5001 || l.SupplementalGroups[1] != 5002 || len(l.SupplementalGroups) != 2 {
		t.Fatalf("supplemental groups = %v", l.SupplementalGroups)
	}
	ms, vs := mountsByPath(l), volumes(l)
	for _, f := range in.Folders {
		m, ok := ms[podstorage.SharedRoot+"/"+naming.GroupDir(f.Spec.Group)]
		if !ok || m.SubPath != "data" || m.ReadOnly {
			t.Fatalf("folder %s mount = %+v (found %v)", f.Spec.Group, m, ok)
		}
		if pvc := vs[m.Name].PersistentVolumeClaim; pvc == nil || pvc.ClaimName != f.Status.ClaimName {
			t.Errorf("folder %s volume = %+v", f.Spec.Group, vs[m.Name])
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
		if pvc := v.PersistentVolumeClaim; pvc != nil && !pvc.ReadOnly {
			t.Errorf("claim volume %s is writable", name)
		}
	}
}

func TestCoLocationNeedsTheOwnersOtherPodsOnTheSameNode(t *testing.T) {
	in := input()
	in.CoLocate = true
	aff := podstorage.Build(owner, in).Affinity
	if aff == nil || aff.PodAffinity == nil || len(aff.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution) != 1 {
		t.Fatalf("affinity = %+v", aff)
	}
	term := aff.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0]
	if term.TopologyKey != corev1.LabelHostname || term.LabelSelector.MatchLabels[model.LabelOwner] != owner {
		t.Errorf("term = %+v", term)
	}

	in.CoLocate = false
	if got := podstorage.Build(owner, in).Affinity; got != nil {
		t.Errorf("shared mode must not constrain placement, affinity = %+v", got)
	}
}

func TestScratchIsNodeLocalAndSafeToEvict(t *testing.T) {
	l := podstorage.Build(owner, input())
	if got := l.Annotations["cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes"]; got != "scratch,tmp" {
		t.Errorf("safe-to-evict = %q", got)
	}
	vs, ms := volumes(l), mountsByPath(l)
	scratch := vs["scratch"].EmptyDir
	if scratch == nil || scratch.SizeLimit == nil || scratch.SizeLimit.Cmp(resource.MustParse("20Gi")) != 0 {
		t.Fatalf("scratch volume = %+v", vs["scratch"])
	}
	if vs["tmp"].EmptyDir == nil || ms["/tmp"].Name != "tmp" {
		t.Errorf("/tmp = %+v", ms["/tmp"])
	}
	if m := ms[podstorage.ScratchDir]; m.Name != "scratch" || m.SubPath != "" {
		t.Errorf("/scratch mount = %+v", m)
	}
	if m := ms[podstorage.HomeDir+"/.cache"]; m.Name != "scratch" || m.SubPath != "cache" {
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
	claim := func(l podstorage.Layout) string { return volumes(l)["home"].PersistentVolumeClaim.ClaimName }
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
	if l.RunAsUser != 100500 || l.RunAsGroup != 100 {
		t.Errorf("ids = %d:%d", l.RunAsUser, l.RunAsGroup)
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

// /tmp is node disk too: it has the scratch limit, so filling it evicts only
// this pod instead of putting the node under disk pressure.
func TestTmpHasTheScratchLimit(t *testing.T) {
	tmp := volumes(podstorage.Build(owner, input()))["tmp"].EmptyDir
	if tmp == nil || tmp.SizeLimit == nil || tmp.SizeLimit.Cmp(resource.MustParse("20Gi")) != 0 {
		t.Fatalf("tmp volume = %+v, want a 20Gi size limit", tmp)
	}
}

// One person's pod cannot fill the node's disk (and with it the database and
// everyone else's pods): /tmp has a size limit, and the app container a
// limit on all it writes to node-local disk. The request stays zero so the
// limit does not reserve disk on a small node.
func TestTmpAndTheContainersDiskAreLimited(t *testing.T) {
	in := input()
	in.TmpMax, in.DiskMax = resource.MustParse("2Gi"), resource.MustParse("30Gi")
	l := podstorage.Build(owner, in)
	tmp := volumes(l)["tmp"].EmptyDir
	if tmp == nil || tmp.SizeLimit == nil || tmp.SizeLimit.Cmp(resource.MustParse("2Gi")) != 0 {
		t.Fatalf("/tmp volume = %+v", tmp)
	}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}
	l.Apply(pod, &pod.Spec.Containers[0])
	r := pod.Spec.Containers[0].Resources
	if lim := r.Limits[corev1.ResourceEphemeralStorage]; lim.Cmp(resource.MustParse("30Gi")) != 0 {
		t.Fatalf("ephemeral-storage limit = %s", lim.String())
	}
	if req, ok := r.Requests[corev1.ResourceEphemeralStorage]; !ok || !req.IsZero() {
		t.Fatalf("ephemeral-storage request = %v (set %v), want an explicit 0", req.String(), ok)
	}
}

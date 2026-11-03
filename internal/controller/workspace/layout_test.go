package workspace

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
)

// testLayout is the storage layout of owner "alice" running as uid.
func testLayout(uid int64) podstorage.Layout {
	return podstorage.Build("alice", podstorage.Input{UID: uid, GID: 100})
}

func TestBuildPodAppliesTheStorageLayout(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	folder := model.SharedFolder{
		Spec:   model.SharedFolderSpec{Group: "research"},
		Status: model.SharedFolderStatus{ClaimName: "folder-research", GID: 5001, Prepared: true},
	}
	l := podstorage.Build("alice", podstorage.Input{Folders: []model.SharedFolder{folder}, CoLocate: true, UID: 1000, GID: 100})
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", l, sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.SecurityContext.FSGroup != nil {
		t.Error("an app pod must not set fsGroup: it would re-group every shared folder it mounts")
	}
	if got := pod.Spec.SecurityContext.SupplementalGroups; len(got) != 1 || got[0] != 5001 {
		t.Errorf("supplemental groups = %v", got)
	}
	if pod.Spec.Affinity == nil || pod.Annotations[podstorage.SafeToEvictAnnotation] != "scratch,tmp" {
		t.Errorf("affinity %+v, annotations %v", pod.Spec.Affinity, pod.Annotations)
	}
	c := pod.Spec.Containers[0]
	if !slices.ContainsFunc(c.VolumeMounts, func(m corev1.VolumeMount) bool { return m.MountPath == "/shared/research" }) ||
		!slices.ContainsFunc(c.Env, func(e corev1.EnvVar) bool { return e.Name == "CONDA_ENVS_PATH" && e.Value == "/scratch/envs" }) {
		t.Errorf("mounts %+v env %+v", c.VolumeMounts, c.Env)
	}
}

// An app that runs the image of its creator's choice runs as the image's own
// user, but keeps the home's group so it can write there. The group is a
// supplemental one: containerd refuses a pod that sets runAsGroup without
// runAsUser ("user group is specified without user") and never starts it.
func TestUserImageRunsAsItsOwnUserInTheHomeGroup(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	tpl.Spec.UserImage = true
	ws.Image = "example.com/team/app:1"
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(100500), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if sc := pod.Spec.SecurityContext; sc.RunAsUser != nil || sc.RunAsGroup != nil || !slices.Contains(sc.SupplementalGroups, 100) {
		t.Fatalf("security context = %+v", sc)
	}
}

// A preview runs a pull request its owner has not reviewed: like the home,
// the owner's group folders stay out of its pod.
func TestPreviewPodMountsNoGroupFolder(t *testing.T) {
	t.Parallel()
	ws, tpl := sourceFixtures()
	ws.ParentID, ws.PullNumber = "parent-1", 7
	folder := model.SharedFolder{
		Spec:   model.SharedFolderSpec{Group: "research"},
		Status: model.SharedFolderStatus{ClaimName: "folder-research", GID: 5001, Prepared: true},
	}
	l := podstorage.Build("alice", podstorage.Input{Folders: []model.SharedFolder{folder}, UID: 1000, GID: 100})
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", l, sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "folder-research" {
			t.Fatalf("preview pod has the group folder's claim as volume %s", v.Name)
		}
	}
	if got := pod.Spec.SecurityContext.SupplementalGroups; len(got) != 0 {
		t.Errorf("supplemental groups = %v, want none", got)
	}
}

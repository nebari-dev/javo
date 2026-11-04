package storage

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/storage/probestep"
)

// helperJobBuilders lists every builder of a Job that touches a volume. A
// Job that sets fsGroup re-owns everything its pod mounts (drivers with
// fsGroupPolicy File), so it may mount one volume only. Whoever adds a Job
// builder (folder prepare, migration copy) adds it here.
func helperJobBuilders() map[string]func() *batchv1.Job {
	h := &model.Home{ID: "h-1", Namespace: "javo-apps", Spec: model.HomeSpec{Owner: "alice"}}
	key := client.ObjectKey{Namespace: "javo-apps", Name: "job"}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "probe-claim"}}
	folder := &model.SharedFolder{Name: "data-science", Namespace: "javo-apps",
		Spec: model.SharedFolderSpec{Group: "/org/data-science"}, Status: model.SharedFolderStatus{ClaimName: "folder-data-science", GID: 2001}}

	out := map[string]func() *batchv1.Job{
		"prepare home":   func() *batchv1.Job { return (&HomeReconciler{}).prepareJob(key, h, 1000, 100, true) },
		"prepare folder": func() *batchv1.Job { return (&SharedFolderReconciler{}).prepareJob(key, folder) },
	}
	moving := h.DeepCopy()
	moving.Status = model.HomeStatus{ClaimName: "home-alice", Migration: &model.HomeMigration{NewClaim: "home-alice-2"}}
	out["copy home"] = func() *batchv1.Job { return (&MigrationReconciler{}).copyJob(moving, 1000, 100, []int64{2345}) }
	out["backup home"] = func() *batchv1.Job {
		return (&BackupReconciler{Backups: &HomeBackups{}}).volumeJob(volume{ns: "javo-apps", claim: "home-alice", owner: "alice", uid: 1000, gid: 100}, "job", "backup", true, nil)
	}
	out["restore home"] = func() *batchv1.Job {
		return (&BackupReconciler{Backups: &HomeBackups{}}).volumeJob(volume{ns: "javo-apps", claim: "home-alice", owner: "alice", uid: 1000, gid: 100}, "job", "restore", false, nil)
	}
	for _, step := range []string{probestep.StepPrepare, probestep.StepWrite, probestep.StepCheck, probestep.StepConfirm} {
		out["probe "+step] = func() *batchv1.Job {
			return (&ProbeReconciler{}).buildJob(key, "nonce", step, pvc, 0, nil)
		}
	}
	return out
}

func TestEveryJobThatSetsFSGroupMountsExactlyOneVolume(t *testing.T) {
	t.Parallel()
	sawFSGroup := false
	for name, build := range helperJobBuilders() {
		spec := build().Spec.Template.Spec
		if spec.SecurityContext.FSGroup == nil {
			continue
		}
		sawFSGroup = true
		if len(spec.Volumes) != 1 {
			t.Errorf("%s sets fsGroup and mounts %d volumes, want 1", name, len(spec.Volumes))
		}
	}
	if !sawFSGroup {
		t.Fatal("no helper Job sets fsGroup: the sweep checks nothing")
	}
}

// Service links put every Service of the namespace (other people's app names
// and addresses, in a shared namespace) into a helper's environment.
func TestNoHelperJobGetsServiceLinks(t *testing.T) {
	t.Parallel()
	for name, build := range helperJobBuilders() {
		if l := build().Spec.Template.Spec.EnableServiceLinks; l == nil || *l {
			t.Errorf("%s: service-link variables are on", name)
		}
	}
}

//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"os"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"testing"
	"time"
)

type bucketPage struct {
	Bucket *struct {
		Status struct{ Reason string }
		Jobs   struct {
			Backup, Restore *struct{ Name, Owner string }
		}
		LastRun *struct {
			Run, RunID, Outcome, Database              string
			Homes, HomesFailed, Volumes, VolumesFailed int
		}
	}
}

// ensureBackupJobs makes the backup and restore jobs as the Backups page's
// Set up backups does: ordinary jobs from the "backup" template, through
// the jobs API, owned by the admin.
func ensureBackupJobs(t *testing.T, admin *session) {
	t.Helper()
	var page bucketPage
	admin.json(http.MethodGet, "/api/admin/backups", nil, http.StatusOK, &page)
	if page.Bucket == nil {
		t.Fatal("no bucket: make up with JAVO_E2E_S3=1")
	}
	if page.Bucket.Jobs.Backup == nil {
		admin.json(http.MethodPost, "/api/jobs", map[string]any{
			"displayName": "Backup", "template": "backup", "engine": "command", "command": "/javo backup run",
		}, http.StatusCreated, nil)
	}
	if page.Bucket.Jobs.Restore == nil {
		admin.json(http.MethodPost, "/api/jobs", map[string]any{
			"displayName": "Restore", "template": "backup", "engine": "command", "command": "/javo backup restore-run",
			"parameters": []map[string]string{
				{"name": "from", "type": "string", "default": ""},
				{"name": "person", "type": "string", "default": ""},
				{"name": "mode", "type": "string", "default": "replace"},
			},
		}, http.StatusCreated, nil)
	}
}

// waitJobRun waits until the run runID of job (owned by owner) has ended and
// returns its outcome; it fails the test with the run's log otherwise.
func waitJobRun(t *testing.T, s *session, job, owner, runID string) string {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Minute); ; time.Sleep(3 * time.Second) {
		var d struct {
			Runs []struct{ ID, Outcome, Message string }
		}
		s.json(http.MethodGet, "/api/jobs/"+job+"?owner="+owner, nil, http.StatusOK, &d)
		for _, r := range d.Runs {
			if r.ID == runID && r.Outcome != "" && r.Outcome != "Running" {
				if r.Outcome != "Succeeded" {
					t.Fatalf("run %s of %s: %s %s", runID, job, r.Outcome, r.Message)
				}
				return r.Outcome
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s of %s did not end: %+v", runID, job, d.Runs)
		}
	}
}

// The restore drill for backups to a bucket, on a cluster made with JAVO_E2E_S3=1 (an
// S3-compatible bucket in the cluster): a person's app and a file in their
// home are backed up by a run of the backup job, both are lost, and a run of
// the restore job brings back everything: the app (held, then released)
// and the file.
func TestBucketBackupRestoreDrill(t *testing.T) {
	if os.Getenv("JAVO_E2E_S3") != "1" {
		t.Skip("needs the drill's bucket: make up with JAVO_E2E_S3=1, run with JAVO_E2E_S3=1")
	}
	if !sqliteInstall(t) {
		t.Skip("the Postgres database is copied by its CronJob, not by the backup job")
	}
	c := kube(t)
	admin := adminSession(t)
	ensureBackupJobs(t, admin)
	s := newUser(t)
	me := s.me()
	a, _ := runningSample(t, s, c, me.ID, "e2e drill "+time.Now().Format("150405"))
	content := "drill " + time.Now().Format(time.RFC3339Nano)
	runHomeJob(t, s, c, me.ID, "sh", "-c", `printf '%s' "$1" > /home/user/drill.txt`, "write", content)

	// A home volume from before, with no account and no row: it is found,
	// backed up as the uid that owns its files, and listed as such.
	orphan := "local-oldie-" + time.Now().Format("150405")
	orphanClaim := naming.HomeClaimName(orphan)
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: orphanClaim, Namespace: appsNS,
			Labels: map[string]string{model.LabelOwner: orphan}},
		Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}},
	}
	if err := c.Create(context.Background(), pvc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Its backup Jobs' pods name the claim and keep it until they go.
		_ = c.DeleteAllOf(context.Background(), &batchv1.Job{}, client.InNamespace(appsNS), client.MatchingLabels{model.LabelOwner: orphan},
			client.PropagationPolicy(metav1.DeletePropagationBackground))
		_ = c.Delete(context.Background(), pvc)
		// Forgotten once the volume is gone.
		for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); client.IgnoreNotFound(err) == nil && err != nil {
				break
			}
		}
		javoCLI(t, "home", "prune", "--orphans", "--yes")
	})
	runClaimJob(t, c, orphanClaim, 4321, "sh", "-c", `printf old > /home/user/old.txt`)

	// Back up now: one run of the backup job backs up everything.
	var now struct{ Job, Owner, RunID string }
	admin.json(http.MethodPost, "/api/admin/backups/now", nil, http.StatusCreated, &now)
	waitJobRun(t, admin, now.Job, now.Owner, now.RunID)
	var page bucketPage
	admin.json(http.MethodGet, "/api/admin/backups", nil, http.StatusOK, &page)
	run := page.Bucket.LastRun
	if run == nil || run.RunID != now.RunID || run.Outcome != "Succeeded" || run.Database == "" || run.Homes == 0 || run.HomesFailed != 0 ||
		run.Volumes == 0 || run.VolumesFailed != 0 {
		t.Fatalf("last run = %+v", run)
	}
	// The run's log: one key=value line per event, each step's own events,
	// where it went, and a summary.
	runLog := jobRunLog(t, c, run.Run)
	for _, want := range []string{
		"step=database action=dump file=", "step=database action=upload snapshot=", "dest=s3://javo-e2e/javo/database",
		"step=home person=", "action=start dir=/home/user dest=s3://javo-e2e/javo/homes/", "action=snapshot id=", "uploaded=",
		"action=forget kept=", `person="no account"`, "claim=" + orphanClaim, "SUMMARY",
	} {
		if !strings.Contains(runLog, want) {
			t.Errorf("the run's log lacks %q:\n%s", want, runLog)
		}
	}
	if page.Bucket.Status.Reason != "FullBackupDone" {
		t.Fatalf("status = %+v", page.Bucket.Status)
	}

	// Lose the app and the file.
	if resp, data := s.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d %s", resp.StatusCode, data)
	}
	runHomeJob(t, s, c, me.ID, "rm", "/home/user/drill.txt")

	// Restore everything from that run, as the restore job.
	var restore struct{ Job, Owner, RunID string }
	admin.json(http.MethodPost, "/api/admin/backups/restore", map[string]any{"from": run.Run}, http.StatusCreated, &restore)
	waitJobRun(t, admin, restore.Job, restore.Owner, restore.RunID)

	// The app is back, held under the database backup's name: release it.
	s.waitFor(a.Name, 2*time.Minute, func(a app) bool { return firstReason(a) == "RestoreHeld" })
	releaseRestore(t, admin, run.Database, 600)
	s.waitFor(a.Name, 4*time.Minute, func(a app) bool { return a.Phase == "Running" })
	runHomeJob(t, s, c, me.ID, "sh", "-c", `test "$(cat /home/user/drill.txt)" = "$1"`, "check", content)
}

// jobRunLog is the log of the pod of the job run whose Kubernetes Job is k8sJob.
func jobRunLog(t *testing.T, c client.Client, k8sJob string) string {
	t.Helper()
	var pods corev1.PodList
	if err := c.List(context.Background(), &pods, client.InNamespace(appsNS), client.MatchingLabels{"batch.kubernetes.io/job-name": k8sJob}); err != nil || len(pods.Items) == 0 {
		t.Fatalf("pods of %s: %v", k8sJob, err)
	}
	return podLog(t, &pods.Items[0])
}

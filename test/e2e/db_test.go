//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/controller/manager"
)

// controllerLease is the Lease the controller replicas compete for
// (manager.LeaderElectionID), in the release namespace.
const controllerLease = manager.LeaderElectionID

// runningSample starts the Streamlit sample as s's user and waits for it to
// run; it returns the app and its pod.
func runningSample(t *testing.T, s *session, c client.Client, owner, display string) (app, *corev1.Pod) {
	t.Helper()
	a := s.createApp(map[string]any{"displayName": display, "template": "streamlit", "sample": true})
	s.waitFor(a.Name, 4*time.Minute, running(t))
	return a, appPod(t, c, owner, a.Name)
}

// waitReady waits until the Deployment name has all its replicas ready on its
// current spec and the gateway answers /readyz through its Service.
func waitReady(t *testing.T, c client.Client, name string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for {
		var d appsv1.Deployment
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: gatewayNS, Name: name}, &d); err != nil {
			t.Fatal(err)
		}
		want := ptr.Deref(d.Spec.Replicas, 1)
		if d.Status.ObservedGeneration >= d.Generation && d.Status.UpdatedReplicas == want &&
			d.Status.ReadyReplicas == want && d.Status.Replicas == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("deploy/%s not ready: %+v", name, d.Status)
		}
		time.Sleep(2 * time.Second)
	}
	s := newSession(t)
	for {
		resp, _ := s.do(http.MethodGet, "/readyz", nil)
		if resp.StatusCode == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("/readyz: %d", resp.StatusCode)
		}
		time.Sleep(2 * time.Second)
	}
}

// waitRelease waits until every Deployment of the release runs its current
// spec: helm --wait can return while the one SQLite pod (Recreate) is still
// being replaced.
func waitRelease(t *testing.T, c client.Client) {
	t.Helper()
	time.Sleep(3 * time.Second) // let the Deployments see the new spec
	var l appsv1.DeploymentList
	if err := c.List(context.Background(), &l, client.InNamespace(gatewayNS), client.MatchingLabels{"app.kubernetes.io/instance": "javo", "app.kubernetes.io/name": "javo"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range l.Items {
		waitReady(t, c, d.Name)
	}
}

// deletePods deletes the pods of the Deployment name and waits until new ones
// are ready.
func deletePods(t *testing.T, c client.Client, name string, sel map[string]string) {
	t.Helper()
	if err := c.DeleteAllOf(context.Background(), &corev1.Pod{}, client.InNamespace(gatewayNS), client.MatchingLabels(sel)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second) // let the Deployment see the pods go
	waitReady(t, c, name)
}

// With SQLite the gateway and the controller are one pod with the database
// on its volume. Deleting that pod loses nothing: the same browser session
// still works, apps and job history are there, and running apps were never
// restarted.
func TestSQLiteRestartKeepsEverything(t *testing.T) {
	c := kube(t)
	if !sqliteInstall(t) {
		t.Skip("the release runs Postgres")
	}
	s := newSession(t)
	s.login(localLogin, "alice")
	me := s.me()
	a, before := runningSample(t, s, c, me.ID, "e2e restart "+time.Now().Format("150405"))

	var job struct{ Name string }
	s.json(http.MethodPost, "/api/job-samples/jobs-python", nil, http.StatusCreated, &job)
	t.Cleanup(func() { s.do(http.MethodDelete, "/api/jobs/"+job.Name, nil) })
	run := startRun(t, s, job.Name)

	deletePods(t, c, "javo", map[string]string{"app.kubernetes.io/instance": "javo", "app.kubernetes.io/name": "javo"})

	s.me() // the session from before the restart
	got := s.waitFor(a.Name, time.Minute, func(a app) bool { return a.Phase == "Running" })
	if got.Name != a.Name {
		t.Fatalf("app %+v", got)
	}
	if after := appPod(t, c, me.ID, a.Name); after.UID != before.UID {
		t.Fatalf("the app's pod was replaced: %s, then %s", before.UID, after.UID)
	}
	var d struct{ Runs []struct{ ID string } }
	s.json(http.MethodGet, "/api/jobs/"+job.Name, nil, http.StatusOK, &d)
	found := false
	for _, r := range d.Runs {
		found = found || r.ID == run
	}
	if !found {
		t.Fatalf("run %s is gone from the history: %+v", run, d.Runs)
	}
}

// deleteControllerLeader deletes the controller pod that holds the leader
// Lease, with no grace period, and returns the Lease's holder identity.
func deleteControllerLeader(t *testing.T, c client.Client) string {
	t.Helper()
	var lease coordinationv1.Lease
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: gatewayNS, Name: controllerLease}, &lease); err != nil {
		t.Fatalf("lease %s: %v", controllerLease, err)
	}
	holder := ptr.Deref(lease.Spec.HolderIdentity, "")
	var leader corev1.Pod
	found := false
	var pods corev1.PodList
	if err := c.List(context.Background(), &pods, client.InNamespace(gatewayNS),
		client.MatchingLabels{"app.kubernetes.io/instance": "javo", "app.kubernetes.io/component": "controller"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range pods.Items {
		if holder != "" && len(holder) >= len(p.Name) && holder[:len(p.Name)] == p.Name {
			leader, found = p, true
		}
	}
	if !found || len(pods.Items) < 2 {
		t.Fatalf("leader %q among %d controller pods", holder, len(pods.Items))
	}
	t.Logf("deleting the leader %s", leader.Name)
	if err := c.Delete(context.Background(), &leader, client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	return holder
}

// With Postgres two controller replicas elect a leader. Deleting the leader
// while an app is starting hands the work to the other one: the app runs.
func TestPostgresControllerFailover(t *testing.T) {
	c := kube(t)
	if sqliteInstall(t) {
		t.Skip("the release runs SQLite")
	}
	s := newSession(t)
	s.login(localLogin, "alice")
	a := s.createApp(map[string]any{"displayName": "e2e failover " + time.Now().Format("150405"), "template": "streamlit", "sample": true})

	holder := deleteControllerLeader(t, c)
	s.waitFor(a.Name, 5*time.Minute, running(t))
	var lease coordinationv1.Lease
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: gatewayNS, Name: controllerLease}, &lease); err != nil {
		t.Fatal(err)
	}
	if now := ptr.Deref(lease.Spec.HolderIdentity, ""); now == holder {
		t.Fatalf("the lease still names the deleted leader %q", now)
	}
}

// A backup taken while the server runs brings back an app deleted after it.
// SQLite: `javo db backup` in the server, then `javo db restore` in a one-off
// pod while the server is scaled to 0 (site/src/content/docs/admin/backups.mdx).
func TestBackupRestoresADeletedApp(t *testing.T) {
	c := kube(t)
	if !sqliteInstall(t) {
		t.Skip("the Postgres restore is pg_restore against the operator's server (site/src/content/docs/admin/backups.mdx)")
	}
	s := newSession(t)
	s.login(localLogin, "alice")
	me := s.me()
	a, _ := runningSample(t, s, c, me.ID, "e2e backup "+time.Now().Format("150405"))

	file := "/var/lib/javo/e2e-backup-" + time.Now().Format("20060102150405") + ".db"
	javoCLI(t, "db", "backup", file)
	resp, data := s.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d %s", resp.StatusCode, data)
	}
	if resp, _ := s.do(http.MethodGet, "/api/workspaces/"+a.Name, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted app answers %d", resp.StatusCode)
	}

	restoreSQLite(t, c, file)

	// Every restored app waits for an admin, then starts when released. A
	// whole restore holds every app of the install, and the ones that were
	// running start in name order at the chosen rate: on a shared install
	// (a long-lived single-node install) dozens may come before this one, so release them at the
	// highest rate rather than one a minute.
	s = newSession(t)
	s.login(localLogin, "alice")
	s.waitFor(a.Name, 2*time.Minute, func(a app) bool { return firstReason(a) == "RestoreHeld" })
	releaseRestore(t, adminSession(t), path.Base(file), 600)
	s.waitFor(a.Name, 4*time.Minute, func(a app) bool { return a.Phase == "Running" })
}

// restoreSQLite scales the server to 0, runs `javo db restore file` in a
// one-off pod on the data volume and scales the server back to 1.
func restoreSQLite(t *testing.T, c client.Client, file string) {
	t.Helper()
	ctx := context.Background()
	var d appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: gatewayNS, Name: "javo"}, &d); err != nil {
		t.Fatal(err)
	}
	scale := func(n int32) {
		t.Helper()
		base := d.DeepCopy()
		d.Spec.Replicas = ptr.To(n)
		if err := c.Patch(ctx, &d, client.MergeFrom(base)); err != nil {
			t.Fatal(err)
		}
	}
	scale(0)
	defer func() { scale(1); waitReady(t, c, "javo") }()
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(2 * time.Second) {
		var pods corev1.PodList
		if err := c.List(ctx, &pods, client.InNamespace(gatewayNS), client.MatchingLabels{"app.kubernetes.io/instance": "javo", "app.kubernetes.io/name": "javo"}); err != nil {
			t.Fatal(err)
		}
		if len(pods.Items) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server pod did not stop")
		}
	}
	image := d.Spec.Template.Spec.Containers[0].Image
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-restore-", Namespace: gatewayNS},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			AutomountServiceAccountToken: ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To[int64](65532), RunAsGroup: ptr.To[int64](65532), FSGroup: ptr.To[int64](65532),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name: "restore", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
				Args: []string{"db", "restore", "--database-path=/var/lib/javo/javo.db", file},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
					Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/var/lib/javo"}},
			}},
			Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "javo-data"},
			}}},
		},
	}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Delete(context.Background(), pod) }()
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(2 * time.Second) {
		var p corev1.Pod
		if err := c.Get(ctx, client.ObjectKeyFromObject(pod), &p); err != nil {
			t.Fatal(err)
		}
		if p.Status.Phase == corev1.PodSucceeded {
			t.Logf("restore:\n%s", podLog(t, pod))
			return
		}
		if p.Status.Phase == corev1.PodFailed || time.Now().After(deadline) {
			t.Fatalf("restore pod %s:\n%s", p.Status.Phase, podLog(t, pod))
		}
	}
}

// startRun runs job once (waiting while the owner has no uid yet) and
// returns the run's id once it has ended.
func startRun(t *testing.T, s *session, job string) string {
	t.Helper()
	var run struct{ ID string }
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(2 * time.Second) {
		resp, data := s.do(http.MethodPost, "/api/jobs/"+job+"/run", map[string]any{})
		if resp.StatusCode == http.StatusServiceUnavailable && time.Now().Before(deadline) {
			continue
		}
		if resp.StatusCode != http.StatusCreated || json.Unmarshal(data, &run) != nil || run.ID == "" {
			t.Fatalf("run %s: %d %s", job, resp.StatusCode, data)
		}
		break
	}
	for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(2 * time.Second) {
		var d struct {
			Runs []struct{ ID, Outcome string }
		}
		s.json(http.MethodGet, "/api/jobs/"+job, nil, http.StatusOK, &d)
		for _, r := range d.Runs {
			if r.ID == run.ID && r.Outcome != "" && r.Outcome != "Running" {
				return run.ID
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s of %s did not end: %+v", run.ID, job, d.Runs)
		}
	}
}

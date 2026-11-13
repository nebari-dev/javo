package storagecmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/cli/clitest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// runStorage runs `javo storage args...` on db (nil: a command that needs no
// database).
func runStorage(t *testing.T, db *store.Store, args ...string) (string, error) {
	t.Helper()
	open := func(context.Context) (*store.Store, error) {
		if db == nil {
			t.Fatal("this command must not need a database")
		}
		return storetest.Reopen(t, db), nil
	}
	return clitest.Run(New(open), append([]string{"storage"}, args...)...)
}

func TestProbeStepCommandRunsTheStepsInADirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o775); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"prepare", "write", "check", "confirm"} {
		if out, err := runStorage(t, nil, "probe-step", "--step", step, "--dir", dir); err != nil {
			t.Fatalf("%s: %v\n%s", step, err, out)
		}
	}
}

func TestProbeStepCommandFailureCarriesTheStepError(t *testing.T) {
	_, err := runStorage(t, nil, "probe-step", "--step", "confirm", "--dir", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "confirm") {
		t.Fatalf("err = %v, want the confirm step's error", err)
	}
}

func TestPrepareCommandChecksTheGroupAndMode(t *testing.T) {
	dir := t.TempDir()
	gid := fmt.Sprint(os.Getgid())
	if out, err := runStorage(t, nil, "prepare", "--dir", dir, "--gid", gid, "--mode", "0775", "--no-mkdir"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o775 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}
	other, otherUID := fmt.Sprint(os.Getgid()+1), fmt.Sprint(os.Getuid()+1)
	_, err := runStorage(t, nil, "prepare", "--dir", dir, "--uid", otherUID, "--gid", other, "--mode", "0775", "--no-mkdir")
	if err == nil || !strings.Contains(err.Error(), "fsGroup") {
		t.Fatalf("err = %v, want the driver-ignored-fsGroup error", err)
	}
}

func TestPrepareCommandCreatesTheDirectoryUnlessToldNotTo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "root")
	args := []string{"prepare", "--dir", dir, "--gid", fmt.Sprint(os.Getgid()), "--mode", "0775"}
	if _, err := runStorage(t, nil, append(args, "--no-mkdir")...); err == nil {
		t.Fatal("a missing directory must fail with --no-mkdir")
	}
	if out, err := runStorage(t, nil, args...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// controllerStandIn plays the probe reconciler: it waits for a nonce and then
// reports the given progress, one status at a time.
func controllerStandIn(t *testing.T, db *store.Store, progress ...model.ProbeStatus) {
	t.Helper()
	go func() {
		ctx := context.Background()
		var req model.StorageRequests
		for {
			r, err := db.Storage().Requests(ctx)
			if err == nil && r.Probe.Nonce != "" {
				req = r
				break
			}
			time.Sleep(time.Millisecond)
		}
		for _, p := range progress {
			p.Nonce, p.StorageClassName = req.Probe.Nonce, req.Probe.StorageClassName
			if err := db.Storage().SetProbeStatus(ctx, &p, nil); err != nil {
				t.Error(err)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
}

func TestVerifyAsksForAProbeAndPrintsEachStep(t *testing.T) {
	verifyPoll = 5 * time.Millisecond
	db := storetest.New(t)
	// A result from an earlier run must not be mistaken for this run's.
	if err := db.Storage().SetProbeStatus(context.Background(), &model.ProbeStatus{Nonce: "old", Result: model.ProbeFailed, FailedStep: "mount"}, nil); err != nil {
		t.Fatal(err)
	}
	controllerStandIn(t, db,
		model.ProbeStatus{Result: model.ProbeRunning, Step: "write"},
		model.ProbeStatus{Result: model.ProbeRunning, Step: "check"},
		model.ProbeStatus{Result: model.ProbeRunning, Step: "confirm"},
		model.ProbeStatus{Result: model.ProbePassed, NodesTested: 2},
	)
	out, err := runStorage(t, db, "verify", "nfs", "--timeout", "10s")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	last := -1
	for _, want := range []string{"step: write", "step: check", "step: confirm", "result: Passed", "2 nodes"} {
		i := strings.Index(out, want)
		if i < 0 || i < last {
			t.Fatalf("output lacks %q in order:\n%s", want, out)
		}
		last = i
	}
	r, err := db.Storage().Requests(context.Background())
	if p := r.Probe; err != nil || p.StorageClassName != "nfs" || p.Nonce == "" || p.Timeout.Duration != 10*time.Second {
		t.Errorf("probe request = %+v, %v", p, err)
	}
}

func TestVerifyFailureIsAnErrorWithTheEvidence(t *testing.T) {
	verifyPoll = 5 * time.Millisecond
	db := storetest.New(t)
	controllerStandIn(t, db, model.ProbeStatus{
		Result: model.ProbeFailed, FailedStep: "mount", Evidence: []string{"FailedMount x: mount.nfs missing"},
	})
	out, err := runStorage(t, db, "verify", "nfs")
	if err == nil {
		t.Fatalf("want an error for a failed probe\n%s", out)
	}
	for _, want := range []string{"result: Failed", "mount", "mount.nfs missing"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestVerifyGivesUpAfterItsTimeout(t *testing.T) {
	verifyPoll = 5 * time.Millisecond
	verifyGrace = 20 * time.Millisecond
	if _, err := runStorage(t, storetest.New(t), "verify", "nfs", "--timeout", "10ms"); err == nil {
		t.Fatal("want a timeout error when nothing answers")
	}
}

// migrationStore is a database whose storage chose the shared mode.
func migrationStore(t *testing.T) *store.Store {
	t.Helper()
	db := storetest.New(t)
	if err := db.Storage().SetClusterStatus(context.Background(), model.ClusterStorageStatus{Choice: &model.StorageChoice{Mode: model.HomeModeShared}}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigratePlanSaysWhoWouldMoveAndChangesNothing(t *testing.T) {
	db := migrationStore(t)
	out, err := runStorage(t, db, "migrate", "plan")
	if err != nil || !strings.Contains(out, "No homes need to move") {
		t.Fatalf("empty plan: %q, %v", out, err)
	}
	if err := db.Storage().SetClusterStatus(context.Background(), model.ClusterStorageStatus{
		Choice:    &model.StorageChoice{Mode: model.HomeModeShared},
		Migration: &model.MigrationStatus{From: model.HomeModePerUser, To: model.HomeModeShared, Users: 2, RequestedBytes: 15 << 30},
	}); err != nil {
		t.Fatal(err)
	}
	out, err = runStorage(t, db, "migrate", "plan")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2 homes", "perUser", "shared", "15Gi", "javo storage migrate schedule"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output lacks %q:\n%s", want, out)
		}
	}
	if r, err := db.Storage().Requests(context.Background()); err != nil || r.Migration.StartAt != nil {
		t.Fatalf("plan scheduled something: %v, %+v", err, r.Migration)
	}
}

func TestMigrateScheduleSetsTheStartTime(t *testing.T) {
	db := migrationStore(t)
	out, err := runStorage(t, db, "migrate", "schedule", "--at", "2026-11-01T02:00:00Z")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r, err := db.Storage().Requests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)
	if got := r.Migration.StartAt; got == nil || !got.Time.Equal(want) {
		t.Fatalf("startAt = %v, want %v", got, want)
	}
	if r.Migration.To != model.HomeModeShared {
		t.Fatalf("to = %q, want the chosen mode, so a later mode change needs a new schedule", r.Migration.To)
	}
	if !strings.Contains(out, "2026-11-01T02:00:00Z") {
		t.Errorf("output does not repeat the time:\n%s", out)
	}
	if _, err := runStorage(t, db, "migrate", "schedule", "--at", "tomorrow"); err == nil {
		t.Fatal("a time that is not RFC 3339 must be refused")
	}
	if _, err := runStorage(t, db, "migrate", "schedule"); err == nil {
		t.Fatal("--at is required")
	}
	if _, err := runStorage(t, db, "migrate", "retry"); err != nil {
		t.Fatal(err)
	}
	if r, _ := db.Storage().Requests(context.Background()); r.Migration.Attempt != 1 {
		t.Fatalf("attempt = %d after retry, want 1", r.Migration.Attempt)
	}
}

func TestMigrateStatusListsHomesAndTheCommandToWatchTheCopy(t *testing.T) {
	db := migrationStore(t)
	home := func(name string, m *model.HomeMigration) {
		h, err := db.Homes().Ensure(context.Background(), "javo-apps", name, nil)
		if err != nil {
			t.Fatal(err)
		}
		h.Status.Migration = m
		if _, err := db.Homes().Update(context.Background(), h, h.Version); err != nil {
			t.Fatal(err)
		}
	}
	home("alice", &model.HomeMigration{Phase: model.MigrationCopying, Job: "javo-migrate-home-alice-2-a0"})
	home("bob", &model.HomeMigration{Phase: model.MigrationFailed, Evidence: []string{"copy failed: partial transfer", "x: open /old/x: permission denied"}})
	home("carol", &model.HomeMigration{Phase: model.MigrationSwitched})
	home("dave", nil)
	out, err := runStorage(t, db, "migrate", "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"alice", "Copying", "kubectl logs -f job/javo-migrate-home-alice-2-a0 -n javo-apps",
		"bob", "Failed", "copy failed: partial transfer", "open /old/x",
		"carol", "Switched",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "dave") {
		t.Errorf("a home that is not moving is listed:\n%s", out)
	}
	if strings.Count(out, "kubectl logs") != 1 {
		t.Errorf("only the running copy gets a logs command:\n%s", out)
	}
}

// The report for a cluster with Hetzner volumes per user and an untested
// Longhorn prints the title lines,
// each with its provenance in brackets. testdata/storage-report-hetzner.golden
// is the whole expected output, byte for byte.
func TestStorageReportForHetznerAndLonghorn(t *testing.T) {
	fact := func(v bool) model.Fact { return model.Fact{Value: v, Provenance: model.ProvenanceKnown} }
	perNode := int32(16)
	db := storetest.New(t)
	if err := db.Storage().SetClusterStatus(context.Background(), model.ClusterStorageStatus{
		Nodes: 3,
		Classes: []model.ClassCapability{
			{Name: "hcloud-volumes", Provisioner: "csi.hetzner.cloud", IsDefault: true, VolumesPerNode: &perNode,
				RWXFile: fact(false), PosixOwnership: fact(true), SizeEnforced: fact(true), Expansion: fact(true), Snapshots: fact(false)},
			{Name: "longhorn", Provisioner: "driver.longhorn.io", Notes: []string{"NeedsNfsClient"},
				RWXFile: fact(true), PosixOwnership: fact(true), SizeEnforced: fact(true), Expansion: fact(true), Snapshots: fact(false)},
		},
		Choice: &model.StorageChoice{Mode: model.HomeModePerUser, HomeClass: "hcloud-volumes", SharedClass: "longhorn", Source: "auto"},
	}); err != nil {
		t.Fatal(err)
	}
	out, err := runStorage(t, db, "report")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "storage-report-hetzner.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if out != string(want) {
		t.Errorf("report differs from the golden file\ngot:\n%s\nwant:\n%s", out, want)
	}
}

// An admin gives a group a folder from the server pod; it is listed with its
// mount directory and state, and removing it keeps the claim.
func TestFolderAddListRemove(t *testing.T) {
	db := storetest.New(t)
	out, err := runStorage(t, db, "folder", "add", "/org/data-science", "--size", "50Gi")
	if err != nil || !strings.Contains(out, "/shared/data-science") {
		t.Fatalf("add: %q, %v", out, err)
	}
	f, err := db.SharedFolders().ByName(context.Background(), model.DefaultWorkspaceNamespace, "data-science")
	if err != nil || f.Spec.Group != "/org/data-science" || f.Spec.Size.String() != "50Gi" {
		t.Fatalf("folder = %+v, %v", f, err)
	}
	if _, err := runStorage(t, db, "folder", "add", "/org/data-science"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second add: %v", err)
	}
	meta.SetStatusCondition(&f.Status.Conditions, metav1.Condition{Type: model.ConditionReady, Status: metav1.ConditionFalse, Reason: "GroupHasNoId", Message: "waiting"})
	if _, err := db.SharedFolders().Update(context.Background(), f, f.Version); err != nil {
		t.Fatal(err)
	}
	if out, err := runStorage(t, db, "folder", "list"); err != nil || !strings.Contains(out, "data-science\tgroup /org/data-science\t/shared/data-science\tGroupHasNoId: waiting") {
		t.Fatalf("list: %q, %v", out, err)
	}
	if out, err := runStorage(t, db, "folder", "remove", "data-science"); err != nil || !strings.Contains(out, "folder-data-science") {
		t.Fatalf("remove: %q, %v", out, err)
	}
	if out, _ := runStorage(t, db, "folder", "list"); !strings.Contains(out, "No shared folders") {
		t.Fatalf("list after remove: %q", out)
	}
}

func TestHomeSizeSetsTheSizeTheHomeAsksFor(t *testing.T) {
	db := storetest.New(t)
	if _, err := runStorage(t, db, "home-size", "alice-1234abcd", "20Gi"); err == nil || !strings.Contains(err.Error(), "no home") {
		t.Fatalf("no home: %v", err)
	}
	if _, err := db.Homes().Ensure(context.Background(), model.DefaultWorkspaceNamespace, "alice-1234abcd", nil); err != nil {
		t.Fatal(err)
	}
	if out, err := runStorage(t, db, "home-size", "alice-1234abcd", "20Gi"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	h, err := db.Homes().ByOwner(context.Background(), model.DefaultWorkspaceNamespace, "alice-1234abcd")
	if err != nil || h.Spec.Size.String() != "20Gi" {
		t.Fatalf("home = %+v, %v", h.Spec, err)
	}
}

// Where apps do not run on Kubernetes there are no storage classes,
// claims or group folders: a folder asked for on such an install is
// refused, and no row is made that nothing could ever serve.
func TestStorageCommandsNeedACluster(t *testing.T) {
	db := storetest.New(t)
	config := filepath.Join(t.TempDir(), "config.yaml")
	yaml := "auth:\n  local:\n    enabled: true\nexecutor: docker\nkeysDir: /k\ndocker:\n  gateway: javo\n"
	if err := os.WriteFile(config, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runStorage(t, db, "folder", "add", "/org/data-science", "--config", config)
	if err == nil || !strings.Contains(err.Error(), "needs executor kubernetes") || !strings.Contains(err.Error(), "executor docker") {
		t.Fatalf("%v\n%s", err, out)
	}
	if folders, _ := db.SharedFolders().List(context.Background()); len(folders) != 0 {
		t.Fatalf("a folder was made: %+v", folders)
	}
}

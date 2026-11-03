package store_test

import (
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var controller = model.Actor{System: model.SystemController}

// The first app of an owner asks for a home; the next ones find the same one.
func TestEnsureHomeOncePerOwner(t *testing.T) {
	s := storetest.New(t)
	size := resource.MustParse("10Gi")
	first, err := s.Homes().Ensure(ctx, "javo-apps", "alice-1234abcd", &size)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Homes().Ensure(ctx, "javo-apps", "alice-1234abcd", nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.Spec.Size.String() != "10Gi" || again.Version != 1 {
		t.Fatalf("second ensure = %+v, want the first home %+v", again, first)
	}
	if _, err := s.Homes().Ensure(ctx, "javo-apps", "Not A Name", nil); err == nil {
		t.Fatal("a home for an owner that is not a name was stored")
	}
}

// A write names the version it read; a stale one is ErrConflict, so the home
// and migration controllers never overwrite each other's status.
func TestUpdateHomeConflict(t *testing.T) {
	s := storetest.New(t)
	h, err := s.Homes().Ensure(ctx, "javo-apps", "alice-1234abcd", nil)
	if err != nil {
		t.Fatal(err)
	}
	next := h
	next.Status.ClaimName = "home-alice-1234abcd"
	got, err := s.Homes().Update(ctx, next, h.Version)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != h.Version+1 || got.Status.ClaimName != "home-alice-1234abcd" {
		t.Fatalf("updated = %+v", got)
	}
	stale := h
	stale.Status.Prepared = true
	if _, err := s.Homes().Update(ctx, stale, h.Version); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale update err = %v, want ErrConflict", err)
	}
	read, err := s.Homes().Get(ctx, h.ID)
	if err != nil || read.Status.Prepared || read.Status.ClaimName != "home-alice-1234abcd" {
		t.Fatalf("home = %+v, %v", read, err)
	}
}

// Home writes are announced: an app waiting for its home hears when it is ready.
func TestHomeWritesAreAnnounced(t *testing.T) {
	s, _ := startedStore(t)
	h, err := s.Homes().Ensure(ctx, "javo-apps", "alice-1234abcd", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != store.TableHomes || c.ID != h.ID {
		t.Fatalf("change = %+v", c)
	}
	h.Status.Prepared = true
	if _, err := s.Homes().Update(ctx, h, h.Version); err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != store.TableHomes || c.ID != h.ID {
		t.Fatalf("change = %+v", c)
	}
}

// A person with homes in two namespaces (namespace mode changed) has both.
func TestHomesByOwner(t *testing.T) {
	s := storetest.New(t)
	for _, ns := range []string{"javo-apps", "javo-alice"} {
		if _, err := s.Homes().Ensure(ctx, ns, "alice-1234abcd", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Homes().Ensure(ctx, "javo-apps", "bob-5678abcd", nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.Homes().ListByOwner(ctx, "alice-1234abcd")
	if err != nil || len(got) != 2 {
		t.Fatalf("homes = %v, %v", got, err)
	}
	h, err := s.Homes().ByOwner(ctx, "javo-alice", "alice-1234abcd")
	if err != nil || h.Namespace != "javo-alice" {
		t.Fatalf("home = %+v, %v", h, err)
	}
	if _, err := s.Homes().ByOwner(ctx, "javo-apps", "carol-0000abcd"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	all, err := s.Homes().List(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("all homes = %d, %v", len(all), err)
	}
}

// An admin's restore of a home is audited and kept in the spec; a second
// one is refused while the first has not ended.
func TestRequestHomeRestoreAudited(t *testing.T) {
	s := storetest.New(t)
	h, err := s.Homes().Ensure(ctx, "javo-apps", "alice-1234abcd", nil)
	if err != nil {
		t.Fatal(err)
	}
	before := auditCount(t, s)
	req := model.HomeRestoreRequest{Snapshot: "0123abcd", SnapshotTime: metav1.NewTime(t0), Mode: model.RestoreBeside}
	got, err := s.Homes().RequestRestore(ctx, model.Actor{Person: "admin"}, h.ID, req, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r := got.Spec.Restore
	if r == nil || r.ID == "" || r.Snapshot != "0123abcd" || r.Mode != model.RestoreBeside || r.By != "admin" || !r.At.Time.Equal(t0.Add(time.Hour)) {
		t.Fatalf("restore = %+v", r)
	}
	if auditCount(t, s) != before+1 {
		t.Fatal("restore not audited")
	}
	if _, err := s.Homes().RequestRestore(ctx, model.Actor{Person: "admin"}, h.ID, req, t0); !errors.Is(err, store.ErrRestoreRunning) {
		t.Fatalf("second restore while the first runs: %v", err)
	}
	// Once the controller reports the first one ended, another may start.
	got.Status.Restore = &model.HomeRestore{ID: r.ID, Phase: model.RestoreDone}
	if _, err := s.Homes().Update(ctx, got, got.Version); err != nil {
		t.Fatal(err)
	}
	again, err := s.Homes().RequestRestore(ctx, model.Actor{Person: "admin"}, h.ID, req, t0)
	if err != nil || again.Spec.Restore.ID == r.ID {
		t.Fatalf("restore after the first ended: %+v, %v", again.Spec.Restore, err)
	}
	if _, err := s.Homes().RequestRestore(ctx, model.Actor{Person: "admin"}, "nope", req, t0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown home: %v", err)
	}
}

// A full backup run asks every home and every shared folder to be backed
// up as part of it, once, audited.
func TestRequestBackupRunMarksEveryHomeAndFolder(t *testing.T) {
	s := storetest.New(t)
	for _, o := range []string{"alice-1234abcd", "bob-5678abcd"} {
		if _, err := s.Homes().Ensure(ctx, "javo-apps", o, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SharedFolders().Create(ctx, model.Actor{Person: "admin"}, model.SharedFolder{
		Namespace: "javo-apps", Name: "data", Spec: model.SharedFolderSpec{Group: "/data"},
	}); err != nil {
		t.Fatal(err)
	}
	before := auditCount(t, s)
	homes, folders, err := s.Homes().RequestBackupRun(ctx, model.Actor{Person: "admin"}, "backup-run-1", t0)
	if err != nil || homes != 2 || folders != 1 {
		t.Fatalf("asked %d homes, %d folders: %v", homes, folders, err)
	}
	all, _ := s.Homes().List(ctx)
	for _, h := range all {
		if h.Spec.Backup == nil || h.Spec.Backup.Run != "backup-run-1" || !h.Spec.Backup.At.Time.Equal(t0) {
			t.Fatalf("home %s: %+v", h.Spec.Owner, h.Spec.Backup)
		}
	}
	fs, _ := s.SharedFolders().List(ctx)
	if fs[0].Spec.Backup == nil || fs[0].Spec.Backup.Run != "backup-run-1" {
		t.Fatalf("folder: %+v", fs[0].Spec.Backup)
	}
	if auditCount(t, s) != before+1 {
		t.Fatal("run not audited once")
	}
	// The same run asked again changes nothing.
	if homes, _, err := s.Homes().RequestBackupRun(ctx, model.Actor{Person: "admin"}, "backup-run-1", t0); err != nil || homes != 0 {
		t.Fatalf("again: %d, %v", homes, err)
	}
}

// Removing a person's homes deletes their rows (and with them their backup
// and restore bookkeeping), audited; other people's homes stay.
func TestDeleteHomesOfAnOwner(t *testing.T) {
	s := storetest.New(t)
	for _, ns := range []string{"javo-apps", "team-a"} {
		if _, err := s.Homes().Ensure(ctx, ns, "alice-1234abcd", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Homes().Ensure(ctx, "javo-apps", "bob-5678abcd", nil); err != nil {
		t.Fatal(err)
	}
	before := auditCount(t, s)
	n, err := s.Homes().DeleteFor(ctx, model.Actor{System: model.SystemCLI}, "alice-1234abcd")
	if err != nil || n != 2 {
		t.Fatalf("deleted %d, %v", n, err)
	}
	if left, _ := s.Homes().ListByOwner(ctx, "alice-1234abcd"); len(left) != 0 {
		t.Fatalf("alice's homes left: %+v", left)
	}
	if left, _ := s.Homes().List(ctx); len(left) != 1 {
		t.Fatalf("homes left: %d, want bob's", len(left))
	}
	if auditCount(t, s) != before+2 {
		t.Fatal("deletes not audited")
	}
}

// An admin's resize of a home is audited and kept in the spec.
func TestSetHomeSizeAudited(t *testing.T) {
	s := storetest.New(t)
	h, err := s.Homes().Ensure(ctx, "javo-apps", "alice-1234abcd", nil)
	if err != nil {
		t.Fatal(err)
	}
	before := auditCount(t, s)
	got, err := s.Homes().SetSize(ctx, model.Actor{Person: "admin"}, h.ID, resource.MustParse("20Gi"))
	if err != nil || got.Spec.Size.String() != "20Gi" {
		t.Fatalf("home = %+v, %v", got, err)
	}
	if auditCount(t, s) != before+1 {
		t.Fatal("resize not audited")
	}
}

func TestSharedFolders(t *testing.T) {
	s, _ := startedStore(t)
	admin := model.Actor{Person: "admin"}
	f, err := s.SharedFolders().Create(ctx, admin, model.SharedFolder{
		Namespace: "javo-apps", Name: "data-science", Spec: model.SharedFolderSpec{Group: "/org/data-science"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != store.TableSharedFolders || c.ID != f.ID {
		t.Fatalf("change = %+v", c)
	}
	if _, err := s.SharedFolders().Create(ctx, admin, model.SharedFolder{
		Namespace: "javo-apps", Name: "data-science", Spec: model.SharedFolderSpec{Group: "/org/other"},
	}); !errors.Is(err, store.ErrExists) {
		t.Fatalf("second folder of the same name: %v, want ErrExists", err)
	}
	if _, err := s.SharedFolders().Create(ctx, admin, model.SharedFolder{Namespace: "javo-apps", Name: "x"}); err == nil {
		t.Fatal("a folder without a group was stored")
	}
	f.Status.GID = 2001
	if _, err := s.SharedFolders().Update(ctx, f, f.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SharedFolders().Update(ctx, f, f.Version); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale folder write: %v", err)
	}
	got, err := s.SharedFolders().ByName(ctx, "javo-apps", "data-science")
	if err != nil || got.Status.GID != 2001 || got.Spec.Group != "/org/data-science" || got.CreatedAt.IsZero() {
		t.Fatalf("folder = %+v, %v", got, err)
	}
	if err := s.SharedFolders().Delete(ctx, admin, f.ID); err != nil {
		t.Fatal(err)
	}
	if list, err := s.SharedFolders().List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("folders after delete = %v, %v", list, err)
	}
}

// The admin's requests are read back as written, audited and announced; a
// stale write is a conflict.
func TestStorageRequests(t *testing.T) {
	s, _ := startedStore(t)
	empty, err := s.Storage().Requests(ctx)
	if err != nil || empty.Probe.Nonce != "" {
		t.Fatalf("requests before any = %+v, %v", empty, err)
	}
	before := auditCount(t, s)
	at := metav1.NewTime(time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC))
	err = s.Storage().UpdateRequests(ctx, model.Actor{Person: "admin"}, "storage.schedule", func(r *model.StorageRequests) error {
		r.Migration.StartAt, r.Migration.To = &at, model.HomeModeShared
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != store.TableStorageSettings {
		t.Fatalf("change = %+v", c)
	}
	got, err := s.Storage().Requests(ctx)
	if err != nil || !got.Migration.StartAt.Equal(&at) || got.Migration.To != model.HomeModeShared {
		t.Fatalf("requests = %+v, %v", got, err)
	}
	if auditCount(t, s) != before+1 {
		t.Fatal("request not audited")
	}
	// A function that fails writes nothing.
	boom := errors.New("boom")
	if err := s.Storage().UpdateRequests(ctx, model.Actor{Person: "admin"}, "storage.retry", func(r *model.StorageRequests) error {
		r.Migration.Attempt = 9
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := s.Storage().Requests(ctx); got.Migration.Attempt != 0 {
		t.Fatal("a failed update was written")
	}
}

// The cluster and probe controllers write their own parts of the status; the
// read puts them together and neither write undoes the other.
func TestStorageStatusParts(t *testing.T) {
	s, _ := startedStore(t)
	err := s.Storage().SetClusterStatus(ctx, model.ClusterStorageStatus{
		Nodes: 2, Choice: &model.StorageChoice{Mode: model.HomeModePerUser},
		// Probe parts given here are not the cluster controller's to write.
		Probe: &model.ProbeStatus{Nonce: "ignored"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != store.TableStorageStatus {
		t.Fatalf("change = %+v", c)
	}
	probe := &model.ProbeStatus{Nonce: "n1", Result: model.ProbePassed, StorageClassName: "nfs"}
	if err := s.Storage().SetProbeStatus(ctx, probe, []model.ProbeStatus{*probe}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Storage().Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Nodes != 2 || got.Choice == nil || got.Probe == nil || got.Probe.Nonce != "n1" || len(got.ProbeResults) != 1 {
		t.Fatalf("status = %+v", got)
	}
	// Writing the cluster part again keeps the probe part.
	if err := s.Storage().SetClusterStatus(ctx, model.ClusterStorageStatus{Nodes: 3}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Storage().Status(ctx); got.Nodes != 3 || got.Probe == nil || got.Probe.Nonce != "n1" {
		t.Fatalf("status after cluster write = %+v", got)
	}
}

// Cluster puts together the config, the admin's requests and the status.
func TestStorageCluster(t *testing.T) {
	s := storetest.New(t)
	var cfg model.ClusterStorageSpec
	cfg.ApplyDefaults()
	if err := s.Storage().UpdateRequests(ctx, controller, "storage.probe", func(r *model.StorageRequests) error {
		r.Probe = model.ProbeSpec{StorageClassName: "nfs", Nonce: "n1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Storage().SetClusterStatus(ctx, model.ClusterStorageStatus{Nodes: 1}); err != nil {
		t.Fatal(err)
	}
	cs, err := s.Storage().Cluster(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Spec.Probe.Nonce != "n1" || cs.Spec.Home.Size.String() != "10Gi" || cs.Status.Nodes != 1 {
		t.Fatalf("cluster storage = %+v", cs)
	}
}

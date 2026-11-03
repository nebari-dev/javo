package admin_test

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/backup"
	"github.com/nebari-dev/javo/internal/backup/restic"
	adminapi "github.com/nebari-dev/javo/internal/gateway/admin"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// A run of the backup job starts a full backup: the database is backed up
// and copied with the run's tag, every home and shared folder is asked to
// follow, and its progress shows each step until all ended.
func TestAFullBackupRunCopiesTheDatabaseAndAsksEveryHomeAndFolder(t *testing.T) {
	dir := t.TempDir()
	var f *apitest.Fixture
	c := &apitest.Copies{}
	cfg := apitest.SqliteBackups(dir)
	sqlite := storetest.Engine() == store.EngineSQLite
	if !sqlite {
		// Postgres: its CronJob copies the dumps; the run does not.
		cfg = adminapi.BackupConfig{Engine: store.EnginePostgres}
	}
	cfg.Bucket = apitest.BucketWith(t, &f, c, nil, nil)
	f = apitest.New(t, apitest.Options{Backups: cfg})
	admin := f.Session(apitest.AdminID, "javo-admins")
	ctx := context.Background()
	for _, o := range []string{"alice-1234abcd", "bob-5678abcd"} {
		if _, err := f.Db.Homes().Ensure(ctx, gatewaytest.Namespace, o, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Db.SharedFolders().Create(ctx, gatewaytest.Actor, model.SharedFolder{Namespace: gatewaytest.Namespace, Name: "data", Spec: model.SharedFolderSpec{Group: "/data"}}); err != nil {
		t.Fatal(err)
	}

	p := apitest.Decode[apitest.ProgressJSON](t, f.Do(http.MethodPost, "/api/admin/backups/runs", `{"run":"r1"}`, admin))
	if sqlite && (len(c.Runs) != 1 || c.Runs[0] != "r1" || filepath.Dir(c.Files[0]) != dir) {
		t.Fatalf("copies = %+v", c)
	}
	if !sqlite && (len(c.Runs) != 0 || p.Steps[0].Status.Reason != string(reasons.DatabaseBackupsByCronJob)) {
		t.Fatalf("postgres: copies %+v, database step %+v", c, p.Steps[0])
	}
	states := map[string]string{}
	for _, s := range p.Steps {
		states[s.Kind+"/"+s.Name] = s.State
	}
	if len(p.Steps) != 4 || states["home/alice-1234abcd"] != "Waiting" || states["folder/data"] != "Waiting" || p.Done {
		t.Fatalf("progress = %+v", p)
	}
	// Starting the same run again copies nothing twice.
	f.Do(http.MethodPost, "/api/admin/backups/runs", `{"run":"r1"}`, admin)
	if sqlite && len(c.Runs) != 1 {
		t.Fatalf("copied again: %+v", c.Runs)
	}

	Homes, _ := f.Db.Homes().List(ctx)
	for i, h := range Homes {
		h.Status.Backup = &model.HomeBackup{Run: "r1"}
		if i == 1 {
			h.Status.Backup.Reason = string(reasons.BackupDenied)
		}
		if _, err := f.Db.Homes().Update(ctx, h, h.Version); err != nil {
			t.Fatal(err)
		}
	}
	fs, _ := f.Db.SharedFolders().List(ctx)
	fs[0].Status.Backup = &model.HomeBackup{Run: "r1"}
	if _, err := f.Db.SharedFolders().Update(ctx, fs[0], fs[0].Version); err != nil {
		t.Fatal(err)
	}
	p = apitest.Decode[apitest.ProgressJSON](t, f.Do(http.MethodGet, "/api/admin/backups/runs/r1", "", admin))
	if !p.Done || p.Failed != 1 {
		t.Fatalf("progress after the steps ended = %+v", p)
	}
	if w := f.Do(http.MethodPost, "/api/admin/backups/runs", `{"run":"r2"}`, f.Session("bob", "")); w.Code != http.StatusForbidden {
		t.Fatalf("a member starting a backup: %d", w.Code)
	}
}

// The page sums up the newest run of the backup job in one status, what it
// included and how much failed; without a backup job it says to set one up.
func TestTheBackupsPageSumsUpTheNewestRun(t *testing.T) {
	var f *apitest.Fixture
	cfg := apitest.SqliteBackups(t.TempDir())
	cfg.Bucket = apitest.BucketWith(t, &f, &apitest.Copies{}, nil, nil)
	f = apitest.New(t, apitest.Options{Backups: cfg, People: []apitest.TestPerson{apitest.SignedIn("alice", time.Time{})}})
	admin := f.Session(apitest.AdminID, "javo-admins")
	ctx := context.Background()

	got := apitest.Decode[apitest.PageJSON](t, f.Do(http.MethodGet, "/api/admin/backups", "", admin))
	if got.Bucket == nil || got.Bucket.Status.Reason != string(reasons.BackupsNotSetUp) || got.Bucket.Jobs.Backup != nil {
		t.Fatalf("no backup job: %+v", got.Bucket)
	}

	job, err := f.Db.Jobs().Create(ctx, gatewaytest.Actor, &model.Job{Namespace: gatewaytest.Namespace, Name: "backup", Owner: apitest.AdminID, Spec: model.JobSpec{
		Template: adminapi.BackupTemplate, Engine: "command", Command: []string{"javo", "backup", "run"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// alice has an account; the other is a home volume with no account.
	for _, o := range []string{apitest.PersonID("alice"), "bob-5678abcd"} {
		h, err := f.Db.Homes().Ensure(ctx, gatewaytest.Namespace, o, nil)
		if err != nil {
			t.Fatal(err)
		}
		at := metav1.Now()
		h.Spec.Backup = &model.BackupRequest{Run: "r1", At: at}
		h.Status.Backup = &model.HomeBackup{Run: "r1", LastAt: &at}
		if o == "bob-5678abcd" {
			h.Status.Backup.Reason = string(reasons.BackupDenied)
		}
		if _, err := f.Db.Homes().Update(ctx, h, h.Version); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if _, err := f.Db.Jobs().RecordRun(ctx, model.Run{JobID: job.ID, K8sJob: "r1", Trigger: "schedule",
		StartTime: now.Add(-time.Hour), FinishTime: now, Outcome: model.RunOutcomeFailed}); err != nil {
		t.Fatal(err)
	}
	got = apitest.Decode[apitest.PageJSON](t, f.Do(http.MethodGet, "/api/admin/backups", "", admin))
	b := got.Bucket
	if b.Jobs.Backup == nil || b.Jobs.Backup.Name != "backup" {
		t.Fatalf("jobs = %+v", b.Jobs)
	}
	if r := b.LastRun; r == nil || r.Run != "r1" || r.Homes != 1 || r.HomesFailed != 0 || r.Volumes != 1 || r.VolumesFailed != 1 || r.Status.Reason != string(reasons.FullBackupProblems) {
		t.Fatalf("last run = %+v", r)
	}
	if b.Status.Reason != string(reasons.FullBackupProblems) || !b.Status.Problem || len(b.Runs) != 1 {
		t.Fatalf("status %+v, runs %+v", b.Status, b.Runs)
	}
	if b.Homes[0].Owner != "bob-5678abcd" || !b.Homes[0].Status.Problem || b.Homes[0].Person != "Home volume with no account" || b.Homes[1].Person != "Alice (alice, SSO)" {
		t.Fatalf("problems first: %+v", b.Homes)
	}
}

// A run of the restore job brings back a full backup: the apps gone since
// come back held (from that run's database copy) and every home folder in
// that run is restored; a home not in it says so.
func TestRestoreEverythingFromARun(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	dir := t.TempDir()
	alice := apitest.PersonID("alice")
	name := apitest.BackupOf(t, dir, now.Add(-time.Hour), gatewaytest.App(alice, "gone"))
	var f *apitest.Fixture
	cfg := apitest.SqliteBackups(dir)
	snaps := map[string][]restic.Snapshot{alice: {
		{ID: "old", Time: now.Add(-48 * time.Hour), Tags: []string{"home", "run-r0"}},
		{ID: "a1", Time: now.Add(-time.Hour), Tags: []string{"home", "run-r1"}},
	}}
	cfg.Bucket = apitest.BucketWith(t, &f, &apitest.Copies{}, snaps, []restic.Snapshot{{ID: "d1", Paths: []string{"/" + name}, Tags: []string{"database", "run-r1"}}})
	f = apitest.New(t, apitest.Options{Now: func() time.Time { return now }, Backups: cfg, People: []apitest.TestPerson{apitest.SignedIn("alice", time.Time{})}},
		gatewaytest.Template("t", "streamlit"))
	admin := f.Session(apitest.AdminID, "javo-admins")
	ctx := context.Background()
	for _, o := range []string{alice, "bob-5678abcd"} {
		if _, err := f.Db.Homes().Ensure(ctx, gatewaytest.Namespace, o, nil); err != nil {
			t.Fatal(err)
		}
	}

	w := f.Do(http.MethodPost, "/api/admin/backups/restores", `{"run":"x1","from":"r1"}`, admin)
	if w.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	p := apitest.Decode[apitest.ProgressJSON](t, w)
	states := map[string]string{}
	for _, s := range p.Steps {
		states[s.Kind+"/"+s.Name] = s.State + "/" + s.Status.Reason
	}
	if states["home/"+alice] != "Waiting/HomeRestoreWaiting" || states["home/bob-5678abcd"] != "Failed/NotInBackupRun" || states["apps/"+name] != "Done/DatabaseCopied" {
		t.Fatalf("steps = %v", states)
	}
	back := f.GetWorkspace(alice, "gone")
	if back.RestoreHold != name {
		t.Errorf("app back and held: %+v", back.RestoreHold)
	}
	h, err := f.Db.Homes().ByOwner(ctx, gatewaytest.Namespace, alice)
	if err != nil || h.Spec.Restore == nil || h.Spec.Restore.Run != "x1" || h.Spec.Restore.Snapshot != "a1" || h.Spec.Restore.Mode != model.RestoreReplace {
		t.Fatalf("alice's restore = %+v, %v", h.Spec.Restore, err)
	}
	p = apitest.Decode[apitest.ProgressJSON](t, f.Do(http.MethodGet, "/api/admin/backups/restores/x1", "", admin))
	if len(p.Steps) != 1 || p.Steps[0].Name != alice || p.Done {
		t.Fatalf("progress = %+v", p)
	}
}

// Without database backups (SQLite and no database.backup.schedule) a full
// backup cannot hold the database, and its progress says so.
func TestAFullBackupWithoutDatabaseBackupsSaysTheDatabaseIsMissing(t *testing.T) {
	var f *apitest.Fixture
	cfg := adminapi.BackupConfig{Engine: store.EngineSQLite}
	cfg.Bucket = apitest.BucketWith(t, &f, &apitest.Copies{}, nil, nil)
	f = apitest.New(t, apitest.Options{Backups: cfg})
	p := apitest.Decode[apitest.ProgressJSON](t, f.Do(http.MethodPost, "/api/admin/backups/runs", `{"run":"r1"}`, f.Session(apitest.AdminID, "javo-admins")))
	if len(p.Steps) == 0 || p.Steps[0].Kind != "database" || p.Steps[0].State != "Failed" || p.Steps[0].Status.Reason != string(reasons.DatabaseBackupsOff) {
		t.Fatalf("progress = %+v", p)
	}
}

// Back up now takes no body and runs the backup job; Restore runs the
// restore job with what to restore as the run's parameters.
func TestBackUpNowAndRestoreRunTheJobs(t *testing.T) {
	var f *apitest.Fixture
	cfg := apitest.SqliteBackups(t.TempDir())
	cfg.Bucket = apitest.BucketWith(t, &f, &apitest.Copies{}, nil, nil)
	f = apitest.New(t, apitest.Options{Backups: cfg})
	admin := f.Session(apitest.AdminID, "javo-admins")
	ctx := context.Background()
	if w := f.Do(http.MethodPost, "/api/admin/backups/now", "", admin); w.Code != http.StatusConflict {
		t.Fatalf("no backup job yet: %d %s", w.Code, w.Body)
	}
	for _, c := range [][]string{{"javo", "backup", "run"}, {"/javo", "backup", "restore-run"}} {
		if _, err := f.Db.Jobs().Create(ctx, gatewaytest.Actor, &model.Job{Namespace: gatewaytest.Namespace, Name: c[2], Owner: apitest.AdminID, Spec: model.JobSpec{
			Template: adminapi.BackupTemplate, Engine: "command", Command: c,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	w := f.Do(http.MethodPost, "/api/admin/backups/now", "", admin)
	if w.Code != http.StatusCreated {
		t.Fatalf("back up now: %d %s", w.Code, w.Body)
	}
	got := apitest.Decode[struct{ Job, RunID string }](t, w)
	if got.Job != "run" || got.RunID == "" {
		t.Fatalf("got %+v", got)
	}
	w = f.Do(http.MethodPost, "/api/admin/backups/restore", `{"from":"r1","person":"alice-1234abcd","mode":"beside"}`, admin)
	if w.Code != http.StatusCreated {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	id := apitest.Decode[struct{ RunID string }](t, w).RunID
	run, err := f.Db.Jobs().Run(ctx, id)
	if err != nil || run.Parameters["from"] != "r1" || run.Parameters["person"] != "alice-1234abcd" || run.Parameters["mode"] != "beside" {
		t.Fatalf("run = %+v, %v", run, err)
	}
	// Everyone: only the date is given; the job's defaults stand for the rest.
	w = f.Do(http.MethodPost, "/api/admin/backups/restore", `{"from":"r1"}`, admin)
	run, err = f.Db.Jobs().Run(ctx, apitest.Decode[struct{ RunID string }](t, w).RunID)
	if _, set := run.Parameters["mode"]; err != nil || set || run.Parameters["from"] != "r1" {
		t.Fatalf("run = %+v, %v", run, err)
	}
}

// The steps say who each home is (display name, username, local or SSO),
// tell a home volume with no account apart, and the database step's log
// says what was dumped and uploaded where, with restic's numbers.
func TestFullBackupStepsSayWhoAndWhere(t *testing.T) {
	var f *apitest.Fixture
	cfg := apitest.SqliteBackups(t.TempDir())
	cfg.Bucket = apitest.BucketWith(t, &f, &apitest.Copies{}, nil, nil)
	f = apitest.New(t, apitest.Options{Backups: cfg, People: []apitest.TestPerson{apitest.SignedIn("alice", time.Time{})}})
	admin := f.Session(apitest.AdminID, "javo-admins")
	ctx := context.Background()
	for _, o := range []string{apitest.PersonID("alice"), "local-alice-6f5951a4"} {
		if _, err := f.Db.Homes().Ensure(ctx, gatewaytest.Namespace, o, nil); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 11, 2, 14, 3, 12, 0, time.UTC)
	if err := backup.RecordDatabaseCopy(ctx, f.Db.Facts(), backup.DatabaseCopy{Run: "r1", At: at, File: "javo-1.db", Snapshot: "abcdef0123",
		Size: 1258291, Added: 4000, Uploaded: 2048, Took: 1.25, Kept: 7, Removed: 1, Dest: "s3://backups/javo/database"}, at); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Db.Homes().RequestBackupRun(ctx, gatewaytest.Actor, "r1", at); err != nil {
		t.Fatal(err)
	}
	p := apitest.Decode[struct {
		Steps []struct {
			Kind, Name, Person, Claim, Dest string
			Account                         bool
			Log                             []string
		}
	}](t, f.Do(http.MethodGet, "/api/admin/backups/runs/r1?logs=true", "", admin))
	by := map[string]int{}
	for i, s := range p.Steps {
		by[s.Kind+"/"+s.Name] = i
	}
	db := p.Steps[by["database/javo-1.db"]]
	log := strings.Join(db.Log, "\n")
	for _, want := range []string{"14:03:12 step=database action=dump file=javo-1.db size=1.2MiB",
		"action=upload snapshot=abcdef01 added=3.9KiB uploaded=2.0KiB took=1.2s dest=s3://backups/javo/database",
		"action=forget kept=7 removed=1"} {
		if !strings.Contains(log, want) {
			t.Errorf("database log lacks %q:\n%s", want, log)
		}
	}
	alice := p.Steps[by["home/"+apitest.PersonID("alice")]]
	if !alice.Account || alice.Person != "Alice (alice, SSO)" || alice.Dest != "s3://backups/javo/homes/"+gatewaytest.Namespace+"/"+apitest.PersonID("alice") || alice.Claim != naming.HomeClaimName(apitest.PersonID("alice")) {
		t.Errorf("alice's step = %+v", alice)
	}
	old := p.Steps[by["home/local-alice-6f5951a4"]]
	if old.Account || old.Person != "Home volume with no account" {
		t.Errorf("the volume without an account = %+v", old)
	}
}

package admin_test

import (
	"net/http"
	"testing"
	"time"

	adminapi "github.com/nebari-dev/javo/internal/gateway/admin"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// The admin sees the database backups, newest first, and their health; a
// schedule with a recent backup is healthy, and Back up now adds one.
func TestAdminListsAndTakesDatabaseBackups(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	dir := t.TempDir()
	apitest.BackupOf(t, dir, now.Add(-30*time.Hour))
	newest := apitest.BackupOf(t, dir, now.Add(-6*time.Hour))
	f := apitest.New(t, apitest.Options{Now: func() time.Time { return now }, Backups: apitest.SqliteBackups(dir)})
	admin := f.Session(apitest.AdminID, "javo-admins")

	got := apitest.Decode[apitest.BackupsJSON](t, f.Do(http.MethodGet, "/api/admin/backups", "", admin))
	if got.Database.Engine != "sqlite" || len(got.Database.Items) != 2 || got.Database.Items[0].Name != newest || got.Database.Items[0].Size == 0 {
		t.Fatalf("backups = %+v", got.Database)
	}
	if got.Database.Status.Reason != string(reasons.DatabaseBackedUp) {
		t.Errorf("status = %+v", got.Database.Status)
	}

	if storetest.Engine() != store.EngineSQLite {
		return // Back up now copies the live database, which is SQLite only.
	}
	if w := f.Do(http.MethodPost, "/api/admin/backups", "", admin); w.Code != http.StatusCreated {
		t.Fatalf("back up now: %d %s", w.Code, w.Body)
	}
	if got := apitest.Decode[apitest.BackupsJSON](t, f.Do(http.MethodGet, "/api/admin/backups", "", admin)); len(got.Database.Items) != 3 {
		t.Errorf("after back up now: %d backups", len(got.Database.Items))
	}
}

// Without a schedule, or with backups older than two scheduled runs, the
// status says so; with Postgres the backups are the CronJob's.
func TestDatabaseBackupStatus(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	stale := t.TempDir()
	apitest.BackupOf(t, stale, now.Add(-72*time.Hour))
	for _, c := range []struct {
		name string
		cfg  adminapi.BackupConfig
		want reasons.Code
	}{
		{"no schedule", adminapi.BackupConfig{Engine: store.EngineSQLite}, reasons.DatabaseBackupsOff},
		{"stale", apitest.SqliteBackups(stale), reasons.DatabaseBackupStale},
		{"none yet", apitest.SqliteBackups(t.TempDir()), reasons.DatabaseBackupNone},
		{"postgres", adminapi.BackupConfig{Engine: store.EnginePostgres}, reasons.DatabaseBackupsByCronJob},
	} {
		f := apitest.New(t, apitest.Options{Now: func() time.Time { return now }, Backups: c.cfg})
		got := apitest.Decode[apitest.BackupsJSON](t, f.Do(http.MethodGet, "/api/admin/backups", "", f.Session(apitest.AdminID, "javo-admins")))
		if got.Database.Status.Reason != string(c.want) {
			t.Errorf("%s: status %+v, want %s", c.name, got.Database.Status, c.want)
		}
	}
}

// The restore preview compares the backup with now: an app that is gone
// comes back, one that exists is left alone, one whose owner or template is
// gone cannot come back. Restore brings back only the first, held.
func TestRestorePreviewAndRestore(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	dir := t.TempDir()
	alice := apitest.PersonID("alice")
	gone, kept := gatewaytest.App(alice, "gone"), gatewaytest.App(alice, "kept")
	ghost := gatewaytest.App("ghost-00000000", "orphan")
	noTpl := gatewaytest.App(alice, "old")
	noTpl.Template = "retired"
	name := apitest.BackupOf(t, dir, now.Add(-time.Hour), gone, kept, ghost, noTpl)
	f := apitest.New(t, apitest.Options{Now: func() time.Time { return now }, Backups: apitest.SqliteBackups(dir), People: []apitest.TestPerson{apitest.SignedIn("alice", time.Time{})}},
		gatewaytest.Template("t", "streamlit"), gatewaytest.App(alice, "kept"))
	admin := f.Session(apitest.AdminID, "javo-admins")

	w := f.Do(http.MethodPost, "/api/admin/backups/"+name+"/preview", `{}`, admin)
	if w.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
	outcomes := map[string]string{}
	for _, r := range apitest.Decode[apitest.RestoreRowsJSON](t, w).Rows {
		outcomes[r.Name] = r.Outcome
	}
	want := map[string]string{"gone": "Create", "kept": "Exists", "orphan": "OwnerUnknown", "old": "TemplateMissing"}
	for k, v := range want {
		if outcomes[k] != v {
			t.Errorf("%s: %q, want %q (all %v)", k, outcomes[k], v, outcomes)
		}
	}
	if f.WorkspaceCount() != 1 {
		t.Fatal("the preview changed the apps")
	}

	// Scoped to one person: only their apps are in the preview.
	w = f.Do(http.MethodPost, "/api/admin/backups/"+name+"/preview", `{"owner":"ghost-00000000"}`, admin)
	if rows := apitest.Decode[apitest.RestoreRowsJSON](t, w).Rows; len(rows) != 1 || rows[0].Name != "orphan" {
		t.Errorf("owner scope: %+v", rows)
	}

	w = f.Do(http.MethodPost, "/api/admin/backups/"+name+"/restore", `{}`, admin)
	if w.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	back := f.GetWorkspace(alice, "gone")
	if back.RestoreHold != name || !back.Held(now) {
		t.Errorf("restored app hold = %q", back.RestoreHold)
	}
	if f.WorkspaceCount() != 2 {
		t.Errorf("apps after restore = %d, want kept and gone", f.WorkspaceCount())
	}
}

// Only a listed backup can be read: a name with a path in it is not found.
func TestRestorePreviewNeedsAListedBackup(t *testing.T) {
	f := apitest.New(t, apitest.Options{Backups: apitest.SqliteBackups(t.TempDir())})
	admin := f.Session(apitest.AdminID, "javo-admins")
	for _, n := range []string{"javo-20260101T000000Z.db", "..%2Fjavo.db"} {
		if w := f.Do(http.MethodPost, "/api/admin/backups/"+n+"/preview", `{}`, admin); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s", n, w.Code, w.Body)
		}
	}
}

// The Backups page says whether home folders are backed up: without a
// backup bucket they are in no backup; with one, the bucket's part tells.
func TestAdminBackupsSayHomeFoldersNeedTheBucket(t *testing.T) {
	type homesJSON struct {
		HomeFolders *struct {
			Reason  string `json:"reason"`
			Problem bool   `json:"problem"`
		} `json:"homeFolders"`
		Bucket *struct{} `json:"bucket"`
	}
	f := apitest.New(t, apitest.Options{})
	got := apitest.Decode[homesJSON](t, f.Do(http.MethodGet, "/api/admin/backups", "", f.Session(apitest.AdminID, "javo-admins")))
	if got.HomeFolders == nil || got.HomeFolders.Reason != string(reasons.NoBackupTool) || !got.HomeFolders.Problem {
		t.Errorf("without a bucket: %+v", got.HomeFolders)
	}

	var bf *apitest.Fixture
	cfg := apitest.SqliteBackups(t.TempDir())
	cfg.Bucket = apitest.BucketWith(t, &bf, &apitest.Copies{}, nil, nil)
	bf = apitest.New(t, apitest.Options{Backups: cfg})
	got = apitest.Decode[homesJSON](t, bf.Do(http.MethodGet, "/api/admin/backups", "", bf.Session(apitest.AdminID, "javo-admins")))
	if got.HomeFolders != nil || got.Bucket == nil {
		t.Errorf("with a bucket: homeFolders %+v, bucket %v", got.HomeFolders, got.Bucket)
	}
}

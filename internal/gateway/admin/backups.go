package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/robfig/cron/v3"

	"github.com/nebari-dev/javo/internal/gateway/apps"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// BackupConfig is how the install backs up its database: the engine, and
// for SQLite the server's own scheduled backups (javo server --backup-*).
type BackupConfig struct {
	Engine string
	// Dir holds the SQLite backups; empty when the server takes none.
	Dir      string
	Schedule string
	Keep     int
	// Bucket is the S3-compatible bucket backups go to; nil when none.
	Bucket *Bucket
}

// BackupStatus is a catalog reason, for the status cards of the Backups page.
type BackupStatus struct {
	Reason string `json:"reason"`
	Title  string `json:"title"`
	What   string `json:"what"`
	Todo   string `json:"todo"`
	// Problem is true when something needs the admin's attention.
	Problem bool `json:"problem"`
}

func noticeOf(code reasons.Code, problemJSON bool) BackupStatus {
	r, _ := reasons.Lookup(code)
	return BackupStatus{Reason: string(r.Code), Title: r.Title, What: r.What, Todo: r.Todo, Problem: problemJSON}
}

// DatabaseBackup is one backup file.
type DatabaseBackup struct {
	Name    string    `json:"name"`
	TakenAt time.Time `json:"takenAt"`
	Size    int64     `json:"size" doc:"Bytes"`
}

// DatabaseBackups is the state of the database backups.
type DatabaseBackups struct {
	Engine   string `json:"engine" enum:"sqlite,postgres"`
	Schedule string `json:"schedule" doc:"Cron schedule of the backups; empty when none is set"`
	Keep     int    `json:"keep"`
	// NextAt is the next scheduled backup; absent without a schedule.
	NextAt *time.Time   `json:"nextAt,omitempty"`
	Status BackupStatus `json:"status"`
	// CanBackUpNow and CanRestore say whether this install's backups are
	// files the server holds (SQLite with a schedule).
	CanBackUpNow bool             `json:"canBackUpNow"`
	Items        []DatabaseBackup `json:"items"`
}

type backupsOutput struct {
	Body struct {
		Database DatabaseBackups `json:"database"`
		// HomeFolders says home folders are in no backup; absent with a
		// bucket, whose part tells their backups.
		HomeFolders *BackupStatus `json:"homeFolders,omitempty"`
		// Bucket is the full backups to the backup bucket; absent without one.
		Bucket *bucketState `json:"bucket,omitempty"`
	}
}

type backupNowOutput struct {
	Body DatabaseBackup
}

type restoreScope struct {
	Owner string `json:"owner,omitempty" maxLength:"253" doc:"Only this person's apps (identity name)"`
	App   string `json:"app,omitempty" maxLength:"63" doc:"Only the app with this short name"`
}

type restoreInput struct {
	Name string `path:"name" maxLength:"253"`
	Body restoreScope
}

func (a *API) registerBackups(api huma.API) {
	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminListBackups", Method: http.MethodGet, Path: "/api/admin/backups",
		Summary: "The database backups and their health (platform admins)",
	}, func(ctx context.Context, _ *struct{}) (*backupsOutput, error) {
		db, err := a.databaseBackups()
		if err != nil {
			return nil, problem.Internal("list backups", err)
		}
		out := &backupsOutput{}
		out.Body.Database = db
		if out.Body.Bucket, err = a.bucket(ctx); err != nil {
			return nil, problem.Internal("read bucket backups", err)
		}
		if out.Body.Bucket == nil {
			n := noticeOf(reasons.NoBackupTool, true)
			out.Body.HomeFolders = &n
		}
		return out, nil
	})
	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminBackUpNow", Method: http.MethodPost, Path: "/api/admin/backups",
		Summary: "Back up the database now (platform admins)", DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusConflict},
	}, func(ctx context.Context, _ *struct{}) (*backupNowOutput, error) {
		if !a.filesHere() {
			return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgBackupNotHere))
		}
		at := a.Now()
		name := store.BackupName(at)
		if err := a.DB.Backup(ctx, filepath.Join(a.Backups.Dir, name)); err != nil {
			return nil, problem.Internal("back up the database", err)
		}
		if err := store.PruneBackups(a.Backups.Dir, max(a.Backups.Keep, 1)); err != nil {
			return nil, problem.Internal("prune backups", err)
		}
		if b := a.Backups.Bucket; b != nil && b.CopyDatabase != nil {
			// The outcome is recorded for the page; the local backup stands either way.
			if err := b.CopyDatabase(ctx, filepath.Join(a.Backups.Dir, name), ""); err != nil {
				slog.Warn("copy the database backup to the bucket", "file", name, "err", err)
			}
		}
		files, err := store.ListBackups(a.Backups.Dir)
		if err != nil {
			return nil, problem.Internal("list backups", err)
		}
		out := &backupNowOutput{}
		for _, f := range files {
			if f.Name == name {
				out.Body = DatabaseBackup{Name: f.Name, TakenAt: f.TakenAt, Size: f.Size}
			}
		}
		return out, nil
	})
	for _, apply := range []bool{false, true} {
		op := huma.Operation{
			OperationID: "adminPreviewRestore", Method: http.MethodPost, Path: "/api/admin/backups/{name}/preview",
			Summary: "What restoring apps from a database backup would do; nothing is changed (platform admins)",
			Errors:  []int{http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		}
		if apply {
			op.OperationID, op.Path, op.Summary = "adminRestore", "/api/admin/backups/{name}/restore",
				"Bring back apps from a database backup; they wait for a release (platform admins)"
		}
		authn.RegisterAdmin(api, a.Roles, op, func(ctx context.Context, in *restoreInput) (*apps.TransferOutput, error) {
			return a.restoreApps(ctx, in.Name, in.Body, apply)
		})
	}
}

// filesHere is whether the backups are files this server holds.
func (a *API) filesHere() bool {
	return a.Backups.Engine == store.EngineSQLite && a.Backups.Dir != ""
}

func (a *API) databaseBackups() (DatabaseBackups, error) {
	b := a.Backups
	out := DatabaseBackups{Engine: b.Engine, Schedule: b.Schedule, Keep: b.Keep, Items: []DatabaseBackup{}}
	if b.Engine == store.EnginePostgres {
		out.Status = noticeOf(reasons.DatabaseBackupsByCronJob, false)
		return out, nil
	}
	if !a.filesHere() || b.Schedule == "" {
		out.Status = noticeOf(reasons.DatabaseBackupsOff, true)
		return out, nil
	}
	out.CanBackUpNow = true
	files, err := store.ListBackups(b.Dir)
	if err != nil {
		return out, err
	}
	for _, f := range files {
		out.Items = append(out.Items, DatabaseBackup{Name: f.Name, TakenAt: f.TakenAt, Size: f.Size})
	}
	now := a.Now()
	sched, err := cron.ParseStandard(b.Schedule)
	if err != nil {
		// The server refuses to start with a bad schedule; this is only reached
		// by a gateway given another one.
		out.Status = noticeOf(reasons.DatabaseBackupsOff, true)
		return out, nil
	}
	next := sched.Next(now)
	out.NextAt = &next
	switch {
	case len(files) == 0:
		out.Status = noticeOf(reasons.DatabaseBackupNone, false)
	case sched.Next(sched.Next(files[0].TakenAt)).Before(now):
		// Two scheduled runs went by without a newer backup.
		out.Status = noticeOf(reasons.DatabaseBackupStale, true)
	default:
		out.Status = noticeOf(reasons.DatabaseBackedUp, false)
	}
	return out, nil
}

// restoreApps compares the apps in the backup with the database now and,
// with apply, brings back the ones that are gone, held under the backup's
// name so nothing starts until the admin releases them.
func (a *API) restoreApps(ctx context.Context, name string, scope restoreScope, apply bool) (*apps.TransferOutput, error) {
	if !a.filesHere() {
		return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgBackupNotHere))
	}
	files, err := store.ListBackups(a.Backups.Dir)
	if err != nil {
		return nil, problem.Internal("list backups", err)
	}
	// Only a listed name: the path is never taken from the request.
	if !slices.ContainsFunc(files, func(f store.BackupFile) bool { return f.Name == name }) {
		return nil, problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgBackupNotFound))
	}
	var saved []model.App
	err = store.ReadBackup(ctx, filepath.Join(a.Backups.Dir, name), func(b *store.Store) error {
		all, err := b.Apps().ListAll(ctx)
		for _, app := range all {
			// A preview of a pull request is made again by its app, not restored.
			if app.DeletedAt.IsZero() && app.ParentID == "" && (scope.Owner == "" || app.Owner == scope.Owner) && (scope.App == "" || app.Name == scope.App) {
				saved = append(saved, app)
			}
		}
		return err
	})
	if err != nil {
		return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgBackupUnreadable))
	}
	out := &apps.TransferOutput{}
	out.Body.Rows = []apps.TransferRow{}
	for i := range saved {
		app := &saved[i]
		row := apps.TransferRow{Name: app.Name, DisplayName: app.DisplayName, Template: app.Template, Owner: app.Owner, WasRunning: !app.Paused}
		var back *model.App
		row.Outcome, row.Message, back, err = a.restoreOutcome(ctx, app)
		if err != nil {
			return nil, problem.Internal("compare backup", err)
		}
		if apply && row.Outcome == apps.OutcomeCreate {
			back.RestoreHold = name
			_, err := a.DB.Apps().Create(ctx, authn.Actor(ctx), back)
			capped := problem.Limit(err)
			switch {
			case errors.Is(err, store.ErrExists):
				row.Outcome, row.Message = apps.OutcomeExists, reasons.Msg(reasons.MsgRestoreExists)
			case capped != nil:
				// The owner is at a cap: this app stays out, the rest go on.
				row.Outcome, row.Message = apps.OutcomeInvalid, capped.Title
			case err != nil:
				return nil, problem.Internal("restore app", err)
			default:
				row.Outcome, row.Message = apps.OutcomeCreated, reasons.Msg(reasons.MsgRestoreCreated)
			}
		}
		out.Body.Rows = append(out.Body.Rows, row)
	}
	return out, nil
}

// restoreOutcome is what restoring app would do now, and with Create the
// app to store: checked as its owner would create it today
// (Workspaces.RestoreCheck), so a rule made stricter since the backup is not
// undone by a restore.
func (a *API) restoreOutcome(ctx context.Context, app *model.App) (string, string, *model.App, error) {
	switch _, err := a.DB.Apps().GetByOwnerName(ctx, app.Owner, app.Name); {
	case err == nil:
		return apps.OutcomeExists, reasons.Msg(reasons.MsgRestoreExists), nil, nil
	case !errors.Is(err, store.ErrNotFound):
		return "", "", nil, err
	}
	person, err := a.People.ByName(ctx, app.Owner)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return apps.OutcomeOwnerUnknown, reasons.Msg(reasons.MsgRestoreOwnerGone, app.Owner), nil, nil
	case err != nil:
		return "", "", nil, err
	}
	switch _, err := a.DB.Templates().Get(ctx, app.Template); {
	case errors.Is(err, store.ErrNotFound):
		return apps.OutcomeTemplateMissing, reasons.Msg(reasons.MsgRestoreTemplateGone, app.Template), nil, nil
	case err != nil:
		return "", "", nil, err
	}
	if a.Workspaces == nil {
		return "", "", nil, errors.New("no app rules to check a restore against")
	}
	back, refusal, err := a.Workspaces.RestoreCheck(ctx, identity.PrincipalOf(person), *app)
	switch {
	case err != nil:
		return "", "", nil, err
	case refusal != "":
		return apps.OutcomeInvalid, refusal, nil, nil
	}
	return apps.OutcomeCreate, reasons.Msg(reasons.MsgRestoreCreate), back, nil
}

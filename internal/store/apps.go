package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

const (
	shareUser  = "user"
	shareGroup = "group"
)

// appSpec is the part of an app's spec that is not a column.
type appSpec struct {
	DisplayName string `json:"displayName,omitempty"`
	File        string `json:"file,omitempty"`
	Sample      bool   `json:"sample,omitempty"`
	Command     string `json:"command,omitempty"`
	Image       string `json:"image,omitempty"`
	Port        int32  `json:"port,omitempty"`
	APIAccess   string `json:"apiAccess,omitempty"`
	// AgentPrompt: see model.App.
	AgentPrompt string `json:"agentPrompt,omitempty"`
	// Env: see model.App.
	Env []model.EnvVar `json:"env,omitempty"`
	// StoppedIdleAfter: see model.App.
	StoppedIdleAfter int32 `json:"stoppedIdleAfter,omitempty"`
	StoppedByAdmin   bool  `json:"stoppedByAdmin,omitempty"`
	// Source is the Git repository the app runs from; nil for a home folder app.
	Source *model.Source `json:"source,omitempty"`
	// Environment: see model.App; its files are in app_environments.
	Environment *model.Environment `json:"environment,omitempty"`
}

// Apps is the apps and app_shares tables. The gateway writes spec columns
// through Create, UpdateSpec and MarkDeleted; the controller writes status
// columns through SetStatus and removes the row with Purge.
type Apps struct{ s *Store }

// Apps returns the apps table.
func (s *Store) Apps() *Apps { return &Apps{s} }

// Create stores a new app at version 1. A taken (owner, name), also one
// held by an app marked deleted and not yet purged, is ErrExists.
func (a *Apps) Create(ctx context.Context, actor model.Actor, in *model.App) (model.App, error) {
	if err := checkEnvironment(in.Environment, nil); err != nil {
		return model.App{}, err
	}
	return a.create(ctx, actor, in, nil)
}

// RepoSecrets are what an app from a repository is created with besides its
// spec: the token the person pasted, and the webhook address and secret
// (NewHook) shown to them before the app existed. Any may be empty.
type RepoSecrets struct {
	Token              string
	HookID, HookSecret string
}

// CreateFromRepo is CreateWith for an app from a repository with no secrets:
// its token, its webhook or both. The audit row carries no secret.
func (a *Apps) CreateFromRepo(ctx context.Context, actor model.Actor, in *model.App, s RepoSecrets) (model.App, error) {
	if s.Token == "" && s.HookID == "" {
		return model.App{}, errors.New("store: an app from a repository with no token and no webhook is a plain Create")
	}
	return a.CreateWith(ctx, actor, in, AppExtras{Repo: s})
}

// AppExtras is what CreateWith stores with a new app: its repository secrets
// (empty: none) and the person's secrets it gets, by variable name.
type AppExtras struct {
	Repo    RepoSecrets
	Secrets []model.SecretBinding
	// Environment is the files of the environment the app names; nil when
	// it names none.
	Environment *model.EnvironmentFiles
}

// CreateWith is Create plus extras, in the same transaction: a refused app
// leaves no token, webhook or binding behind; a webhook address already in
// use, or a binding that names no secret of the owner (*SecretMissingError),
// refuses the app. See GitTokens for what happens to the token next. The
// audit row carries no secret.
func (a *Apps) CreateWith(ctx context.Context, actor model.Actor, in *model.App, extras AppExtras) (model.App, error) {
	r := extras.Repo
	if err := checkEnvironment(in.Environment, extras.Environment); err != nil {
		return model.App{}, err
	}
	if r == (RepoSecrets{}) && len(extras.Secrets) == 0 && extras.Environment == nil {
		return a.Create(ctx, actor, in)
	}
	if (r.HookID == "") != (r.HookSecret == "") {
		return model.App{}, errors.New("store: a webhook needs both its address and its secret")
	}
	var hookSecret string
	if r.HookID != "" {
		var err error
		if hookSecret, err = a.s.sealHook(r.HookID, r.HookSecret); err != nil {
			return model.App{}, err
		}
	}
	return a.create(ctx, actor, in, func(q *sqlcgen.Queries, id string) error {
		if r.Token != "" {
			if err := q.UpsertGitToken(ctx, sqlcgen.UpsertGitTokenParams{AppID: id, Token: r.Token, SetAt: dbtime.From(now())}); err != nil {
				return err
			}
		}
		if r.HookID != "" {
			if err := q.UpsertPreviewHook(ctx, sqlcgen.UpsertPreviewHookParams{
				AppID: id, HookID: sql.NullString{String: r.HookID, Valid: true}, HookSecret: hookSecret,
			}); err != nil {
				return err
			}
		}
		if err := writeAppEnvironment(ctx, q, id, extras.Environment); err != nil {
			return err
		}
		return writeAppBindings(ctx, q, id, in.Owner, extras.Secrets)
	})
}

// create is Create; with alsoWrite it runs it in the same transaction, after
// the app row exists.
func (a *Apps) create(ctx context.Context, actor model.Actor, in *model.App, alsoWrite func(q *sqlcgen.Queries, id string) error) (model.App, error) {
	app := *in
	app.ApplyDefaults()
	if err := app.Validate(); err != nil {
		return model.App{}, err
	}
	spec, err := marshal(specOf(app), "{}")
	if err != nil {
		return model.App{}, err
	}
	id := newID()
	var out model.App
	err = a.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		if err := within(ctx, q, app.Owner, a.s.appCaps(ctx, q, app.Owner, app.ParentID != "", !app.Paused)...); err != nil {
			return effect{}, err
		}
		if err := insertApp(ctx, q, id, app, spec, now()); err != nil {
			return effect{}, err
		}
		if alsoWrite != nil {
			if err := alsoWrite(q, id); err != nil {
				return effect{}, err
			}
		}
		var err error
		if out, err = readApp(ctx, q, id); err != nil {
			return effect{}, err
		}
		return appKind.effect("app.create", out), nil
	})
	return out, err
}

// insertApp writes the row and the shares of a new app at version 1; a taken
// (owner, name) or object name is ErrExists. Create and Import share it.
func insertApp(ctx context.Context, q *sqlcgen.Queries, id string, app model.App, spec string, createdAt time.Time) error {
	cpu, mem := resourceColumns(app.Resources)
	n, err := q.InsertApp(ctx, sqlcgen.InsertAppParams{
		ID: id, Namespace: app.Namespace, Name: app.Name, Owner: app.Owner, Template: app.Template,
		Size: app.Size, Paused: app.Paused, Access: app.Sharing.Access, Spec: spec,
		CreatedAt: dbtime.From(createdAt), RestartRequestedAt: nullTime(app.RestartRequestedAt),
		ObjectName: app.ObjectName(), CpuMillis: cpu, MemoryBytes: mem, IdleStopMinutes: app.IdleStopMinutes,
		RestoreHold: app.RestoreHold,
		ParentID:    sql.NullString{String: app.ParentID, Valid: app.ParentID != ""}, PullNumber: int64(app.PullNumber),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrExists
	}
	return writeShares(ctx, q, id, app.Sharing)
}

var appKind = rowKind[model.App]{
	kind: "app", table: "apps", read: readApp,
	info: func(a model.App) rowInfo {
		return rowInfo{ID: a.ID, Namespace: a.Namespace, Name: a.Name, Owner: a.Owner, Version: a.Version, Deleted: !a.DeletedAt.IsZero()}
	},
	prepare:  func(a *model.App) { a.ApplyDefaults() },
	validate: func(next, old *model.App) error { return next.ValidateUpdate(old) },
	// Starting a stopped app (not a preview) counts against RunningApps.
	guard: func(ctx context.Context, s *Store, q *sqlcgen.Queries, next, old *model.App) error {
		if !old.Paused || next.Paused || next.ParentID != "" {
			return nil
		}
		return within(ctx, q, next.Owner, s.runningCap(ctx, q, next.Owner))
	},
	writeSpec: func(ctx context.Context, q *sqlcgen.Queries, id string, version int64, next *model.App) (int64, error) {
		spec, err := marshal(specOf(*next), "{}")
		if err != nil {
			return 0, err
		}
		cpu, mem := resourceColumns(next.Resources)
		n, err := q.UpdateAppSpec(ctx, sqlcgen.UpdateAppSpecParams{
			Template: next.Template, Size: next.Size, Paused: next.Paused, Access: next.Sharing.Access, Spec: spec,
			RestartRequestedAt: nullTime(next.RestartRequestedAt), CpuMillis: cpu, MemoryBytes: mem,
			IdleStopMinutes: next.IdleStopMinutes, ID: id, Version: version,
		})
		if err != nil || n == 0 {
			return n, err
		}
		return n, writeShares(ctx, q, id, next.Sharing)
	},
	deleteRow: func(ctx context.Context, q *sqlcgen.Queries, id string, at time.Time) (int64, error) {
		return q.MarkAppDeleted(ctx, sqlcgen.MarkAppDeletedParams{DeletedAt: nullTime(at), ID: id})
	},
	// A parent goes last: its previews are purged first.
	purgeRow: func(ctx context.Context, q *sqlcgen.Queries, id string) (int64, error) {
		if n, err := q.CountAppChildren(ctx, sql.NullString{String: id, Valid: true}); err != nil || n > 0 {
			if err == nil {
				err = ErrConflict
			}
			return 0, err
		}
		return q.PurgeApp(ctx, id)
	},
	writeStatus: func(ctx context.Context, q *sqlcgen.Queries, id string, observed int64, status string, at time.Time) (int64, error) {
		return q.SetAppStatus(ctx, sqlcgen.SetAppStatusParams{Status: status, ObservedVersion: observed, StatusAt: nullTime(at), ID: id})
	},
	onStatus: recordUsage,
}

// phaseRunning is the AppStatus.Phase that counts as running for usage.
const phaseRunning = "Running"

// recordUsage opens a usage interval when the app's phase becomes Running and
// closes it when it stops being Running, in the transaction that stores the
// phase, so a retried status write cannot open a second interval.
func recordUsage(ctx context.Context, q *sqlcgen.Queries, id string, status any, at time.Time) error {
	next, ok := status.(model.AppStatus)
	if !ok {
		return nil
	}
	app, err := readApp(ctx, q, id)
	if err != nil {
		return err
	}
	switch was, is := app.Status.Phase == phaseRunning, next.Phase == phaseRunning; {
	case !was && is:
		size := next.Size
		if size == "" {
			size = app.Size
		}
		return q.InsertUsage(ctx, sqlcgen.InsertUsageParams{
			ID: newID(), AppID: id, Owner: app.Owner, Template: app.Template, Size: size, StartedAt: dbtime.From(at),
		})
	case was && !is:
		_, err := q.CloseUsage(ctx, sqlcgen.CloseUsageParams{StoppedAt: nullTime(at), AppID: id})
		return err
	}
	return nil
}

// resourceColumns is how a custom amount is stored: millicores and bytes,
// both 0 for none.
func resourceColumns(r *model.AppResources) (cpuMillis, memoryBytes int64) {
	if r == nil {
		return 0, 0
	}
	return r.CPU.MilliValue(), r.Memory.Value()
}

// resourcesOf reads the custom amount columns back; nil when the app runs at
// a named size.
func resourcesOf(cpuMillis, memoryBytes int64) *model.AppResources {
	if cpuMillis == 0 && memoryBytes == 0 {
		return nil
	}
	return &model.AppResources{
		CPU:    *resource.NewMilliQuantity(cpuMillis, resource.DecimalSI),
		Memory: *resource.NewQuantity(memoryBytes, resource.BinarySI),
	}
}

func specOf(a model.App) appSpec {
	return appSpec{DisplayName: a.DisplayName, File: a.File, Sample: a.Sample, Command: a.Command, Image: a.Image, Port: a.Port, APIAccess: a.APIAccess, AgentPrompt: a.AgentPrompt, Env: a.Env,
		StoppedIdleAfter: a.StoppedIdleAfter, Source: a.Source, StoppedByAdmin: a.StoppedByAdmin, Environment: a.Environment}
}

func writeShares(ctx context.Context, q *sqlcgen.Queries, appID string, sh model.Sharing) error {
	if err := q.DeleteAppShares(ctx, appID); err != nil {
		return err
	}
	for kind, values := range map[string][]string{shareUser: sh.Users, shareGroup: sh.Groups} {
		for _, v := range values {
			if err := q.InsertAppShare(ctx, sqlcgen.InsertAppShareParams{AppID: appID, Kind: kind, Value: v}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Get returns the app (also one marked deleted) or ErrNotFound.
func (a *Apps) Get(ctx context.Context, id string) (model.App, error) {
	return readApp(ctx, a.s.q, id)
}

// GetByOwnerName returns the person's app with this short name (also one
// marked deleted), or ErrNotFound. Two people may use the same short name.
func (a *Apps) GetByOwnerName(ctx context.Context, owner, name string) (model.App, error) {
	row, err := a.s.q.GetAppByOwnerName(ctx, sqlcgen.GetAppByOwnerNameParams{Owner: owner, Name: name})
	return loadApp(ctx, a.s.q, row, err)
}

// GetByObjectName returns the app whose Pod and Service carry this name (also
// one marked deleted), or ErrNotFound.
func (a *Apps) GetByObjectName(ctx context.Context, objectName string) (model.App, error) {
	row, err := a.s.q.GetAppByObjectName(ctx, objectName)
	return loadApp(ctx, a.s.q, row, err)
}

// ParentOf returns the app the preview app previews, as it is now, for
// authz.Object.Parent. It is nil for an app that is not a preview and for a
// preview whose parent is gone or marked deleted; authz then opens the
// preview only for its owner and platform admins.
func (a *Apps) ParentOf(ctx context.Context, app *model.App) (*model.App, error) {
	if app == nil || app.ParentID == "" {
		return nil, nil
	}
	parent, err := a.Get(ctx, app.ParentID)
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	case !parent.DeletedAt.IsZero():
		return nil, nil
	}
	return &parent, nil
}

func readApp(ctx context.Context, q *sqlcgen.Queries, id string) (model.App, error) {
	row, err := q.GetApp(ctx, id)
	return loadApp(ctx, q, row, err)
}

// loadApp finishes a single-row lookup: the not-found mapping, the shares.
func loadApp(ctx context.Context, q *sqlcgen.Queries, row sqlcgen.App, err error) (model.App, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return model.App{}, ErrNotFound
	}
	if err != nil {
		return model.App{}, err
	}
	shares, err := q.ListAppShares(ctx, row.ID)
	if err != nil {
		return model.App{}, err
	}
	return appOf(row, shares)
}

// List returns the person's apps, newest first, without deleted ones and,
// unless previews is true, without previews.
func (a *Apps) List(ctx context.Context, owner string, previews bool) ([]model.App, error) {
	rows, err := a.s.q.ListAppsByOwner(ctx, sqlcgen.ListAppsByOwnerParams{Owner: owner, Column2: previews})
	if err != nil {
		return nil, err
	}
	shares, err := a.s.q.ListAppSharesByOwner(ctx, owner)
	if err != nil {
		return nil, err
	}
	return appsOf(rows, shares)
}

// ListAll returns every app, deleted ones included: the controller's resync.
func (a *Apps) ListAll(ctx context.Context) ([]model.App, error) {
	rows, err := a.s.q.ListAllApps(ctx)
	if err != nil {
		return nil, err
	}
	shares, err := a.s.q.ListAllAppShares(ctx)
	if err != nil {
		return nil, err
	}
	return appsOf(rows, shares)
}

// ListShared returns other people's apps that name the person or one of
// their groups in their sharing list, or that are open to everyone signed in
// or to anyone with the link, newest first. It only finds candidates: whether
// the person may open one is authz.Decide's answer. Previews are left out
// unless previews is true.
func (a *Apps) ListShared(ctx context.Context, person string, groups []string, previews bool) ([]model.App, error) {
	byID := map[string]model.App{}
	addRows := func(rows []sqlcgen.App) error {
		for _, row := range rows {
			if _, seen := byID[row.ID]; seen {
				continue
			}
			shares, err := a.s.q.ListAppShares(ctx, row.ID)
			if err != nil {
				return err
			}
			app, err := appOf(row, shares)
			if err != nil {
				return err
			}
			byID[row.ID] = app
		}
		return nil
	}
	add := func(kind, value string) error {
		rows, err := a.s.q.ListAppsSharedWith(ctx, sqlcgen.ListAppsSharedWithParams{Kind: kind, Value: value, Owner: person, Column4: previews})
		if err != nil {
			return err
		}
		return addRows(rows)
	}
	if err := add(shareUser, person); err != nil {
		return nil, err
	}
	for _, g := range groups {
		if err := add(shareGroup, g); err != nil {
			return nil, err
		}
	}
	open, err := a.s.q.ListAppsOpenToAll(ctx, sqlcgen.ListAppsOpenToAllParams{Owner: person, Column2: previews})
	if err != nil {
		return nil, err
	}
	if err := addRows(open); err != nil {
		return nil, err
	}
	out := make([]model.App, 0, len(byID))
	for _, app := range byID {
		out = append(out, app)
	}
	slices.SortFunc(out, func(x, y model.App) int {
		if c := y.CreatedAt.Compare(x.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(y.ID, x.ID)
	})
	return out, nil
}

// UpdateSpec changes an app's spec. It reads the app, applies fn, validates
// the result and writes it only if the stored version is still version (else
// ErrConflict), raising the version by one. An app marked deleted is
// ErrNotFound. Status columns are not touched.
func (a *Apps) UpdateSpec(ctx context.Context, actor model.Actor, id string, version int64, fn func(*model.App) error) (model.App, error) {
	return appKind.update(ctx, a.s, actor, id, version, fn)
}

// NoteTokenRevoked records that the app's API token id was revoked and wakes
// the controller, which then makes the app a new one. The spec and version
// stay as they are. The write is audited. A missing or deleted app is
// ErrNotFound.
func (a *Apps) NoteTokenRevoked(ctx context.Context, actor model.Actor, id, tokenID string) error {
	return a.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		n, err := q.SetAppTokenRevoked(ctx, sqlcgen.SetAppTokenRevokedParams{TokenRevoked: tokenID, ID: id})
		if err != nil {
			return effect{}, err
		}
		if n == 0 {
			return effect{}, ErrNotFound
		}
		app, err := appKind.read(ctx, q, id)
		if err != nil {
			return effect{}, err
		}
		return appKind.effect("app.token_revoked", app), nil
	})
}

// StopIdle stops the app the way a person's Stop does (the same versioned
// spec write) and records that it was for being idle, at its timeout. The
// controller calls it with the version it judged idle; a changed app is
// ErrConflict, so a start or a new timeout in between wins.
func (a *Apps) StopIdle(ctx context.Context, id string, version int64) (model.App, error) {
	actor := model.Actor{System: model.SystemController}
	return appKind.updateAs(ctx, a.s, actor, "app.stop_idle", id, version, func(app *model.App) error {
		app.Paused, app.StoppedIdleAfter = true, app.IdleStopMinutes
		return nil
	})
}

// NoteActivity records when a gateway last saw traffic to each app (by id).
// A time never moves an app's back, so gateway replicas may flush in any
// order. Missing apps are skipped. Neither the spec nor the version changes
// and nothing is audited: this runs every minute on every gateway.
func (a *Apps) NoteActivity(ctx context.Context, seen map[string]time.Time) error {
	for id, at := range seen {
		ms := dbtime.From(at)
		if _, err := a.s.q.NoteAppActivity(ctx, sqlcgen.NoteAppActivityParams{
			LastActiveAt: dbtime.NullTime{Time: ms, Valid: true}, LastActiveAt_2: dbtime.NullTime{Time: ms, Valid: true}, ID: id,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ListIdleStopCandidates returns running apps (not stopped, not deleted) that
// have a "Stop when idle" timeout. Their shares are not read.
func (a *Apps) ListIdleStopCandidates(ctx context.Context) ([]model.App, error) {
	rows, err := a.s.q.ListIdleStopCandidates(ctx)
	if err != nil {
		return nil, err
	}
	return appsOf(rows, nil)
}

// MarkDeleted records that the owner deleted the app and raises its version.
// The controller removes the app's objects, then calls Purge. Deleting an
// app that is gone or already marked is ErrNotFound. The app's previews are
// marked deleted in the same transaction, each with its own audit row and
// announcement.
func (a *Apps) MarkDeleted(ctx context.Context, actor model.Actor, id string) error {
	return appKind.markDeleted(ctx, a.s, actor, id, func(ctx context.Context, q *sqlcgen.Queries) ([]model.App, error) {
		rows, err := q.ListLiveAppChildren(ctx, sql.NullString{String: id, Valid: true})
		if err != nil {
			return nil, err
		}
		return appsOf(rows, nil)
	})
}

// Purge removes an app marked deleted, with its shares and tokens. A live or
// missing app is ErrNotFound; an app that still has previews (even ones
// marked deleted) is ErrConflict, so the parent goes after its previews.
func (a *Apps) Purge(ctx context.Context, actor model.Actor, id string) error {
	return appKind.purge(ctx, a.s, actor, id)
}

// SetStatus records what the controller observed, and which spec version it
// acted on; see rowKind.recordStatus.
func (a *Apps) SetStatus(ctx context.Context, id string, observedVersion int64, st model.AppStatus) error {
	return appKind.recordStatus(ctx, a.s, id, observedVersion, st)
}

func appsOf(rows []sqlcgen.App, shares []sqlcgen.AppShare) ([]model.App, error) {
	by := map[string][]sqlcgen.AppShare{}
	for _, s := range shares {
		by[s.AppID] = append(by[s.AppID], s)
	}
	out := make([]model.App, len(rows))
	for i, r := range rows {
		var err error
		if out[i], err = appOf(r, by[r.ID]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func appOf(r sqlcgen.App, shares []sqlcgen.AppShare) (model.App, error) {
	var spec appSpec
	if err := unmarshal(r.Spec, &spec); err != nil {
		return model.App{}, err
	}
	app := model.App{
		ID: r.ID, Namespace: r.Namespace, Name: r.Name, Owner: r.Owner, DisplayName: spec.DisplayName,
		Template: r.Template, File: spec.File, Sample: spec.Sample, Size: r.Size, Resources: resourcesOf(r.CpuMillis, r.MemoryBytes), Command: spec.Command,
		Image: spec.Image, Port: spec.Port, APIAccess: spec.APIAccess, AgentPrompt: spec.AgentPrompt, Env: spec.Env, Source: spec.Source, Environment: spec.Environment, ParentID: r.ParentID.String, PullNumber: int32(r.PullNumber), Paused: r.Paused, Sharing: model.Sharing{Access: r.Access},
		RestartRequestedAt: timeOf(r.RestartRequestedAt), TokenRevoked: r.TokenRevoked, Version: r.Version, CreatedAt: r.CreatedAt.Time(),
		DeletedAt: timeOf(r.DeletedAt), IdleStopMinutes: r.IdleStopMinutes, StoppedIdleAfter: spec.StoppedIdleAfter, StoppedByAdmin: spec.StoppedByAdmin,
		LastActiveAt: timeOf(r.LastActiveAt), RestoreHold: r.RestoreHold, ReleaseAt: timeOf(r.RestoreReleaseAt),
	}
	for _, s := range shares {
		if s.Kind == shareUser {
			app.Sharing.Users = append(app.Sharing.Users, s.Value)
		} else {
			app.Sharing.Groups = append(app.Sharing.Groups, s.Value)
		}
	}
	if err := unmarshal(r.Status, &app.Status); err != nil {
		return model.App{}, err
	}
	app.Status.ObservedVersion = r.ObservedVersion
	return app, nil
}

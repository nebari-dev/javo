package apps

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/gateway/sizing"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// The app actions run the same steps as the REST routes for start, stop,
// restart, delete and size: each route and its action call one function
// below, so a proposal applied after approval does exactly what the button does.

// setPaused stops (true) or starts (false) app. An app restored from a
// backup does not start until an admin releases it; someone other than the
// owner (a platform admin) stopping it is recorded, so the owner is told why.
func (s *Workspaces) setPaused(ctx context.Context, app *model.App, paused bool) (*model.App, error) {
	if !paused && app.Held(time.Now()) {
		return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgRestoreHeld))
	}
	if app.Paused == paused {
		return app, nil
	}
	byAdmin := paused && app.Owner != authn.User(ctx).ID
	return s.updateApp(ctx, app, func(a *model.App) { a.Paused, a.StoppedByAdmin = paused, byAdmin })
}

// requestRestart asks the controller to replace app's Pod; the gateway never
// deletes it itself.
func (s *Workspaces) requestRestart(ctx context.Context, app *model.App) (*model.App, error) {
	return s.updateApp(ctx, app, func(a *model.App) { a.RestartRequestedAt = time.Now() })
}

// deleteApp marks app deleted (the controller removes its Pod and Service,
// then the row) and revokes the tokens made for it.
func (s *Workspaces) deleteApp(ctx context.Context, app *model.App) error {
	if err := s.db.Apps().MarkDeleted(ctx, authn.Actor(ctx), app.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return problem.Internal("delete workspace", err)
	}
	if _, err := s.tokens.RevokeForApp(ctx, authn.Actor(ctx), app.ID, ""); err != nil {
		return problem.Internal("revoke the app's tokens", err)
	}
	return nil
}

// appAction is one catalog action on an app owned by (or managed by) the
// person in ctx.
type appAction struct {
	s    *Workspaces
	info actions.Info
	// plan describes the change on app, or fails as the API would.
	plan func(ctx context.Context, app *model.App, p actions.Proposal) (actions.Planned, error)
	// apply makes it.
	apply func(ctx context.Context, app *model.App, p actions.Proposal) error
}

func (a appAction) Info() actions.Info { return a.info }

func (a appAction) target(ctx context.Context, p actions.Proposal) (*model.App, error) {
	return a.s.authorized(ctx, &nameInput{Name: p.Target, Owner: p.Owner}, authz.ActManageApp)
}

func (a appAction) Plan(ctx context.Context, p actions.Proposal) (actions.Planned, error) {
	app, err := a.target(ctx, p)
	if err != nil {
		return actions.Planned{}, err
	}
	pl, err := a.plan(ctx, app, p)
	if err != nil {
		return actions.Planned{}, err
	}
	pl.Proposal = p
	pl.Owner = app.Owner
	return pl, nil
}

func (a appAction) Apply(ctx context.Context, p actions.Proposal) error {
	app, err := a.target(ctx, p)
	if err != nil {
		return err
	}
	if _, err := a.plan(ctx, app, p); err != nil {
		return err
	}
	return a.apply(ctx, app, p)
}

func phaseWord(app *model.App) string {
	if app.Paused {
		return "Stopped"
	}
	if app.Status.Phase == "" {
		return "Pending"
	}
	return app.Status.Phase
}

func notNeeded(app *model.App) error {
	return problem.New(http.StatusConflict, reasons.Msg(reasons.MsgActionNotNeeded, app.DisplayName))
}

// appActions are the app actions, in the order a model is told about them.
func (s *Workspaces) appActions() []actions.Action {
	pausing := func(id string, msg reasons.MessageCode, paused bool, to string) actions.Action {
		return appAction{s: s,
			info: actions.Info{ID: id, Summary: reasons.Msg(msg, "an app")},
			plan: func(_ context.Context, app *model.App, _ actions.Proposal) (actions.Planned, error) {
				if app.Paused == paused {
					return actions.Planned{}, notNeeded(app)
				}
				return actions.Planned{Summary: reasons.Msg(msg, app.DisplayName),
					Changes: []actions.Change{{Field: "state", From: phaseWord(app), To: to}}}, nil
			},
			apply: func(ctx context.Context, app *model.App, _ actions.Proposal) error {
				_, err := s.setPaused(ctx, app, paused)
				return err
			},
		}
	}
	return []actions.Action{
		pausing(actions.Start, reasons.MsgActionStart, false, "Running"),
		pausing(actions.Stop, reasons.MsgActionStop, true, "Stopped"),
		appAction{s: s,
			info: actions.Info{ID: actions.Restart, Summary: reasons.Msg(reasons.MsgActionRestart, "an app")},
			plan: func(_ context.Context, app *model.App, _ actions.Proposal) (actions.Planned, error) {
				if app.Paused {
					return actions.Planned{}, notNeeded(app)
				}
				return actions.Planned{Summary: reasons.Msg(reasons.MsgActionRestart, app.DisplayName),
					Changes: []actions.Change{{Field: "state", From: phaseWord(app), To: "Restarted"}}}, nil
			},
			apply: func(ctx context.Context, app *model.App, _ actions.Proposal) error {
				_, err := s.requestRestart(ctx, app)
				return err
			},
		},
		appAction{s: s,
			info: actions.Info{ID: actions.Resize, Summary: reasons.Msg(reasons.MsgActionResize, "an app", "another size"),
				Params: []actions.Param{{Name: "size", Doc: "the name of one of the sizes the owner may use"}}},
			plan: func(ctx context.Context, app *model.App, p actions.Proposal) (actions.Planned, error) {
				sz, err := s.Allowed(ctx, p.Params["size"], nil)
				if err != nil {
					return actions.Planned{}, err
				}
				if sizing.Same(app.Size, app.Resources, sz) {
					return actions.Planned{}, notNeeded(app)
				}
				name := sz.DisplayName
				if name == "" {
					name = sz.Name
				}
				return actions.Planned{Summary: reasons.Msg(reasons.MsgActionResize, app.DisplayName, name),
					Changes: []actions.Change{{Field: "size", From: app.Size, To: sz.Name}}}, nil
			},
			apply: func(ctx context.Context, app *model.App, p actions.Proposal) error {
				sz, err := s.Allowed(ctx, p.Params["size"], nil)
				if err != nil {
					return err
				}
				_, err = s.updateApp(ctx, app, func(a *model.App) { a.Size, a.Resources = sz.Stored() })
				return err
			},
		},
		appAction{s: s,
			info: actions.Info{ID: actions.Delete, Summary: reasons.Msg(reasons.MsgActionDelete, "an app")},
			plan: func(_ context.Context, app *model.App, _ actions.Proposal) (actions.Planned, error) {
				return actions.Planned{Summary: reasons.Msg(reasons.MsgActionDelete, app.DisplayName), Destructive: true,
					Changes: []actions.Change{{Field: "app", From: app.DisplayName, To: "deleted"}}}, nil
			},
			apply: func(ctx context.Context, app *model.App, _ actions.Proposal) error { return s.deleteApp(ctx, app) },
		},
	}
}

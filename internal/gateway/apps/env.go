package apps

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/envvars"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

type appEnvInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
	Body  envvars.EnvBody
}

// envOf is what the app view lists of its variables.
func (s *Workspaces) envOf(ctx context.Context, app *model.App) []envvars.EnvVar {
	bound, err := s.db.Secrets().Bindings(ctx, app.ID)
	if err != nil {
		slog.Error("list app secrets", "app", app.ID, "err", err)
	}
	return envvars.View(app.Env, bound)
}

func (s *Workspaces) registerEnv(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "setWorkspaceEnv", Method: http.MethodPut, Path: "/api/workspaces/{name}/env",
		Summary: "Set an app's environment variables: plain values and values from your secrets (a running app restarts)",
		Description: "Only the app's owner, with their own secrets. Anyone who can use the app could make it " +
			"show its environment if the app prints it; sharing an app does not share the secrets themselves. " +
			"Previews of pull requests never get secrets.",
		Tags:   []string{"workspaces"},
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *appEnvInput) (*workspaceOutput, error) {
		if err := authn.NoAppTokens(ctx); err != nil {
			return nil, err
		}
		app, err := s.authorizedEdit(ctx, &nameInput{Name: in.Name, Owner: in.Owner}, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		u := authn.User(ctx)
		roles := s.rolesOf(u)
		if err := envvars.MayChange(ctx, u, roles, authz.Object{App: app}, app.Owner); err != nil {
			return nil, err
		}
		plain, bindings, err := envvars.Check(ctx, s.db, u, roles, authz.Object{App: app}, in.Body.Env)
		if err != nil {
			return nil, err
		}
		saved, err := s.db.Apps().SetEnv(ctx, authn.Actor(ctx), app.ID, app.Version, plain, bindings)
		if err != nil {
			return nil, envvars.SaveError(err, errNotFound, func() error { return problem.New(http.StatusConflict, reasons.Msg(reasons.MsgAppChanged)) })
		}
		return s.output(ctx, &saved)
	})
}

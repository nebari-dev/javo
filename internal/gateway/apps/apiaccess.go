package apps

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/envvars"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// apiAccessWords is the plain-words catalog message for each API access level.
var apiAccessWords = map[string]reasons.MessageCode{
	model.APIAccessNone:      reasons.MsgAPIAccessNone,
	model.APIAccessRead:      reasons.MsgAPIAccessRead,
	model.APIAccessReadWrite: reasons.MsgAPIAccessReadWrite,
}

// checkAPIAccess refuses API access above what template t allows. Empty
// asks for the template's default, which is never above it.
func checkAPIAccess(t *model.Template, access string) error {
	max := model.MaxAPIAccess(t)
	if model.APIAccessRank(access) > model.APIAccessRank(max) {
		return problem.FieldError("apiAccess", reasons.MsgAPIAccessTooHigh, reasons.Msg(apiAccessWords[max]))
	}
	return nil
}

type apiAccessInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
	Body  struct {
		APIAccess string `json:"apiAccess" enum:"None,Read,ReadWrite" doc:"None, Read (GET only) or ReadWrite. Anyone the app is shared with can make it act as you."`
	}
}

func (s *Workspaces) registerAPIAccess(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "setWorkspaceAPIAccess", Method: http.MethodPut, Path: "/api/workspaces/{name}/api-access",
		Summary: "Change whether an app can call the API as you (a running app restarts)",
		Description: "The app's old token stops working at once; the app gets a token of the new scope as " +
			"JAVO_TOKEN when it restarts, or none with None.",
		Tags:   []string{"workspaces"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *apiAccessInput) (*workspaceOutput, error) {
		app, err := s.authorizedEdit(ctx, &nameInput{Name: in.Name, Owner: in.Owner}, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		t, err := s.db.Templates().Get(ctx, app.Template)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil, problem.FieldError("template", reasons.MsgTemplateUnavailable)
		case err != nil:
			return nil, problem.Internal("read template", err)
		}
		if err := checkAPIAccess(&t, in.Body.APIAccess); err != nil {
			return nil, err
		}
		// The token acts as the owner, so only they give the app more of it.
		raised := *app
		raised.APIAccess = in.Body.APIAccess
		if model.APIAccessRank(model.APIAccessOf(&raised, &t)) > model.APIAccessRank(model.APIAccessOf(app, &t)) {
			u := authn.User(ctx)
			if err := envvars.OwnerOnly(ctx, u, s.rolesOf(u), authz.Object{App: app}); err != nil {
				return nil, err
			}
		}
		if app.APIAccess != in.Body.APIAccess {
			if app, err = s.updateApp(ctx, app, func(a *model.App) { a.APIAccess = in.Body.APIAccess }); err != nil {
				return nil, err
			}
		}
		// The old token ends here, before the controller makes the new one.
		keep := token.ScopeFor(model.APIAccessOf(app, &t))
		if _, err := s.tokens.RevokeForApp(ctx, authn.Actor(ctx), app.ID, keep); err != nil {
			return nil, problem.Internal("revoke the app's old token", err)
		}
		return s.output(ctx, app)
	})
}

// AppTokenWanted reports whether a token of scope made for app a is still
// one the app should hold: the app exists, is not being deleted, and its API
// access is that scope. The token names the app by the name its Pod and
// Service carry.
func AppTokenWanted(db *store.Store) token.AppCheck {
	return func(ctx context.Context, a token.App, scope token.Scope) (bool, error) {
		app, err := db.Apps().GetByObjectName(ctx, a.Name)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return false, nil
		case err != nil:
			return false, err
		case app.Namespace != a.Namespace || !app.DeletedAt.IsZero():
			return false, nil
		}
		t, err := db.Templates().Get(ctx, app.Template)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return false, nil
		case err != nil:
			return false, err
		}
		return token.ScopeFor(model.APIAccessOf(&app, &t)) == scope, nil
	}
}

// AgentTokens is authn.Authenticator.Agents: an app token's app is a coding
// agent with deploy rights while its template has agentDeploy and the app is
// private, read on every request so turning either off takes effect at once.
func AgentTokens(db *store.Store) func(context.Context, token.App) (bool, error) {
	return func(ctx context.Context, a token.App) (bool, error) {
		app, err := db.Apps().GetByObjectName(ctx, a.Name)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return false, nil
		case err != nil:
			return false, err
		case app.Namespace != a.Namespace || !app.DeletedAt.IsZero() || app.Sharing.Access != model.AccessPrivate:
			return false, nil
		}
		t, err := db.Templates().Get(ctx, app.Template)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return false, nil
		case err != nil:
			return false, err
		}
		return t.Spec.AgentDeploy && model.MaxSharing(&t) == model.AccessPrivate, nil
	}
}

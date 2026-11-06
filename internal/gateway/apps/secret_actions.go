package apps

import (
	"context"
	"net/http"
	"slices"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/envvars"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// The secret actions let a coding agent ask for one of its owner's secrets
// for an app, by name: the owner approves in the browser and the change is
// made through setEnv, the same as the app's Variables form. Nobody but the
// app's pod ever gets the value.

// secretAsker checks who proposes or applies a secret change on app: its
// owner, in the browser, with a personal token or as their coding agent,
// never with another app's or job's own token (anyone an app is shared with
// can make it ask), and never an agent for its own app (it would read the
// value).
func (s *Workspaces) secretAsker(ctx context.Context, app *model.App) error {
	if tok, ok := authn.APIToken(ctx); ok && authn.ViaAgentToken(ctx) && tok.App != nil &&
		tok.App.Namespace == app.Namespace && tok.App.Name == app.ObjectName() {
		return problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgAgentOwnAppSecret))
	}
	u := authn.User(ctx)
	return envvars.MayChange(ctx, u, s.rolesOf(u), authz.Object{App: app}, app.Owner)
}

// attachEnv is the variable an attach-secret proposal names: env, or the
// secret's own name.
func attachEnv(p actions.Proposal) string {
	if e := p.Params["env"]; e != "" {
		return e
	}
	return p.Params["secret"]
}

func (s *Workspaces) planAttachSecret(ctx context.Context, app *model.App, p actions.Proposal) (actions.Planned, error) {
	if err := s.secretAsker(ctx, app); err != nil {
		return actions.Planned{}, err
	}
	env := attachEnv(p)
	switch model.EnvNameProblem(env) {
	case model.EnvNameInvalid:
		return actions.Planned{}, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgEnvNameInvalid))
	case model.EnvNameReserved:
		return actions.Planned{}, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgEnvNameReserved, env))
	}
	u := authn.User(ctx)
	sec, unknown, err := envvars.UsableSecret(ctx, s.db, u, s.rolesOf(u), authz.Object{App: app}, p.Params["secret"], "params.secret")
	if err != nil {
		return actions.Planned{}, err
	}
	if unknown != nil {
		return actions.Planned{}, problem.New(http.StatusUnprocessableEntity, unknown.Msg)
	}
	cur, err := s.storedEnv(ctx, app)
	if err != nil {
		return actions.Planned{}, err
	}
	from := ""
	for _, e := range cur {
		switch {
		case e.Name != env:
		case e.Secret == sec.Name:
			return actions.Planned{}, notNeeded(app)
		case e.Secret != "":
			from = "secret " + e.Secret
		default:
			from = "a plain value"
		}
	}
	return actions.Planned{Summary: reasons.Msg(reasons.MsgActionAttachSecret, app.DisplayName, sec.Name, env),
		Changes: []actions.Change{{Field: env, From: from, To: "secret " + sec.Name}}}, nil
}

func (s *Workspaces) applyAttachSecret(ctx context.Context, app *model.App, p actions.Proposal) error {
	env := attachEnv(p)
	cur, err := s.storedEnv(ctx, app)
	if err != nil {
		return err
	}
	cur = slices.DeleteFunc(cur, func(e envvars.EnvVar) bool { return e.Name == env })
	_, err = s.setEnv(ctx, app, append(cur, envvars.EnvVar{Name: env, Secret: p.Params["secret"]}))
	return err
}

func (s *Workspaces) planDetachSecret(ctx context.Context, app *model.App, p actions.Proposal) (actions.Planned, error) {
	if err := s.secretAsker(ctx, app); err != nil {
		return actions.Planned{}, err
	}
	env := p.Params["env"]
	cur, err := s.storedEnv(ctx, app)
	if err != nil {
		return actions.Planned{}, err
	}
	i := slices.IndexFunc(cur, func(e envvars.EnvVar) bool { return e.Name == env && e.Secret != "" })
	if i < 0 {
		return actions.Planned{}, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgEnvNoSecret, app.DisplayName, env))
	}
	return actions.Planned{Summary: reasons.Msg(reasons.MsgActionDetachSecret, cur[i].Secret, env, app.DisplayName),
		Changes: []actions.Change{{Field: env, From: "secret " + cur[i].Secret, To: "not set"}}}, nil
}

func (s *Workspaces) applyDetachSecret(ctx context.Context, app *model.App, p actions.Proposal) error {
	cur, err := s.storedEnv(ctx, app)
	if err != nil {
		return err
	}
	_, err = s.setEnv(ctx, app, slices.DeleteFunc(cur, func(e envvars.EnvVar) bool { return e.Name == p.Params["env"] }))
	return err
}

// secretActions are attach-secret and detach-secret.
func (s *Workspaces) secretActions() []actions.Action {
	return []actions.Action{
		appAction{s: s,
			info: actions.Info{ID: actions.AttachSecret, Summary: reasons.Msg(reasons.MsgActionAttachSecretInfo), Params: []actions.Param{
				{Name: "secret", Doc: "the name of one of the owner's secrets (GET /api/secret-names lists them)"},
				{Name: "env", Doc: "the environment variable the app reads it as; default the secret's name"},
			}},
			plan: s.planAttachSecret, apply: s.applyAttachSecret,
		},
		appAction{s: s,
			info: actions.Info{ID: actions.DetachSecret, Summary: reasons.Msg(reasons.MsgActionDetachSecretInfo), Params: []actions.Param{
				{Name: "env", Doc: "the environment variable the app gets the secret as"},
			}},
			plan: s.planDetachSecret, apply: s.applyDetachSecret,
		},
	}
}

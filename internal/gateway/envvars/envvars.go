// Package envvars checks and shows the environment variables of apps and
// jobs: plain values and values bound from their owner's secrets.
package envvars

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/nebari-dev/javo/internal/ai/redact"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// EnvVar is one environment variable of an app or of a job's runs: a plain
// value, stored and shown as typed, or the value of one of the owner's
// secrets, which is never shown.
type EnvVar struct {
	Name   string `json:"name" maxLength:"100" doc:"Letters, digits and underscores, not starting with a digit; names the platform sets are refused"`
	Value  string `json:"value,omitempty" doc:"A plain value, shown again as typed: for settings that are not secret"`
	Secret string `json:"secret,omitempty" maxLength:"100" doc:"The name of one of your secrets to take the value from instead; its value is never shown"`
	Hidden bool   `json:"hidden,omitempty" readOnly:"true" doc:"The value is left out: it is named like a credential and only the owner sees it"`
}

// EnvBody is the list a PUT of variables sends.
type EnvBody struct {
	Env []EnvVar `json:"env" maxItems:"100" doc:"Every variable; the list replaces the one before"`
}

// View is what an app or job of owner shows the caller of its variables, by
// name: plain values as stored, secrets by name only. A plain value named
// like a credential (a password typed in instead of picked as a secret) is
// shown to the owner alone, in the browser or with a personal token; anyone
// else (a platform admin, an app's or job's own token) gets it Hidden.
func View(ctx context.Context, owner string, plain []model.EnvVar, bindings []model.SecretBinding) []EnvVar {
	ownerViews := authn.User(ctx).ID == owner && authn.NoAppTokens(ctx) == nil
	out := make([]EnvVar, 0, len(plain)+len(bindings))
	for _, e := range plain {
		if !ownerViews && redact.SensitiveName(e.Name) {
			out = append(out, EnvVar{Name: e.Name, Hidden: true})
			continue
		}
		out = append(out, EnvVar{Name: e.Name, Value: e.Value})
	}
	for _, b := range bindings {
		out = append(out, EnvVar{Name: b.Env, Secret: b.Secret})
	}
	slices.SortStableFunc(out, func(a, b EnvVar) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// Check checks the variables the caller asks target (an app or a job, as
// an authz object) to get: each a valid name the platform does not set,
// given once, at most MaxEnvVars; a plain value of at most MaxEnvValue; and
// each secret one of the caller's that authz lets them give target (their
// own app or job, not a preview). A request with a secret made with an app's
// own API token is refused. It returns the plain values and the bindings.
func Check(ctx context.Context, db *store.Store, u authz.Principal, roles []authz.Role, target authz.Object, in []EnvVar) ([]model.EnvVar, []model.SecretBinding, error) {
	if len(in) == 0 {
		return nil, nil, nil
	}
	if slices.ContainsFunc(in, func(e EnvVar) bool { return e.Secret != "" }) {
		if err := authn.NoAppTokens(ctx); err != nil {
			return nil, nil, err
		}
	}
	if len(in) > model.MaxEnvVars {
		return nil, nil, problem.FieldError("env", reasons.MsgEnvTooMany)
	}
	seen := map[string]bool{}
	var errs []problem.Field
	var plain []model.EnvVar
	var bindings []model.SecretBinding
	for i, e := range in {
		at := func(f string) string { return fmt.Sprintf("env[%d].%s", i, f) }
		switch model.EnvNameProblem(e.Name) {
		case model.EnvNameInvalid:
			errs = append(errs, problem.Field{Name: at("name"), Msg: reasons.Msg(reasons.MsgEnvNameInvalid)})
		case model.EnvNameReserved:
			errs = append(errs, problem.Field{Name: at("name"), Msg: reasons.Msg(reasons.MsgEnvNameReserved, e.Name)})
		default:
			if seen[e.Name] {
				errs = append(errs, problem.Field{Name: at("name"), Msg: reasons.Msg(reasons.MsgEnvNameRepeated, e.Name)})
			}
			seen[e.Name] = true
		}
		switch {
		case e.Secret != "" && e.Value != "":
			errs = append(errs, problem.Field{Name: at("value"), Msg: reasons.Msg(reasons.MsgEnvValueOrSecret)})
		case e.Secret == "":
			if !model.ValidEnvValue(e.Value) {
				errs = append(errs, problem.Field{Name: at("value"), Msg: reasons.Msg(reasons.MsgEnvValueInvalid)})
				continue
			}
			plain = append(plain, model.EnvVar{Name: e.Name, Value: e.Value})
		default:
			sec, unknown, err := UsableSecret(ctx, db, u, roles, target, e.Secret, at("secret"))
			if err != nil {
				return nil, nil, err
			}
			if unknown != nil {
				errs = append(errs, *unknown)
				continue
			}
			bindings = append(bindings, model.SecretBinding{Env: e.Name, Secret: sec.Name})
		}
	}
	if len(errs) > 0 {
		return nil, nil, problem.Fields(errs...)
	}
	return plain, bindings, nil
}

// UsableSecret is the caller's secret called name if authz lets them give it
// to target (their own app or job, not a preview). A name they have no
// secret of is unknown, a field error on field; a refusal is the error.
func UsableSecret(ctx context.Context, db *store.Store, u authz.Principal, roles []authz.Role, target authz.Object, name, field string) (model.Secret, *problem.Field, error) {
	sec, err := db.Secrets().Get(ctx, u.ID, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return model.Secret{}, &problem.Field{Name: field, Msg: reasons.Msg(reasons.MsgSecretUnknown, name)}, nil
	case err != nil:
		return model.Secret{}, nil, problem.Internal("read secret", err)
	}
	target.Secret = &sec
	if d := authz.Decide(u, roles, authz.ActUseSecret, target); !d.Allowed {
		return model.Secret{}, nil, problem.New(http.StatusForbidden, reasons.Msg(d.Code))
	}
	return sec, nil, nil
}

// MayChange lets only the owner change the variables of target, and never
// with an app's own API token: even plain values, so a platform admin who
// manages the app or job cannot change what it runs with, and an app cannot
// change its own environment.
func MayChange(ctx context.Context, u authz.Principal, roles []authz.Role, target authz.Object, owner string) error {
	if err := authn.NoAppTokensExceptAgents(ctx); err != nil {
		return err
	}
	target.Secret = &model.Secret{Owner: owner}
	if d := authz.Decide(u, roles, authz.ActUseSecret, target); !d.Allowed {
		return problem.New(http.StatusForbidden, reasons.Msg(d.Code))
	}
	return nil
}

// MayChangeCode guards a change of what target runs: a job's engine,
// command, file or template, an app's command, image or port. Only the owner
// may make it, never a platform admin and never with an app's or job's own
// token, whatever target holds: every app and job runs in its owner's home,
// where the files their secret-holding jobs and apps run sit, so a command
// someone else picks could rewrite those files (and read the whole home).
func MayChangeCode(ctx context.Context, u authz.Principal, roles []authz.Role, target authz.Object) error {
	if authn.ViaAgentToken(ctx) {
		// Its own target, holding no credentials (AgentTarget found it).
		target.Secret = &model.Secret{Owner: u.ID}
		if !authz.Decide(u, roles, authz.ActUseSecret, target).Allowed {
			return problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgRewriteOwnerOnly))
		}
		return nil
	}
	if err := OwnerOnly(ctx, u, roles, target); err != nil {
		return problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgRewriteOwnerOnly))
	}
	return nil
}

// MayRewrite guards a change that decides which code sees target's
// credentials without running in its owner's home: turning on its pull
// request previews, a run with other parameter values. While target holds
// any of its owner's credentials (a secret as a variable or as its
// repository token, an API token from a job's template, or for an app with
// target.Template set, an API or Nebi token), only the owner may make it
// (OwnerOnly). Without them, whoever manages target may.
func MayRewrite(ctx context.Context, db *store.Store, u authz.Principal, roles []authz.Role, target authz.Object) error {
	held, err := holdsCredentials(ctx, db, target)
	if err != nil {
		return problem.Internal("read secret bindings", err)
	}
	if !held {
		return nil
	}
	return OwnerOnly(ctx, u, roles, target)
}

// OwnerOnly lets only target's owner through, and never with an app's own
// API token, as for choosing its secrets (MayChange).
func OwnerOnly(ctx context.Context, u authz.Principal, roles []authz.Role, target authz.Object) error {
	owner := ""
	switch {
	case target.Job != nil:
		owner = target.Job.Owner
	case target.App != nil:
		owner = target.App.Owner
	}
	target.Secret = &model.Secret{Owner: owner}
	if authn.NoAppTokens(ctx) != nil || !authz.Decide(u, roles, authz.ActUseSecret, target).Allowed {
		return problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgSecretsHeldOwnerOnly))
	}
	return nil
}

// errAgentNeedsPerson is the refusal of a coding agent's token.
func errAgentNeedsPerson() error {
	return problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgAgentNeedsPerson))
}

// AgentTarget refuses a change a coding agent's token (authn.ViaAgentToken)
// asks for on target unless target is the token owner's own and holds no
// credentials (holdsCredentials: no secret, repository token, API token or
// Nebi token): such an app or job is the agent's to manage, any other is the
// person's. Reads and every other caller pass. Every app and job route that
// changes something finds its target through it.
func AgentTarget(ctx context.Context, db *store.Store, target authz.Object) error {
	if !authn.ViaAgentToken(ctx) || !authn.Writes(ctx) {
		return nil
	}
	owner := ""
	switch {
	case target.Job != nil:
		owner = target.Job.Owner
	case target.App != nil:
		owner = target.App.Owner
		if target.Template == nil {
			t, err := db.Templates().Get(ctx, target.App.Template)
			if err != nil {
				// No template to tell what the app holds: the person's.
				return errAgentNeedsPerson()
			}
			target.Template = &t
		}
	}
	if owner == "" || owner != authn.User(ctx).ID {
		return errAgentNeedsPerson()
	}
	held, err := holdsCredentials(ctx, db, target)
	if err != nil {
		return problem.Internal("read secret bindings", err)
	}
	if held {
		return errAgentNeedsPerson()
	}
	return nil
}

// AgentCreate refuses a coding agent's token an app or job that would hold
// credentials: an app whose API access (its own or tpl's default) is not
// None or that gets the owner's Nebi token, a job whose template gives its
// runs an API token, or either with secrets, a repository token or webhook,
// or pull request previews (extras). Every other caller passes.
func AgentCreate(ctx context.Context, app *model.App, job *model.Job, tpl *model.Template, extras bool) error {
	if !authn.ViaAgentToken(ctx) {
		return nil
	}
	switch {
	case extras, tpl == nil:
		return errAgentNeedsPerson()
	case app != nil && (model.APIAccessOf(app, tpl) != model.APIAccessNone || model.NebiTokenFor(app, tpl)):
		return errAgentNeedsPerson()
	case job != nil && model.DefaultAPIAccess(tpl) != model.APIAccessNone:
		return errAgentNeedsPerson()
	}
	return nil
}

// holdsCredentials is whether target's app or job is bound to any of its
// owner's secrets, or runs with a token that acts as its owner: an app with
// API access or its owner's Nebi token, a job whose template gives an API
// token.
func holdsCredentials(ctx context.Context, db *store.Store, target authz.Object) (bool, error) {
	switch {
	case target.Job != nil:
		// Its template may give its runs an API token, as for apps.
		switch tpl, err := db.Templates().Get(ctx, target.Job.Spec.Template); {
		case err == nil && model.DefaultAPIAccess(&tpl) != model.APIAccessNone:
			return true, nil
		case err != nil && !errors.Is(err, store.ErrNotFound):
			return false, err
		}
		b, err := db.Secrets().JobBindings(ctx, target.Job.ID)
		return len(b) > 0, err
	case target.App != nil:
		if target.Template != nil && (model.APIAccessOf(target.App, target.Template) != model.APIAccessNone || model.NebiTokenFor(target.App, target.Template)) {
			return true, nil
		}
		b, err := db.Secrets().Bindings(ctx, target.App.ID)
		if err != nil || len(b) > 0 {
			return len(b) > 0, err
		}
		tok, err := db.Secrets().GitTokenSecret(ctx, target.App.ID)
		return tok != "", err
	}
	return false, nil
}

// SaveError maps a refused save of variables.
func SaveError(err error, notFound, changed func() error) error {
	var missing *store.SecretMissingError
	switch {
	case errors.As(err, &missing):
		// Deleted since Check read it.
		return problem.FieldError("env", reasons.MsgSecretUnknown, missing.Name)
	case errors.Is(err, store.ErrNotFound):
		return notFound()
	case errors.Is(err, store.ErrConflict):
		return changed()
	}
	return problem.Internal("save environment variables", err)
}

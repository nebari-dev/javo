package store

import (
	"context"
	"slices"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// Environment variables: an app's or a job's plain values are
// part of its spec; the ones from secrets are bindings beside it. SetEnv
// changes both in one transaction and moves the version, so a running app
// restarts and a job's next run gets the change.

// envAudit is the audit detail of a change of variables: their names, and
// for each from a secret the secret's name. Plain values stay out of it.
func envAudit(plain []model.EnvVar, bindings []model.SecretBinding) map[string]any {
	names := make([]string, 0, len(plain))
	for _, e := range plain {
		names = append(names, e.Name)
	}
	detail := bindingNames(bindings)
	detail["plain"] = names
	return detail
}

// SetEnv replaces the app's variables if it is still at version (else
// ErrConflict): plain values and the owner's secrets by variable name. A
// binding that names no secret of the owner (*SecretMissingError) changes
// nothing. It is audited with the names only.
func (a *Apps) SetEnv(ctx context.Context, actor model.Actor, id string, version int64, plain []model.EnvVar, bindings []model.SecretBinding) (model.App, error) {
	return appKind.updateWith(ctx, a.s, actor, "app.set_env", id, version, func(app *model.App) error {
		app.Env = slices.Clone(plain)
		return nil
	}, func(q *sqlcgen.Queries, app *model.App) (any, error) {
		return envAudit(plain, bindings), writeAppBindings(ctx, q, app.ID, app.Owner, bindings)
	})
}

// SetEnv is Apps.SetEnv for a job.
func (j *Jobs) SetEnv(ctx context.Context, actor model.Actor, id string, version int64, plain []model.EnvVar, bindings []model.SecretBinding) (model.Job, error) {
	return jobKind.updateWith(ctx, j.s, actor, "job.set_env", id, version, func(job *model.Job) error {
		job.Spec.Env = slices.Clone(plain)
		return nil
	}, func(q *sqlcgen.Queries, job *model.Job) (any, error) {
		return envAudit(plain, bindings), writeJobBindings(ctx, q, job.ID, job.Owner, bindings)
	})
}

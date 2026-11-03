package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// A private repository's token comes from one of the owner's secrets. The
// binding lives in app_secrets under model.GitTokenEnv, so the secret
// counts as in use, its new value is announced to the app, and the
// controller reads it with the app's other secrets; it is never one of the
// app's variables.

// SetGitToken makes the owner's secret called name the app's repository
// token; "" removes it. A name the owner has no secret of is a
// *SecretMissingError. A token pasted on the app (the older way) is dropped either
// way. The app is announced, so its clone gets the change. It is audited with
// the secret's name.
func (x *Secrets) SetGitToken(ctx context.Context, actor model.Actor, app model.App, name string) error {
	return x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		if err := bindGitToken(ctx, q, app, name); err != nil {
			return effect{}, err
		}
		eff := appKind.effect("app.git_token_set", app)
		eff.audit.detail = map[string]string{"secret": name}
		return eff, nil
	})
}

// bindGitToken replaces the app's token binding with the owner's secret
// called name (none for "") and drops a pasted token.
func bindGitToken(ctx context.Context, q *sqlcgen.Queries, app model.App, name string) error {
	if _, err := q.DeleteAppGitTokenSecret(ctx, app.ID); err != nil {
		return err
	}
	if _, err := q.RemoveGitToken(ctx, app.ID); err != nil {
		return err
	}
	if name == "" {
		return nil
	}
	return resolveBindings(ctx, q, app.Owner, []model.SecretBinding{{Env: model.GitTokenEnv, Secret: name}}, func(env, secretID string) error {
		return q.InsertAppSecret(ctx, sqlcgen.InsertAppSecretParams{AppID: app.ID, Env: env, SecretID: secretID})
	})
}

// GitTokenSecret is the name of the secret the app's repository token comes
// from, or "" when none.
func (x *Secrets) GitTokenSecret(ctx context.Context, appID string) (string, error) {
	name, err := x.s.q.GetAppGitTokenSecret(ctx, appID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// maxAdoptNames is how many names AdoptGitToken tries.
const maxAdoptNames = 20

// AdoptGitToken turns the app's pasted repository token (from before
// tokens came from secrets) into a secret of the owner, GIT_TOKEN_<APP> or the first of
// GIT_TOKEN_<APP>_2, _3, ... that is free, and binds it as the app's token,
// in one transaction. seal gets the new row's id and name and returns the
// pasted value sealed for them; the caller (the controller) read it from the
// app's token Secret. The pasted copy is dropped. It is audited, without the
// value.
func (x *Secrets) AdoptGitToken(ctx context.Context, actor model.Actor, app model.App, seal func(id, name string) ([]byte, error)) (model.Secret, error) {
	base := "GIT_TOKEN_" + strings.ToUpper(strings.ReplaceAll(app.Name, "-", "_"))
	var out model.Secret
	err := x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		if n, err := q.CountSecretsForOwner(ctx, app.Owner); err != nil {
			return effect{}, err
		} else if n >= model.MaxSecretsPerOwner {
			return effect{}, ErrTooManySecrets
		}
		name, err := freeSecretName(ctx, q, app.Owner, base)
		if err != nil {
			return effect{}, err
		}
		id := newID()
		sealed, err := seal(id, name)
		if err != nil {
			return effect{}, err
		}
		note := "Repository token of " + app.DisplayName + ", moved here from the app"
		if _, err := q.InsertSecret(ctx, sqlcgen.InsertSecretParams{
			ID: id, Owner: app.Owner, Name: name, Note: note, Ciphertext: encode(sealed), CreatedAt: dbtime.From(now()),
		}); err != nil {
			return effect{}, err
		}
		row, err := q.GetSecret(ctx, id)
		if err != nil {
			return effect{}, err
		}
		out = secretOf(row)
		if err := recordAudit(ctx, q, actor, *secretAudit("secret.create", out, nil)); err != nil {
			return effect{}, err
		}
		if err := bindGitToken(ctx, q, app, name); err != nil {
			return effect{}, err
		}
		eff := appKind.effect("app.git_token_adopt", app)
		eff.audit.detail = map[string]string{"secret": name}
		return eff, nil
	})
	return out, err
}

// freeSecretName is base, or base_2, base_3, ... if owner has a secret of
// that name.
func freeSecretName(ctx context.Context, q *sqlcgen.Queries, owner, base string) (string, error) {
	for n := 1; n <= maxAdoptNames; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s_%d", base, n)
		}
		_, err := readSecret(ctx, q, owner, name)
		if errors.Is(err, ErrNotFound) {
			return name, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", ErrExists
}

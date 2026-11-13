package workspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/workload"
)

// SecretName is the name of an app's Secret of the given use (api, secrets,
// git, nebi): "<objectName>.app-<use>". Object names are DNS labels and never
// hold a ".", so the part after it says whose Secret it is: no name a person
// picks for an app or a job makes another app's or a job's Secret (jobs use
// ".job-" and ".run-").
func SecretName(objectName, use string) string { return objectName + ".app-" + use }

// secretUses are the uses of an app's Secrets.
var secretUses = []string{"api", "secrets", "git", "nebi"}

// RefOf names the app's workload.
func RefOf(app *model.App) workload.Ref {
	return workload.Ref{Namespace: app.Namespace, Name: app.ObjectName(), ID: app.ID, Owner: app.Owner}
}

// revokeOwn revokes the API token with the given id when its record is the
// app's: what Place.Tidy does with a token it finds under an old name.
func (r *Reconciler) revokeOwn(app *model.App) func(ctx context.Context, id string) error {
	return func(ctx context.Context, id string) error {
		if r.Tokens == nil {
			return nil
		}
		switch tok, err := r.Tokens.Get(ctx, id); {
		case errors.Is(err, token.ErrNotFound):
		case err != nil:
			return err
		case tokenOfApp(tok, app):
			if _, err := r.Tokens.Revoke(ctx, model.Actor{System: model.SystemController}, app.Owner, id); err != nil && !errors.Is(err, token.ErrNotFound) {
				return fmt.Errorf("revoke the app's old API token: %w", err)
			}
		}
		return nil
	}
}

// tokenOfApp is true when tok was made for app: same owner, same namespace
// and object name, and not a job's.
func tokenOfApp(tok token.Token, app *model.App) bool {
	return tok.Owner == app.Owner && tok.Job == "" && tok.App != nil &&
		tok.App.Namespace == app.Namespace && tok.App.Name == app.ObjectName()
}

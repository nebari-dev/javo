package jobs

import (
	"context"
	"errors"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// JobTokenWanted is the token.JobCheck over db: a job's token is wanted while
// the job exists, is not deleted, and its template still gives its runs a
// token of that scope (model.DefaultAPIAccess).
func JobTokenWanted(db *store.Store) token.JobCheck {
	return func(ctx context.Context, id string, scope token.Scope) (string, bool, error) {
		job, err := db.Jobs().Get(ctx, id)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return "", false, nil
		case err != nil:
			return "", false, err
		case !job.DeletedAt.IsZero():
			return job.Spec.Template, false, nil
		}
		tpl, err := db.Templates().Get(ctx, job.Spec.Template)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return job.Spec.Template, false, nil
		case err != nil:
			return "", false, err
		}
		return job.Spec.Template, token.ScopeFor(model.DefaultAPIAccess(&tpl)) == scope, nil
	}
}

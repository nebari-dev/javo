package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// Environments is the app_environments and job_environments tables: the
// files of the environment an app or a job runs in (model.Environment). They
// are written with the app or job and go with it.
type Environments struct{ s *Store }

// Environments returns the environment files tables.
func (s *Store) Environments() *Environments { return &Environments{s} }

// ForApp returns the files of the app's environment; ErrNotFound when it has none.
func (e *Environments) ForApp(ctx context.Context, appID string) (model.EnvironmentFiles, error) {
	row, err := e.s.q.GetAppEnvironment(ctx, appID)
	return envFiles(row.Manifest, row.Lock, err)
}

// ForJob returns the files of the job's environment; ErrNotFound when it has none.
func (e *Environments) ForJob(ctx context.Context, jobID string) (model.EnvironmentFiles, error) {
	row, err := e.s.q.GetJobEnvironment(ctx, jobID)
	return envFiles(row.Manifest, row.Lock, err)
}

func envFiles(manifest, lock string, err error) (model.EnvironmentFiles, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return model.EnvironmentFiles{}, ErrNotFound
	}
	return model.EnvironmentFiles{Manifest: manifest, Lock: lock}, err
}

// checkEnvironment refuses files that are not the ones env names, and an
// env with no files: a row and its files always agree.
func checkEnvironment(env *model.Environment, files *model.EnvironmentFiles) error {
	switch {
	case env == nil && files == nil:
		return nil
	case env == nil || files == nil:
		return errors.New("store: an environment and its files are stored together")
	case files.Digest() != env.Digest:
		return fmt.Errorf("store: the environment files do not match digest %s", env.Digest)
	}
	return nil
}

func writeAppEnvironment(ctx context.Context, q *sqlcgen.Queries, appID string, files *model.EnvironmentFiles) error {
	if files == nil {
		return nil
	}
	return q.InsertAppEnvironment(ctx, sqlcgen.InsertAppEnvironmentParams{AppID: appID, Manifest: files.Manifest, Lock: files.Lock})
}

func writeJobEnvironment(ctx context.Context, q *sqlcgen.Queries, jobID string, files *model.EnvironmentFiles) error {
	if files == nil {
		return nil
	}
	return q.InsertJobEnvironment(ctx, sqlcgen.InsertJobEnvironmentParams{JobID: jobID, Manifest: files.Manifest, Lock: files.Lock})
}

package apitest

import (
	"context"

	"github.com/nebari-dev/javo/internal/model"
)

type JobSecretsJSON struct {
	Name string    `json:"name"`
	Env  []EnvJSON `json:"env"`
}

type SecretUsesJSON struct {
	Name string
	Apps []struct{ Name, DisplayName string }
	Jobs []struct{ Name, DisplayName string }
}

func (f *Fixture) JobBindings(owner, name string) []model.SecretBinding {
	f.T.Helper()
	job, err := f.Db.Jobs().GetByOwnerName(context.Background(), owner, name)
	if err != nil {
		f.T.Fatal(err)
	}
	got, err := f.Db.Secrets().JobBindings(context.Background(), job.ID)
	if err != nil {
		f.T.Fatal(err)
	}
	return got
}

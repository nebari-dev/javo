package apitest

import (
	"context"

	"github.com/nebari-dev/javo/internal/model"
)

// JobSizeJSON is the size part of a job's view.
type JobSizeJSON struct {
	Size      string `json:"size"`
	Resources *struct {
		CPU    float64 `json:"cpu"`
		Memory int64   `json:"memory"`
	} `json:"resources"`
}

func (f *Fixture) StoredJob(owner, name string) model.Job {
	f.T.Helper()
	j, err := f.Db.Jobs().GetByOwnerName(context.Background(), owner, name)
	if err != nil {
		f.T.Fatal(err)
	}
	return j
}

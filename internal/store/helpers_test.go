package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

var (
	ctx   = context.Background()
	alice = model.Actor{Person: "alice"}
)

var uidRange = store.IDRange{First: 100000, Last: 199999}

func newApp(owner, name string) *model.App {
	return &model.App{
		Namespace: "javo-apps", Name: name, Owner: owner, Template: "jupyterlab",
		DisplayName: name, Size: "small",
	}
}

func createApp(t *testing.T, s *store.Store, owner, name string) model.App {
	t.Helper()
	app, err := s.Apps().Create(ctx, model.Actor{Person: owner}, newApp(owner, name))
	if err != nil {
		t.Fatalf("create app %s: %v", name, err)
	}
	return app
}

func newJob(owner, name string) *model.Job {
	j := &model.Job{
		Namespace: "javo-apps", Name: name, Owner: owner,
		Spec: model.JobSpec{
			Template: "papermill", Engine: "papermill", Kernel: "python3",
			Source: model.JobSource{Path: "nightly.ipynb"},
		},
	}
	j.ApplyDefaults()
	return j
}

func createJob(t *testing.T, s *store.Store, owner, name string) model.Job {
	t.Helper()
	j, err := s.Jobs().Create(ctx, model.Actor{Person: owner}, newJob(owner, name))
	if err != nil {
		t.Fatalf("create job %s: %v", name, err)
	}
	return j
}

func auditCount(t *testing.T, s *store.Store) int {
	t.Helper()
	events, err := s.Audit().List(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return len(events)
}

var t0 = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

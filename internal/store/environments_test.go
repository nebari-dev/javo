package store_test

import (
	"errors"
	"testing"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var salesFiles = model.EnvironmentFiles{Manifest: "[workspace]\nname = \"sales\"\n", Lock: "version: 6\n"}

func withEnvironment[T any](in T, set func(T, *model.Environment)) T {
	set(in, &model.Environment{Source: "nebi", ID: "ws-1", Name: "sales", Version: "3", Digest: salesFiles.Digest()})
	return in
}

func TestAnAppKeepsTheEnvironmentFilesItWasCreatedWith(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	in := withEnvironment(newApp("alice", "lab"), func(a *model.App, e *model.Environment) { a.Environment = e })
	app, err := s.Apps().CreateWith(ctx, alice, in, store.AppExtras{Environment: &salesFiles})
	if err != nil {
		t.Fatal(err)
	}
	if app.Environment == nil || app.Environment.Name != "sales" || app.Environment.Version != "3" {
		t.Fatalf("environment %+v", app.Environment)
	}
	got, err := s.Environments().ForApp(ctx, app.ID)
	if err != nil || got != salesFiles {
		t.Fatalf("ForApp = %+v, %v", got, err)
	}
	if err := s.Apps().MarkDeleted(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Apps().Purge(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Environments().ForApp(ctx, app.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("files outlived the app: %v", err)
	}
}

func TestAJobKeepsTheEnvironmentFilesItWasCreatedWith(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	in := withEnvironment(newJob("alice", "nightly"), func(j *model.Job, e *model.Environment) { j.Spec.Environment = e })
	job, err := s.Jobs().CreateWith(ctx, alice, in, store.JobExtras{Environment: &salesFiles})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Environments().ForJob(ctx, job.ID)
	if err != nil || got != salesFiles {
		t.Fatalf("ForJob = %+v, %v", got, err)
	}
}

func TestEnvironmentFilesMustMatchTheDigestTheSpecNames(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	in := withEnvironment(newApp("alice", "lab"), func(a *model.App, e *model.Environment) { a.Environment = e })
	other := model.EnvironmentFiles{Manifest: "x", Lock: "y"}
	if _, err := s.Apps().CreateWith(ctx, alice, in, store.AppExtras{Environment: &other}); err == nil {
		t.Fatal("stored files that are not the ones the app names")
	}
	if _, err := s.Apps().Create(ctx, alice, in); err == nil {
		t.Fatal("stored an app that names an environment with no files")
	}
}

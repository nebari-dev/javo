package store_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func TestAppKeepsItsPlainVariables(t *testing.T) {
	s := storetest.New(t)
	in := newApp("alice", "lab")
	in.Env = []model.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}, {Name: "EMPTY"}}
	app, err := s.Apps().Create(ctx, alice, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Apps().Get(ctx, app.ID)
	if err != nil || !slices.Equal(got.Env, in.Env) {
		t.Fatalf("env %+v, %v", got.Env, err)
	}
}

// Plain values and secrets change together: a refused secret leaves both as
// they were, and a change moves the version (the app restarts).
func TestSetAppEnvIsOneChange(t *testing.T) {
	s := storetest.New(t)
	createSecret(t, s, "alice", "API_KEY")
	app := createApp(t, s, "alice", "lab")
	plain := []model.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}}
	bound := []model.SecretBinding{{Env: "OPENAI_API_KEY", Secret: "API_KEY"}}
	next, err := s.Apps().SetEnv(ctx, alice, app.ID, app.Version, plain, bound)
	if err != nil {
		t.Fatal(err)
	}
	if next.Version != app.Version+1 || !slices.Equal(next.Env, plain) {
		t.Fatalf("app %+v", next)
	}
	if got, _ := s.Secrets().Bindings(ctx, app.ID); !slices.Equal(got, bound) {
		t.Fatalf("bindings %+v", got)
	}
	var missing *store.SecretMissingError
	_, err = s.Apps().SetEnv(ctx, alice, app.ID, next.Version, []model.EnvVar{{Name: "OTHER", Value: "x"}}, []model.SecretBinding{{Env: "X", Secret: "NOPE"}})
	if !errors.As(err, &missing) {
		t.Fatalf("unknown secret: %v", err)
	}
	if got, _ := s.Apps().Get(ctx, app.ID); !slices.Equal(got.Env, plain) || got.Version != next.Version {
		t.Fatalf("a refused change kept %+v at version %d", got.Env, got.Version)
	}
	if _, err := s.Apps().SetEnv(ctx, alice, app.ID, app.Version, nil, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale version: %v", err)
	}
	// The audit names the variables, not the plain values.
	if text := auditText(t, s); !strings.Contains(text, "app.set_env") || !strings.Contains(text, "LOG_LEVEL") || strings.Contains(text, "debug") {
		t.Fatalf("audit %s", text)
	}
}

func TestSetJobEnvIsOneChange(t *testing.T) {
	s := storetest.New(t)
	createSecret(t, s, "alice", "API_KEY")
	job := createJob(t, s, "alice", "nightly")
	plain := []model.EnvVar{{Name: "REGION", Value: "emea"}}
	bound := []model.SecretBinding{{Env: "KEY", Secret: "API_KEY"}}
	next, err := s.Jobs().SetEnv(ctx, alice, job.ID, job.Version, plain, bound)
	if err != nil {
		t.Fatal(err)
	}
	if next.Version != job.Version+1 || !slices.Equal(next.Spec.Env, plain) {
		t.Fatalf("job %+v", next.Spec.Env)
	}
	if got, _ := s.Secrets().JobBindings(ctx, job.ID); !slices.Equal(got, bound) {
		t.Fatalf("bindings %+v", got)
	}
	if _, err := s.Jobs().SetEnv(ctx, alice, job.ID, next.Version, nil, []model.SecretBinding{{Env: "X", Secret: "NOPE"}}); err == nil {
		t.Fatal("unknown secret accepted")
	}
	if got, _ := s.Jobs().Get(ctx, job.ID); !slices.Equal(got.Spec.Env, plain) {
		t.Fatalf("a refused change kept %+v", got.Spec.Env)
	}
	if text := auditText(t, s); !strings.Contains(text, "job.set_env") || strings.Contains(text, "emea") {
		t.Fatalf("audit %s", text)
	}
}

// setAppSecrets gives app the secrets, keeping its plain values.
func setAppSecrets(s *store.Store, app model.App, bindings []model.SecretBinding) error {
	cur, err := s.Apps().Get(ctx, app.ID)
	if err != nil {
		return err
	}
	_, err = s.Apps().SetEnv(ctx, model.Actor{Person: app.Owner}, app.ID, cur.Version, cur.Env, bindings)
	return err
}

// setJobSecrets gives job the secrets, keeping its plain values.
func setJobSecrets(s *store.Store, job model.Job, bindings []model.SecretBinding) error {
	cur, err := s.Jobs().Get(ctx, job.ID)
	if err != nil {
		return err
	}
	_, err = s.Jobs().SetEnv(ctx, model.Actor{Person: job.Owner}, job.ID, cur.Version, cur.Spec.Env, bindings)
	return err
}

package store_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// A repository token comes from one of the owner's secrets: it
// is bound beside the app's variables, never one of them.
func TestGitTokenFromASecret(t *testing.T) {
	t.Parallel()
	s, _ := startedStore(t)
	createSecret(t, s, "alice", "GH_TOKEN")
	createSecret(t, s, "alice", "API_KEY")
	createSecret(t, s, "bob", "BOB_TOKEN")
	app := createApp(t, s, "alice", "site")
	var missing *store.SecretMissingError
	if err := s.Secrets().SetGitToken(ctx, alice, app, "BOB_TOKEN"); !errors.As(err, &missing) {
		t.Fatalf("another person's secret: %v", err)
	}
	drain(s)
	if err := s.Secrets().SetGitToken(ctx, alice, app, "GH_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != "apps" || c.ID != app.ID {
		t.Fatalf("announced %+v", c)
	}
	if name, err := s.Secrets().GitTokenSecret(ctx, app.ID); err != nil || name != "GH_TOKEN" {
		t.Fatalf("token secret %q, %v", name, err)
	}
	// Setting the variables keeps the token, and the variables never list it.
	if err := setAppSecrets(s, app, []model.SecretBinding{{Env: "KEY", Secret: "API_KEY"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Secrets().Bindings(ctx, app.ID); len(got) != 1 || got[0].Env != "KEY" {
		t.Fatalf("variables %+v", got)
	}
	if name, _ := s.Secrets().GitTokenSecret(ctx, app.ID); name != "GH_TOKEN" {
		t.Fatalf("setting the variables dropped the token: %q", name)
	}
	// The controller gets it with the variables, under its own key.
	sealed, _ := s.Secrets().ForApp(ctx, app.ID)
	if len(sealed) != 2 || sealed[0].Env != model.GitTokenEnv || sealed[0].Secret.Name != "GH_TOKEN" {
		t.Fatalf("ForApp %+v", sealed)
	}
	// The secret is in use: it cannot be deleted, a new value announces the app.
	var inUse *store.SecretInUseError
	if err := s.Secrets().Delete(ctx, alice, "alice", "GH_TOKEN"); !errors.As(err, &inUse) || len(inUse.Apps) != 1 {
		t.Fatalf("delete: %v", err)
	}
	drain(s)
	if _, err := s.Secrets().SetValue(ctx, alice, "alice", "GH_TOKEN", sealedAs("v2")); err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != "apps" || c.ID != app.ID {
		t.Fatalf("a new value announced %+v", c)
	}
	if err := s.Secrets().SetGitToken(ctx, alice, app, ""); err != nil {
		t.Fatal(err)
	}
	if name, _ := s.Secrets().GitTokenSecret(ctx, app.ID); name != "" {
		t.Fatalf("removed token still %q", name)
	}
	if got, _ := s.Secrets().Bindings(ctx, app.ID); len(got) != 1 {
		t.Fatalf("removing the token changed the variables: %+v", got)
	}
}

// A token pasted before tokens came from secrets becomes a secret of the owner, bound as
// the app's token, and the pasted copy is dropped.
func TestAdoptPastedGitToken(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	in := newApp("alice", "site")
	in.Source = &model.Source{URL: "https://github.com/example-org/site"}
	app, err := s.Apps().CreateFromRepo(ctx, alice, in, store.RepoSecrets{Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	createSecret(t, s, "alice", "GIT_TOKEN_SITE")
	var sealedFor string
	sec, err := s.Secrets().AdoptGitToken(ctx, model.Actor{System: model.SystemController}, app, func(id, name string) ([]byte, error) {
		sealedFor = id + "/" + name
		return []byte("sealed"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sec.Name != "GIT_TOKEN_SITE_2" || sec.Owner != "alice" || sealedFor != sec.ID+"/"+sec.Name {
		t.Fatalf("adopted as %+v, sealed for %s", sec, sealedFor)
	}
	if name, _ := s.Secrets().GitTokenSecret(ctx, app.ID); name != sec.Name {
		t.Fatalf("token secret %q", name)
	}
	if _, _, has, _ := s.GitTokens().Pending(ctx, app.ID); has {
		t.Fatal("the pasted token is still the app's")
	}
	if text := auditText(t, s); !strings.Contains(text, "app.git_token_adopt") || strings.Contains(text, testToken) {
		t.Fatalf("audit %s", text)
	}
}

package store_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

const testToken = "ghp_testtoken123"

func repoAppWithToken(t *testing.T, s *store.Store, name string) model.App {
	t.Helper()
	in := newApp("alice", name)
	in.Source = &model.Source{URL: "https://github.com/example-org/private"}
	app, err := s.Apps().CreateFromRepo(ctx, alice, in, store.RepoSecrets{Token: testToken})
	if err != nil {
		t.Fatalf("CreateFromRepo: %v", err)
	}
	return app
}

func auditText(t *testing.T, s *store.Store) string {
	t.Helper()
	events, err := s.Audit().List(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(events)
	return string(raw)
}

func TestCreateFromRepoStoresAppAndTokenTogether(t *testing.T) {
	s := storetest.New(t)
	app := repoAppWithToken(t, s, "private")
	token, gen, has, err := s.GitTokens().Pending(ctx, app.ID)
	if err != nil || !has || token != testToken || gen != 1 {
		t.Fatalf("Pending = %q, %d, %v, %v", token, gen, has, err)
	}
	if _, _, has, _ := s.GitTokens().Pending(ctx, createApp(t, s, "alice", "plain").ID); has {
		t.Fatal("an app made without a token has one")
	}

	// A taken name stores nothing, not even the token.
	dup := newApp("alice", "private")
	dup.Source = &model.Source{URL: "https://github.com/example-org/private"}
	if _, err := s.Apps().CreateFromRepo(ctx, alice, dup, store.RepoSecrets{Token: "ghp_other"}); !errors.Is(err, store.ErrExists) {
		t.Fatalf("duplicate = %v, want ErrExists", err)
	}
	if token, _, _, _ := s.GitTokens().Pending(ctx, app.ID); token != testToken {
		t.Fatalf("a refused create changed the stored token to %q", token)
	}
	// An invalid app stores nothing either.
	bad := newApp("alice", "bad")
	bad.Source = &model.Source{URL: "http://github.com/example-org/private"}
	var ve *model.ValidationError
	if _, err := s.Apps().CreateFromRepo(ctx, alice, bad, store.RepoSecrets{Token: "ghp_other"}); !errors.As(err, &ve) {
		t.Fatalf("invalid source = %v, want a validation error", err)
	}
	if _, err := s.Apps().CreateFromRepo(ctx, alice, newApp("alice", "empty"), store.RepoSecrets{}); err == nil {
		t.Fatal("an app from a repository with nothing to store was accepted")
	}
}

func TestHandedOffClearsOnlyTheGenerationItWasGiven(t *testing.T) {
	s := storetest.New(t)
	app := repoAppWithToken(t, s, "private")
	_, gen, _, _ := s.GitTokens().Pending(ctx, app.ID)

	// The person pastes a new token while the controller is copying the old one.
	if err := s.GitTokens().Set(ctx, alice, app.ID, "ghp_newer"); err != nil {
		t.Fatal(err)
	}
	if err := s.GitTokens().HandedOff(ctx, app.ID, gen); err != nil {
		t.Fatal(err)
	}
	token, newGen, has, _ := s.GitTokens().Pending(ctx, app.ID)
	if token != "ghp_newer" || newGen != gen+1 || !has {
		t.Fatalf("a stale hand-off cleared the newer token: %q, gen %d", token, newGen)
	}

	if err := s.GitTokens().HandedOff(ctx, app.ID, newGen); err != nil {
		t.Fatal(err)
	}
	token, _, has, _ = s.GitTokens().Pending(ctx, app.ID)
	if token != "" || !has {
		t.Fatalf("after hand-off: token %q, has %v; want an empty token on a row that stays", token, has)
	}
}

func TestSetAndRemoveAnnounceAndNeedAnApp(t *testing.T) {
	s, _ := startedStore(t)
	app := createApp(t, s, "alice", "site")
	drain(s)
	if err := s.GitTokens().Set(ctx, alice, app.ID, testToken); err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != "apps" || c.ID != app.ID {
		t.Fatalf("Set change = %+v", c)
	}
	if err := s.GitTokens().Remove(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != "apps" || c.ID != app.ID {
		t.Fatalf("Remove change = %+v", c)
	}
	if _, _, has, _ := s.GitTokens().Pending(ctx, app.ID); has {
		t.Fatal("token row still there after Remove")
	}
	if err := s.GitTokens().Remove(ctx, alice, app.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second Remove = %v, want ErrNotFound", err)
	}
	if err := s.GitTokens().Set(ctx, alice, "missing", testToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Set on a missing app = %v, want ErrNotFound", err)
	}
	drain(s)
	if err := s.GitTokens().HandedOff(ctx, app.ID, 1); err != nil {
		t.Fatal(err)
	}
	requireNoChange(t, s, "HandedOff")
}

func TestTokenNeverReachesTheAuditLog(t *testing.T) {
	s := storetest.New(t)
	app := repoAppWithToken(t, s, "private")
	if err := s.GitTokens().Set(ctx, alice, app.ID, "ghp_rotated456"); err != nil {
		t.Fatal(err)
	}
	if err := s.GitTokens().Remove(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	log := auditText(t, s)
	for _, secret := range []string{testToken, "ghp_rotated456"} {
		if bytes.Contains([]byte(log), []byte(secret)) {
			t.Fatalf("token %q reached the audit log: %s", secret, log)
		}
	}
	for _, action := range []string{"app.create", "app.git_token_set", "app.git_token_remove"} {
		if !bytes.Contains([]byte(log), []byte(action)) {
			t.Errorf("no %s audit row", action)
		}
	}
}

func TestPurgingTheAppRemovesItsTokenRow(t *testing.T) {
	s := storetest.New(t)
	app := repoAppWithToken(t, s, "private")
	if err := s.Apps().MarkDeleted(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Apps().Purge(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, has, err := s.GitTokens().Pending(ctx, app.ID); err != nil || has {
		t.Fatalf("Pending after purge = has %v, %v", has, err)
	}
}

// After the controller handed the token to a Secret, no backup of the database holds it.
func TestBackupAfterHandOffHoldsNoToken(t *testing.T) {
	if storetest.Engine() != store.EngineSQLite {
		t.Skip("Postgres backups are pg_dump; checked in the end-to-end tests")
	}
	s := storetest.New(t)
	app := repoAppWithToken(t, s, "private")
	dir := t.TempDir()

	before := filepath.Join(dir, "before.db")
	if err := s.Backup(ctx, before); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(before)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(testToken)) {
		t.Fatal("test is blind: the backup taken before the hand-off does not hold the token")
	}

	_, gen, _, _ := s.GitTokens().Pending(ctx, app.ID)
	if err := s.GitTokens().HandedOff(ctx, app.ID, gen); err != nil {
		t.Fatal(err)
	}
	after := filepath.Join(dir, "after.db")
	if err := s.Backup(ctx, after); err != nil {
		t.Fatal(err)
	}
	if raw, err = os.ReadFile(after); err != nil || bytes.Contains(raw, []byte(testToken)) {
		t.Fatalf("backup after the hand-off holds the token (read error %v)", err)
	}
}

func TestRemoveThenSetNeverReusesAGeneration(t *testing.T) {
	s := storetest.New(t)
	app := repoAppWithToken(t, s, "private")
	_, oldGen, _, _ := s.GitTokens().Pending(ctx, app.ID)

	// The controller read token A; before it clears the row the person removes
	// the token and pastes token B.
	if err := s.GitTokens().Remove(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.GitTokens().Set(ctx, alice, app.ID, "ghp_tokenb"); err != nil {
		t.Fatal(err)
	}
	if err := s.GitTokens().HandedOff(ctx, app.ID, oldGen); err != nil {
		t.Fatal(err)
	}
	token, gen, has, _ := s.GitTokens().Pending(ctx, app.ID)
	if token != "ghp_tokenb" || !has || gen <= oldGen {
		t.Fatalf("stale hand-off wiped the new paste: %q, gen %d (old %d), has %v", token, gen, oldGen, has)
	}
}

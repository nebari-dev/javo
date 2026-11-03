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

// sealedAs stands in for package secrets: the "ciphertext" names the row it
// was sealed for, so tests can see which id the store handed to seal.
func sealedAs(text string) func(id string) ([]byte, error) {
	return func(id string) ([]byte, error) { return []byte("sealed:" + id + ":" + text), nil }
}

func createSecret(t *testing.T, s *store.Store, owner, name string) model.Secret {
	t.Helper()
	sec, err := s.Secrets().Create(ctx, model.Actor{Person: owner}, owner, name, "a note", sealedAs("v1"))
	if err != nil {
		t.Fatalf("create secret %s: %v", name, err)
	}
	return sec
}

func TestCreateSecretSealsForTheNewRowAndListsWithoutValues(t *testing.T) {
	s := storetest.New(t)
	sec := createSecret(t, s, "alice", "OPENAI_KEY")
	if sec.ID == "" || sec.Version != 1 || sec.Owner != "alice" || sec.Note != "a note" {
		t.Fatalf("created %+v", sec)
	}
	sealed, err := s.Secrets().Sealed(ctx, sec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(sealed) != "sealed:"+sec.ID+":v1" {
		t.Fatalf("stored ciphertext %q was not sealed for the row's id", sealed)
	}
	if _, err := s.Secrets().Create(ctx, alice, "alice", "OPENAI_KEY", "", sealedAs("v2")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("duplicate name: %v", err)
	}
	// Another person may use the same name.
	createSecret(t, s, "bob", "OPENAI_KEY")

	list, err := s.Secrets().List(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "OPENAI_KEY" || list[0].Owner != "alice" {
		t.Fatalf("list %+v", list)
	}
	if strings.Contains(auditText(t, s), "sealed:") {
		t.Fatal("an audit row carries the ciphertext")
	}
}

func TestNewValueMovesTheVersionAndAnnouncesTheAppsThatUseIt(t *testing.T) {
	s, _ := startedStore(t)
	sec := createSecret(t, s, "alice", "DB_PASSWORD")
	app := createApp(t, s, "alice", "lab")
	other := createApp(t, s, "alice", "other")
	if err := setAppSecrets(s, app, []model.SecretBinding{{Env: "PGPASSWORD", Secret: "DB_PASSWORD"}}); err != nil {
		t.Fatal(err)
	}
	drain(s)
	next2, err := s.Secrets().SetValue(ctx, alice, "alice", "DB_PASSWORD", sealedAs("v2"))
	if err != nil {
		t.Fatal(err)
	}
	if next2.Version != 2 {
		t.Fatalf("version %d", next2.Version)
	}
	if c := next(t, s); c.Table != "apps" || c.ID != app.ID {
		t.Fatalf("announced %+v, want the app that uses it", c)
	}
	requireNoChange(t, s, "a new value of a secret "+other.Name+" does not use")
	if sealed, _ := s.Secrets().Sealed(ctx, sec.ID); string(sealed) != "sealed:"+sec.ID+":v2" {
		t.Fatalf("stored %q", sealed)
	}
	if _, err := s.Secrets().SetValue(ctx, alice, "alice", "NOPE", sealedAs("x")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing secret: %v", err)
	}
}

func TestBindingsResolveOnlyTheAppOwnersSecrets(t *testing.T) {
	s := storetest.New(t)
	createSecret(t, s, "bob", "BOB_ONLY")
	createSecret(t, s, "alice", "API_KEY")
	app := createApp(t, s, "alice", "lab")
	var missing *store.SecretMissingError
	err := setAppSecrets(s, app, []model.SecretBinding{{Env: "STOLEN", Secret: "BOB_ONLY"}})
	if !errors.As(err, &missing) || missing.Name != "BOB_ONLY" {
		t.Fatalf("binding another person's secret: %v", err)
	}
	if err := setAppSecrets(s, app, []model.SecretBinding{{Env: "KEY_A", Secret: "API_KEY"}, {Env: "KEY_B", Secret: "API_KEY"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Secrets().Bindings(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.SecretBinding{{Env: "KEY_A", Secret: "API_KEY"}, {Env: "KEY_B", Secret: "API_KEY"}}
	if !slices.Equal(got, want) {
		t.Fatalf("bindings %+v", got)
	}
	// A refused set leaves the earlier one.
	if err := setAppSecrets(s, app, []model.SecretBinding{{Env: "X", Secret: "GONE"}}); err == nil {
		t.Fatal("unknown secret accepted")
	}
	if got, _ := s.Secrets().Bindings(ctx, app.ID); len(got) != 2 {
		t.Fatalf("a refused set changed the bindings to %+v", got)
	}
}

func TestDeletingASecretAnAppUsesIsRefused(t *testing.T) {
	s := storetest.New(t)
	createSecret(t, s, "alice", "TOKEN")
	app := createApp(t, s, "alice", "lab")
	if err := setAppSecrets(s, app, []model.SecretBinding{{Env: "TOKEN", Secret: "TOKEN"}}); err != nil {
		t.Fatal(err)
	}
	var inUse *store.SecretInUseError
	if err := s.Secrets().Delete(ctx, alice, "alice", "TOKEN"); !errors.As(err, &inUse) || len(inUse.Apps) != 1 || inUse.Apps[0].Name != "lab" {
		t.Fatalf("delete of a used secret: %v", err)
	}
	list, _ := s.Secrets().List(ctx, "alice")
	if len(list) != 1 || len(list[0].Apps) != 1 || list[0].Apps[0].Name != "lab" {
		t.Fatalf("list %+v", list)
	}
	// An app being deleted no longer counts.
	if err := s.Apps().MarkDeleted(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Secrets().Delete(ctx, alice, "alice", "TOKEN"); err != nil {
		t.Fatalf("delete once the app is gone: %v", err)
	}
	if list, _ := s.Secrets().List(ctx, "alice"); len(list) != 0 {
		t.Fatalf("left %+v", list)
	}
	if err := s.Secrets().Delete(ctx, alice, "alice", "TOKEN"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestPurgedAppTakesItsBindings(t *testing.T) {
	s := storetest.New(t)
	createSecret(t, s, "alice", "TOKEN")
	app := createApp(t, s, "alice", "lab")
	if err := setAppSecrets(s, app, []model.SecretBinding{{Env: "TOKEN", Secret: "TOKEN"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Apps().MarkDeleted(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Apps().Purge(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Secrets().Bindings(ctx, app.ID); len(got) != 0 {
		t.Fatalf("bindings of a purged app: %+v", got)
	}
}

func TestCreateAppWithSecretsIsOneTransaction(t *testing.T) {
	s := storetest.New(t)
	createSecret(t, s, "alice", "TOKEN")
	app, err := s.Apps().CreateWith(ctx, alice, newApp("alice", "lab"), store.AppExtras{Secrets: []model.SecretBinding{{Env: "MY_TOKEN", Secret: "TOKEN"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Secrets().Bindings(ctx, app.ID); len(got) != 1 || got[0].Env != "MY_TOKEN" {
		t.Fatalf("bindings %+v", got)
	}
	// An unknown secret refuses the whole create.
	if _, err := s.Apps().CreateWith(ctx, alice, newApp("alice", "other"), store.AppExtras{Secrets: []model.SecretBinding{{Env: "X", Secret: "NOPE"}}}); err == nil {
		t.Fatal("created with an unknown secret")
	}
	if _, err := s.Apps().GetByOwnerName(ctx, "alice", "other"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the refused app was stored: %v", err)
	}
}

func TestSealedValuesForAnApp(t *testing.T) {
	s := storetest.New(t)
	sec := createSecret(t, s, "alice", "TOKEN")
	app := createApp(t, s, "alice", "lab")
	if err := setAppSecrets(s, app, []model.SecretBinding{{Env: "MY_TOKEN", Secret: "TOKEN"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Secrets().ForApp(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Env != "MY_TOKEN" || got[0].Secret.ID != sec.ID || got[0].Secret.Owner != "alice" ||
		got[0].Secret.Name != "TOKEN" || got[0].Secret.Version != 1 || string(got[0].Ciphertext) != "sealed:"+sec.ID+":v1" {
		t.Fatalf("ForApp %+v", got)
	}
}

func TestResealKeepsTheVersionAndLosesToANewValue(t *testing.T) {
	s := storetest.New(t)
	sec := createSecret(t, s, "alice", "TOKEN")
	old, _ := s.Secrets().Sealed(ctx, sec.ID)
	// A new value arrives between the rotation's read and its write.
	if _, err := s.Secrets().SetValue(ctx, alice, "alice", "TOKEN", sealedAs("v2")); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Secrets().Reseal(ctx, sec.ID, old, []byte("resealed")); err != nil || ok {
		t.Fatalf("reseal over a newer value: %v, %v", ok, err)
	}
	current, _ := s.Secrets().Sealed(ctx, sec.ID)
	if ok, err := s.Secrets().Reseal(ctx, sec.ID, current, []byte("resealed")); err != nil || !ok {
		t.Fatalf("reseal: %v, %v", ok, err)
	}
	got, _ := s.Secrets().Get(ctx, "alice", "TOKEN")
	if got.Version != 2 {
		t.Fatalf("reseal moved the version to %d", got.Version)
	}
	all, err := s.Secrets().AllSealed(ctx)
	if err != nil || len(all) != 1 || string(all[0].Ciphertext) != "resealed" {
		t.Fatalf("AllSealed %+v, %v", all, err)
	}
}

func TestDeleteForOwnerRemovesOnlyTheirSecrets(t *testing.T) {
	s := storetest.New(t)
	createSecret(t, s, "alice", "A")
	createSecret(t, s, "bob", "B")
	if n, err := s.Secrets().DeleteFor(ctx, model.Actor{System: "test"}, "alice"); err != nil || n != 1 {
		t.Fatalf("DeleteFor = %d, %v", n, err)
	}
	if l, _ := s.Secrets().List(ctx, "bob"); len(l) != 1 {
		t.Fatal("bob's secret went too")
	}
}

func TestKeysetIsWrittenOnce(t *testing.T) {
	s := storetest.New(t)
	if _, found, err := s.SecretKeys().Get(ctx); err != nil || found {
		t.Fatalf("empty: %v, %v", found, err)
	}
	first := store.SecretKeyset{Wrapped: []byte("w1"), Public: []byte("p1")}
	if won, err := s.SecretKeys().Insert(ctx, first); err != nil || !won {
		t.Fatalf("first insert: %v, %v", won, err)
	}
	if won, err := s.SecretKeys().Insert(ctx, store.SecretKeyset{Wrapped: []byte("w2"), Public: []byte("p2")}); err != nil || won {
		t.Fatalf("second insert replaced the first: %v, %v", won, err)
	}
	got, _, _ := s.SecretKeys().Get(ctx)
	if string(got.Wrapped) != "w1" || string(got.Public) != "p1" || got.Version != 1 {
		t.Fatalf("got %+v", got)
	}
	if err := s.SecretKeys().Replace(ctx, store.SecretKeyset{Wrapped: []byte("w3"), Public: []byte("p3")}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SecretKeys().Replace(ctx, store.SecretKeyset{Wrapped: []byte("w4"), Public: []byte("p4")}, 1); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale replace: %v", err)
	}
}

func TestJobBindingsResolveOnlyTheJobOwnersSecrets(t *testing.T) {
	s := storetest.New(t)
	createSecret(t, s, "bob", "BOB_ONLY")
	createSecret(t, s, "alice", "API_KEY")
	job := createJob(t, s, "alice", "nightly")
	var missing *store.SecretMissingError
	err := setJobSecrets(s, job, []model.SecretBinding{{Env: "STOLEN", Secret: "BOB_ONLY"}})
	if !errors.As(err, &missing) || missing.Name != "BOB_ONLY" {
		t.Fatalf("binding another person's secret: %v", err)
	}
	want := []model.SecretBinding{{Env: "KEY_A", Secret: "API_KEY"}, {Env: "KEY_B", Secret: "API_KEY"}}
	if err := setJobSecrets(s, job, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Secrets().JobBindings(ctx, job.ID)
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("bindings %+v, %v", got, err)
	}
	if err := setJobSecrets(s, job, []model.SecretBinding{{Env: "X", Secret: "GONE"}}); err == nil {
		t.Fatal("unknown secret accepted")
	}
	if got, _ := s.Secrets().JobBindings(ctx, job.ID); len(got) != 2 {
		t.Fatalf("a refused set changed the bindings to %+v", got)
	}
	// The audit row names the variables and secrets, never a sealed value.
	text := auditText(t, s)
	if !strings.Contains(text, "job.set_env") || !strings.Contains(text, "KEY_A") || strings.Contains(text, "sealed:") {
		t.Fatalf("audit %s", text)
	}
}

func TestCreateJobWithSecretsIsOneTransaction(t *testing.T) {
	s := storetest.New(t)
	createSecret(t, s, "alice", "TOKEN")
	job, err := s.Jobs().CreateWith(ctx, alice, newJob("alice", "nightly"), store.JobExtras{Secrets: []model.SecretBinding{{Env: "MY_TOKEN", Secret: "TOKEN"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Secrets().JobBindings(ctx, job.ID); len(got) != 1 || got[0].Env != "MY_TOKEN" {
		t.Fatalf("bindings %+v", got)
	}
	if _, err := s.Jobs().CreateWith(ctx, alice, newJob("alice", "other"), store.JobExtras{Secrets: []model.SecretBinding{{Env: "X", Secret: "NOPE"}}}); err == nil {
		t.Fatal("created with an unknown secret")
	}
	if _, err := s.Jobs().GetByOwnerName(ctx, "alice", "other"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the refused job was stored: %v", err)
	}
}

func TestNewValueAnnouncesTheJobsThatUseIt(t *testing.T) {
	s, _ := startedStore(t)
	createSecret(t, s, "alice", "DB_PASSWORD")
	job := createJob(t, s, "alice", "nightly")
	createJob(t, s, "alice", "other")
	if err := setJobSecrets(s, job, []model.SecretBinding{{Env: "PGPASSWORD", Secret: "DB_PASSWORD"}}); err != nil {
		t.Fatal(err)
	}
	drain(s)
	if _, err := s.Secrets().SetValue(ctx, alice, "alice", "DB_PASSWORD", sealedAs("v2")); err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != "jobs" || c.ID != job.ID {
		t.Fatalf("announced %+v, want the job that uses it", c)
	}
	requireNoChange(t, s, "a new value of a secret other does not use")
	// Removing the person's secrets announces the job too.
	if _, err := s.Secrets().DeleteFor(ctx, model.Actor{System: "test"}, "alice"); err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != "jobs" || c.ID != job.ID {
		t.Fatalf("delete-all announced %+v", c)
	}
}

func TestDeletingASecretAJobUsesIsRefused(t *testing.T) {
	s := storetest.New(t)
	createSecret(t, s, "alice", "TOKEN")
	job := createJob(t, s, "alice", "nightly")
	if err := setJobSecrets(s, job, []model.SecretBinding{{Env: "TOKEN", Secret: "TOKEN"}}); err != nil {
		t.Fatal(err)
	}
	var inUse *store.SecretInUseError
	if err := s.Secrets().Delete(ctx, alice, "alice", "TOKEN"); !errors.As(err, &inUse) || len(inUse.Jobs) != 1 || inUse.Jobs[0].Name != "nightly" {
		t.Fatalf("delete of a secret a job uses: %v", err)
	}
	list, _ := s.Secrets().List(ctx, "alice")
	if len(list) != 1 || len(list[0].Jobs) != 1 || list[0].Jobs[0].Name != "nightly" || len(list[0].Apps) != 0 {
		t.Fatalf("list %+v", list)
	}
	// A job being deleted no longer counts; once purged its bindings are gone.
	if err := s.Jobs().MarkDeleted(ctx, alice, job.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Secrets().List(ctx, "alice"); len(list[0].Jobs) != 0 {
		t.Fatalf("a deleted job is listed: %+v", list[0].Jobs)
	}
	if err := s.Jobs().Purge(ctx, alice, job.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Secrets().JobBindings(ctx, job.ID); len(got) != 0 {
		t.Fatalf("bindings of a purged job: %+v", got)
	}
	if err := s.Secrets().Delete(ctx, alice, "alice", "TOKEN"); err != nil {
		t.Fatalf("delete once the job is gone: %v", err)
	}
}

func TestSealedValuesForAJob(t *testing.T) {
	s := storetest.New(t)
	sec := createSecret(t, s, "alice", "TOKEN")
	job := createJob(t, s, "alice", "nightly")
	if err := setJobSecrets(s, job, []model.SecretBinding{{Env: "MY_TOKEN", Secret: "TOKEN"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Secrets().ForJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Env != "MY_TOKEN" || got[0].Secret.ID != sec.ID || got[0].Secret.Owner != "alice" ||
		got[0].Secret.Version != 1 || string(got[0].Ciphertext) != "sealed:"+sec.ID+":v1" {
		t.Fatalf("ForJob %+v", got)
	}
}

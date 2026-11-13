package vault

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
	"github.com/nebari-dev/javo/internal/workload"
)

var (
	ctx   = context.Background()
	sales = workload.Ref{Namespace: "javo", Name: "alice-sales", ID: "app-1", Owner: "alice"}
)

// vaultOn is a vault on a fresh database with a keyset under a new random
// key-encryption key.
func vaultOn(t *testing.T) (Secrets, *store.Store) {
	t.Helper()
	b := make([]byte, secrets.KEKSize)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	kek, err := secrets.KEKFromBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	db := storetest.New(t)
	keys := &secrets.Keys{Store: db, KEK: kek}
	if err := keys.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	return Secrets{Keys: keys}, db
}

// stored is the row of the bundle name in the database.
func stored(t *testing.T, db *store.Store, name string) store.SealedBundle {
	t.Helper()
	row, found, err := db.Bundles().Get(ctx, "javo", name)
	if err != nil || !found {
		t.Fatalf("the row of %s: %v, %v", name, found, err)
	}
	return row
}

func TestABundleIsKeptSealedInTheAppsOwnRow(t *testing.T) {
	s, db := vaultOn(t)
	want := executor.Bundle{Data: map[string][]byte{"token": []byte("clear-value-one")}, Meta: map[string]string{"generation": "3"}}
	if err := s.Put(ctx, sales, "alice-sales.app-git", want); err != nil {
		t.Fatal(err)
	}
	row := stored(t, db, "alice-sales.app-git")
	if row.WorkloadID != "app-1" || row.Owner != "alice" {
		t.Errorf("the row is kept for %s of %s", row.WorkloadID, row.Owner)
	}
	if bytes.Contains(row.Sealed, []byte("clear-value-one")) {
		t.Error("the database holds the value in clear")
	}
	got, err := s.Get(ctx, sales, "alice-sales.app-git")
	if err != nil || got == nil {
		t.Fatalf("Get = %v, %v", got == nil, err)
	}
	if string(got.Data["token"]) != "clear-value-one" || len(got.Data) != 1 || got.Meta["generation"] != "3" {
		t.Errorf("read back %d values, meta %v", len(got.Data), got.Meta)
	}

	// Replaced in place: the same row, the new values.
	want.Data["token"], want.Meta["generation"] = []byte("clear-value-two"), "4"
	if err := s.Put(ctx, sales, "alice-sales.app-git", want); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, sales, "alice-sales.app-git"); err != nil || string(got.Data["token"]) != "clear-value-two" || got.Meta["generation"] != "4" {
		t.Errorf("after the second Put: meta %v, %v", got.Meta, err)
	}
}

func TestAnAppWithoutABundleHasNone(t *testing.T) {
	s, _ := vaultOn(t)
	if got, err := s.Get(ctx, sales, "alice-sales.app-api"); got != nil || err != nil {
		t.Fatalf("Get = %v, %v", got != nil, err)
	}
	if err := s.Delete(ctx, sales, "alice-sales.app-api"); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteRemovesTheBundle(t *testing.T) {
	s, _ := vaultOn(t)
	if err := s.Put(ctx, sales, "alice-sales.app-api", executor.Bundle{Data: map[string][]byte{"token": []byte("v")}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, sales, "alice-sales.app-api"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, sales, "alice-sales.app-api"); got != nil || err != nil {
		t.Fatalf("after Delete: %v, %v", got != nil, err)
	}
}

// A row at the app's name that is kept for another app is never opened,
// replaced or deleted for the app.
func TestABundleOfAnotherAppIsLeftAlone(t *testing.T) {
	s, db := vaultOn(t)
	other := workload.Ref{Namespace: "javo", Name: "bob-sales", ID: "app-2", Owner: "bob"}
	if err := s.Put(ctx, other, "alice-sales.app-api", executor.Bundle{Data: map[string][]byte{"token": []byte("theirs")}}); err != nil {
		t.Fatal(err)
	}
	before := stored(t, db, "alice-sales.app-api")
	if got, err := s.Get(ctx, sales, "alice-sales.app-api"); got != nil || !errors.Is(err, executor.ErrForeign) {
		t.Errorf("Get = %v, %v", got != nil, err)
	}
	if err := s.Put(ctx, sales, "alice-sales.app-api", executor.Bundle{Data: map[string][]byte{"token": []byte("mine")}}); !errors.Is(err, executor.ErrForeign) {
		t.Errorf("Put = %v", err)
	}
	if err := s.Delete(ctx, sales, "alice-sales.app-api"); err != nil {
		t.Errorf("Delete = %v", err)
	}
	if after := stored(t, db, "alice-sales.app-api"); after.WorkloadID != "app-2" || after.Owner != "bob" || !bytes.Equal(after.Sealed, before.Sealed) {
		t.Error("their row changed")
	}
	if got, err := s.Get(ctx, other, "alice-sales.app-api"); err != nil || string(got.Data["token"]) != "theirs" {
		t.Errorf("their bundle no longer reads back: %v", err)
	}
}

// A row copied in the database to another app, another name or another
// namespace does not open there, and the error says nothing of its values.
func TestACopiedRowDoesNotOpen(t *testing.T) {
	s, db := vaultOn(t)
	if err := s.Put(ctx, sales, "alice-sales.app-api", executor.Bundle{Data: map[string][]byte{"token": []byte("clear-value-one")}}); err != nil {
		t.Fatal(err)
	}
	row := stored(t, db, "alice-sales.app-api")
	thief := workload.Ref{Namespace: "javo", Name: "bob-sales", ID: "app-2", Owner: "bob"}
	elsewhere := workload.Ref{Namespace: "team-a", Name: "alice-sales", ID: "app-1", Owner: "alice"}
	for _, c := range []struct {
		why  string
		of   workload.Ref
		name string
	}{
		{"another app", thief, "bob-sales.app-api"},
		{"another name", sales, "alice-sales.app-git"},
		{"another namespace", elsewhere, "alice-sales.app-api"},
	} {
		copied := row
		copied.WorkloadID, copied.Owner = c.of.ID, c.of.Owner
		if ok, err := db.Bundles().Put(ctx, c.of.Namespace, c.name, copied); !ok || err != nil {
			t.Fatalf("%s: copy the row: %v, %v", c.why, ok, err)
		}
		got, err := s.Get(ctx, c.of, c.name)
		if got != nil || !errors.Is(err, secrets.ErrOpen) {
			t.Errorf("%s: Get = %v, %v", c.why, got != nil, err)
		}
		if err != nil && strings.Contains(err.Error(), "clear-value-one") {
			t.Errorf("%s: the error holds the value", c.why)
		}
	}
}

// Sealing needs no key that opens: a vault holding another key-encryption
// key still stores a bundle, cannot read any, and its error says nothing of
// the values.
func TestOnlyTheKeyEncryptionKeyOpens(t *testing.T) {
	s, _ := vaultOn(t)
	b := make([]byte, secrets.KEKSize)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	wrong, err := secrets.KEKFromBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	keyless := Secrets{Keys: &secrets.Keys{Store: s.Keys.Store, KEK: wrong}}
	if err := keyless.Put(ctx, sales, "alice-sales.app-api", executor.Bundle{Data: map[string][]byte{"token": []byte("clear-value-one")}}); err != nil {
		t.Fatal(err)
	}
	got, err := keyless.Get(ctx, sales, "alice-sales.app-api")
	if got != nil || err == nil {
		t.Fatalf("Get with another key = %v, %v", got != nil, err)
	}
	if strings.Contains(err.Error(), "clear-value-one") {
		t.Error("the error holds the value")
	}
	if got, err := s.Get(ctx, sales, "alice-sales.app-api"); err != nil || string(got.Data["token"]) != "clear-value-one" {
		t.Errorf("the right key does not open it: %v", err)
	}
}

package secrets

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var ctx = context.Background()

func TestEnsureMakesTheKeysetOnceAndRefusesAnotherKEK(t *testing.T) {
	db := storetest.New(t)
	kek := testKEK(t)
	keys := &Keys{Store: db, KEK: kek}
	if err := keys.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	first, _, _ := db.SecretKeys().Get(ctx)
	if err := keys.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	again, _, _ := db.SecretKeys().Get(ctx)
	if !bytes.Equal(first.Wrapped, again.Wrapped) {
		t.Fatal("a second Ensure replaced the keyset")
	}
	// Another KEK (a lost or regenerated Kubernetes Secret) never replaces
	// the keyset: that would make every stored value unreadable.
	other := &Keys{Store: db, KEK: testKEK(t)}
	if err := other.Ensure(ctx); err == nil {
		t.Fatal("Ensure accepted a KEK that does not open the stored keyset")
	}
	if now, _, _ := db.SecretKeys().Get(ctx); !bytes.Equal(now.Wrapped, first.Wrapped) {
		t.Fatal("the keyset changed")
	}
}

// seal stores a secret the way the gateway does: sealed with the stored
// public half for its row.
func seal(t *testing.T, keys *Keys, owner, name, value string) model.Secret {
	t.Helper()
	ks, _, err := keys.Store.SecretKeys().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSealer(ks.Public)
	if err != nil {
		t.Fatal(err)
	}
	sec, err := keys.Store.Secrets().Create(ctx, model.Actor{Person: owner}, owner, name, "", func(id string) ([]byte, error) {
		return s.Seal([]byte(value), AD{ID: id, Owner: owner, Name: name})
	})
	if err != nil {
		t.Fatal(err)
	}
	return sec
}

func open(t *testing.T, keys *Keys, sec model.Secret) string {
	t.Helper()
	o, err := keys.Opener(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := keys.Store.Secrets().Sealed(ctx, sec.ID)
	if err != nil {
		t.Fatal(err)
	}
	v, err := o.Open(ct, AD{ID: sec.ID, Owner: sec.Owner, Name: sec.Name})
	if err != nil {
		t.Fatal(err)
	}
	return string(v)
}

func TestRotateResealsEveryValueUnderTheNewKey(t *testing.T) {
	db := storetest.New(t)
	keys := &Keys{Store: db, KEK: testKEK(t)}
	if err := keys.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	a := seal(t, keys, "alice", "A_KEY", "alpha")
	b := seal(t, keys, "bob", "B_KEY", "beta")
	before, _ := keys.Opener(ctx)
	oldA, _ := db.Secrets().Sealed(ctx, a.ID)

	n, bundles, err := keys.Rotate(ctx)
	if err != nil || n != 2 || bundles != 0 {
		t.Fatalf("Rotate = %d, %d, %v", n, bundles, err)
	}
	newA, _ := db.Secrets().Sealed(ctx, a.ID)
	if bytes.Equal(oldA, newA) {
		t.Fatal("the value was not sealed again")
	}
	// Only the rotated keyset opens it now: it is under the new primary key.
	if _, err := before.Open(newA, AD{ID: a.ID, Owner: a.Owner, Name: a.Name}); err == nil {
		t.Fatal("the pre-rotation keyset opened a value sealed after rotation")
	}
	if open(t, keys, a) != "alpha" || open(t, keys, b) != "beta" {
		t.Fatal("values changed")
	}
	// The same value: the version stays, so no app restarts.
	if got, _ := db.Secrets().Get(ctx, "alice", "A_KEY"); got.Version != 1 {
		t.Fatalf("version %d", got.Version)
	}
	// New values use the new key too.
	c := seal(t, keys, "alice", "C_KEY", "gamma")
	cct, _ := db.Secrets().Sealed(ctx, c.ID)
	if _, err := before.Open(cct, AD{ID: c.ID, Owner: c.Owner, Name: c.Name}); err == nil {
		t.Fatal("a value sealed after rotation opened with the old keyset")
	}
	if open(t, keys, c) != "gamma" {
		t.Fatal("new value")
	}
}

// A workload's bundle sealed before a rotation is sealed again too: after
// it, the keyset from before no longer opens the row, and its values and
// notes are the same.
func TestRotateResealsEveryBundleUnderTheNewKey(t *testing.T) {
	db := storetest.New(t)
	keys := &Keys{Store: db, KEK: testKEK(t)}
	if err := keys.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	seal(t, keys, "alice", "A_KEY", "alpha")
	ks, _, _ := db.SecretKeys().Get(ctx)
	sealer, err := NewSealer(ks.Public)
	if err != nil {
		t.Fatal(err)
	}
	ad := BundleAD("javo", "alice-sales.app-git", "app-1")
	sealed, err := sealer.Seal([]byte("clear-value-one"), ad)
	if err != nil {
		t.Fatal(err)
	}
	row := store.SealedBundle{WorkloadID: "app-1", Owner: "alice", Meta: map[string]string{"generation": "3"}, Sealed: sealed}
	if ok, err := db.Bundles().Put(ctx, "javo", "alice-sales.app-git", row); !ok || err != nil {
		t.Fatalf("Put = %v, %v", ok, err)
	}
	before, _ := keys.Opener(ctx)

	nSecrets, nBundles, err := keys.Rotate(ctx)
	if err != nil || nSecrets != 1 || nBundles != 1 {
		t.Fatalf("Rotate = %d, %d, %v", nSecrets, nBundles, err)
	}
	got, _, err := db.Bundles().Get(ctx, "javo", "alice-sales.app-git")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(got.Sealed, sealed) {
		t.Fatal("the bundle was not sealed again")
	}
	if _, err := before.Open(got.Sealed, ad); err == nil {
		t.Fatal("the pre-rotation keyset opened a bundle sealed after rotation")
	}
	after, _ := keys.Opener(ctx)
	if v, err := after.Open(got.Sealed, ad); err != nil || string(v) != "clear-value-one" {
		t.Fatalf("the bundle's values changed: %v", err)
	}
	if got.WorkloadID != "app-1" || got.Owner != "alice" || got.Meta["generation"] != "3" || len(got.Meta) != 1 {
		t.Fatalf("the row is now kept for %s of %s, meta %v", got.WorkloadID, got.Owner, got.Meta)
	}
	// Running it again is safe: the same values under one more key.
	if _, n, err := keys.Rotate(ctx); err != nil || n != 1 {
		t.Fatalf("second Rotate = %d, %v", n, err)
	}
	again, _ := keys.Opener(ctx)
	got, _, _ = db.Bundles().Get(ctx, "javo", "alice-sales.app-git")
	if v, err := again.Open(got.Sealed, ad); err != nil || string(v) != "clear-value-one" {
		t.Fatalf("after a second rotation: %v", err)
	}
}

// A backup of the database holds the sealed value only: neither the value
// nor the private half of the keyset is in its bytes.
func TestBackupHoldsNoValue(t *testing.T) {
	if storetest.Engine() == store.EnginePostgres {
		t.Skip("Postgres backups are pg_dump, run by the chart")
	}
	db := storetest.New(t)
	keys := &Keys{Store: db, KEK: testKEK(t)}
	if err := keys.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	const value = "sk-test-PLACEHOLDER-backup-0451"
	seal(t, keys, "alice", "A_KEY", value)
	file := filepath.Join(t.TempDir(), "backup.db")
	if err := db.Backup(ctx, file); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(value)) || bytes.Contains(raw, []byte("PLACEHOLDER")) {
		t.Fatal("the backup holds the value in clear")
	}
	if !bytes.Contains(raw, []byte("A_KEY")) {
		t.Fatal("the backup does not hold the secret at all; the check above proves nothing")
	}
}

// A ciphertext copied into another row of the database does not open there.
func TestSwappedCiphertextDoesNotOpen(t *testing.T) {
	db := storetest.New(t)
	keys := &Keys{Store: db, KEK: testKEK(t)}
	if err := keys.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	a := seal(t, keys, "alice", "A_KEY", "alpha")
	b := seal(t, keys, "alice", "B_KEY", "beta")
	ctA, _ := db.Secrets().Sealed(ctx, a.ID)
	ctB, _ := db.Secrets().Sealed(ctx, b.ID)
	// Swap them as an attacker with write access to the database would.
	if ok, err := db.Secrets().Reseal(ctx, b.ID, ctB, ctA); err != nil || !ok {
		t.Fatal(ok, err)
	}
	o, _ := keys.Opener(ctx)
	moved, _ := db.Secrets().Sealed(ctx, b.ID)
	if _, err := o.Open(moved, AD{ID: b.ID, Owner: b.Owner, Name: b.Name}); err == nil {
		t.Fatal("a ciphertext moved to another row opened")
	}
}

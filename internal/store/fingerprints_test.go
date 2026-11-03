package store_test

import (
	"testing"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// A secret needs a fingerprint under the current key for its current
// value: one made under another key, or before the value changed, is stale.
func TestFingerprintsFollowTheKeyAndTheValue(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	a := createSecret(t, s, "alice", "DB_PASSWORD")
	createSecret(t, s, "bob", "TOKEN")

	todo := func(key string) []string {
		t.Helper()
		rows, err := s.Secrets().Unfingerprinted(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, r := range rows {
			names = append(names, r.Owner+"/"+r.Name)
		}
		return names
	}
	if got := todo("k1"); len(got) != 2 {
		t.Fatalf("to fingerprint %v, want both", got)
	}
	mine := func() store.OwnerFingerprint {
		t.Helper()
		fps, err := s.Secrets().Fingerprints(ctx, "alice")
		if err != nil || len(fps) != 1 {
			t.Fatalf("alice's fingerprints %+v, %v", fps, err)
		}
		return fps[0]
	}
	if fp := mine(); fp.Name != "DB_PASSWORD" || fp.Fresh("k1") {
		t.Fatalf("before: %+v", fp)
	}

	if err := s.Secrets().SetFingerprint(ctx, store.SecretFingerprint{SecretID: a.ID, Version: a.Version, KeyID: "k1", Length: 16, MAC: "m1"}); err != nil {
		t.Fatal(err)
	}
	if got := todo("k1"); len(got) != 1 || got[0] != "bob/TOKEN" {
		t.Fatalf("to fingerprint %v", got)
	}
	if fp := mine(); !fp.Fresh("k1") || fp.Fresh("k2") || fp.Length != 16 || fp.MAC != "m1" {
		t.Fatalf("after: %+v", fp)
	}
	if got := todo("k2"); len(got) != 2 {
		t.Fatalf("under another key: %v", got)
	}

	// A new value makes the fingerprint stale until it is made again.
	if _, err := s.Secrets().SetValue(ctx, model.Actor{Person: "alice"}, "alice", "DB_PASSWORD", sealedAs("v2")); err != nil {
		t.Fatal(err)
	}
	if fp := mine(); fp.Fresh("k1") {
		t.Fatalf("after a new value: %+v", fp)
	}
	if got := todo("k1"); len(got) != 2 {
		t.Fatalf("to fingerprint after a new value %v", got)
	}
}

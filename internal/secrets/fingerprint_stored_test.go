package secrets

import (
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/store/storetest"
)

func testFingerprinter(t *testing.T) *Fingerprinter {
	t.Helper()
	f, err := FingerprinterFromBytes([]byte(strings.Repeat("f", KEKSize)))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// The controller fingerprints every stored value it can open, once per
// value, and what it stores lets the gateway find the value in text.
func TestFingerprintStoredMakesWhatTheGatewayLooksFor(t *testing.T) {
	db := storetest.New(t)
	keys := &Keys{Store: db, KEK: testKEK(t)}
	if err := keys.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	seal(t, keys, "alice", "DB_PASSWORD", "Vx9-kQ2pLr7mWz4s")
	f := testFingerprinter(t)
	if n, err := keys.FingerprintStored(ctx, f); err != nil || n != 1 {
		t.Fatalf("fingerprinted %d: %v", n, err)
	}
	if n, err := keys.FingerprintStored(ctx, f); err != nil || n != 0 {
		t.Fatalf("second pass fingerprinted %d: %v", n, err)
	}
	stored, err := db.Secrets().Fingerprints(ctx, "alice")
	if err != nil || len(stored) != 1 || !stored[0].Fresh(f.KeyID()) {
		t.Fatalf("stored %+v, %v", stored, err)
	}
	if strings.Contains(stored[0].MAC, "Vx9") {
		t.Fatal("the value is in the fingerprint")
	}
	got := f.Find("password Vx9-kQ2pLr7mWz4s", FingerprintsOf(stored))
	if len(got) != 1 || got[0].Name != "DB_PASSWORD" {
		t.Fatalf("found %+v", got)
	}
}

// Until every value of the owner has a current fingerprint, the lookout says
// so, and so does a gateway with no fingerprint key at all.
func TestLookoutSaysWhenSomeValuesCannotBeLookedFor(t *testing.T) {
	db := storetest.New(t)
	keys := &Keys{Store: db, KEK: testKEK(t)}
	if err := keys.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	f := testFingerprinter(t)
	if _, unchecked, err := Lookout(ctx, db, f, "alice"); err != nil || unchecked {
		t.Fatalf("no secrets: unchecked %v, %v", unchecked, err)
	}
	seal(t, keys, "alice", "DB_PASSWORD", "Vx9-kQ2pLr7mWz4s")
	if _, unchecked, _ := Lookout(ctx, db, f, "alice"); !unchecked {
		t.Fatal("a value without a fingerprint was not reported")
	}
	if _, err := keys.FingerprintStored(ctx, f); err != nil {
		t.Fatal(err)
	}
	find, unchecked, err := Lookout(ctx, db, f, "alice")
	if err != nil || unchecked || len(find("x Vx9-kQ2pLr7mWz4s")) != 1 {
		t.Fatalf("after fingerprinting: unchecked %v, %v", unchecked, err)
	}
	if _, unchecked, _ := Lookout(ctx, db, nil, "alice"); !unchecked {
		t.Fatal("no fingerprint key, yet the values were reported as looked for")
	}
}

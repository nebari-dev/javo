package secrets

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

// testKEK writes a fresh 32-byte key-encryption key to a file and loads it.
func testKEK(t *testing.T) KEK {
	t.Helper()
	b := make([]byte, KEKSize)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := KEKFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newKeys(t *testing.T, kek KEK) Keyset {
	t.Helper()
	ks, err := NewKeyset(kek)
	if err != nil {
		t.Fatal(err)
	}
	return ks
}

func sealer(t *testing.T, ks Keyset) *Sealer {
	t.Helper()
	s, err := NewSealer(ks.Public)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func opener(t *testing.T, ks Keyset, kek KEK) *Opener {
	t.Helper()
	o, err := NewOpener(ks, kek)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

var adA = AD{ID: "s-1", Owner: "alice", Name: "OPENAI_KEY"}

func TestSealThenOpen(t *testing.T) {
	kek := testKEK(t)
	ks := newKeys(t, kek)
	value := []byte("sk-placeholder-value")
	ct, err := sealer(t, ks).Seal(value, adA)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, value) {
		t.Fatal("ciphertext contains the value")
	}
	got, err := opener(t, ks, kek).Open(ct, adA)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("opened %q", got)
	}
}

func TestSealingTwiceGivesDifferentCiphertexts(t *testing.T) {
	ks := newKeys(t, testKEK(t))
	s := sealer(t, ks)
	a, _ := s.Seal([]byte("same"), adA)
	b, _ := s.Seal([]byte("same"), adA)
	if bytes.Equal(a, b) {
		t.Fatal("two seals of one value are equal")
	}
}

// A ciphertext copied onto another row (another id, owner or name) does not open.
func TestCiphertextIsBoundToItsRow(t *testing.T) {
	kek := testKEK(t)
	ks := newKeys(t, kek)
	ct, err := sealer(t, ks).Seal([]byte("v"), adA)
	if err != nil {
		t.Fatal(err)
	}
	o := opener(t, ks, kek)
	for _, other := range []AD{
		{ID: "s-2", Owner: adA.Owner, Name: adA.Name},
		{ID: adA.ID, Owner: "bob", Name: adA.Name},
		{ID: adA.ID, Owner: adA.Owner, Name: "OTHER"},
		// The fields cannot be shifted into one another.
		{ID: adA.ID + adA.Owner, Owner: "", Name: adA.Name},
	} {
		if _, err := o.Open(ct, other); err == nil {
			t.Fatalf("opened under %+v", other)
		}
	}
}

func TestAnotherKEKCannotOpenTheKeyset(t *testing.T) {
	ks := newKeys(t, testKEK(t))
	if _, err := NewOpener(ks, testKEK(t)); err == nil {
		t.Fatal("opened the keyset with another key-encryption key")
	}
}

// The stored keyset holds no private key in clear: neither part opens
// without the key-encryption key, and the public part carries no secret.
func TestPublicPartCannotOpen(t *testing.T) {
	kek := testKEK(t)
	ks := newKeys(t, kek)
	if _, err := NewOpener(Keyset{Wrapped: ks.Public, Public: ks.Public}, kek); err == nil {
		t.Fatal("the public part opened as a private keyset")
	}
}

func TestRotateKeepsOldCiphertextsOpenAndSealsWithTheNewKey(t *testing.T) {
	kek := testKEK(t)
	old := newKeys(t, kek)
	before, err := sealer(t, old).Seal([]byte("before"), adA)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := Rotate(old, kek)
	if err != nil {
		t.Fatal(err)
	}
	o := opener(t, rotated, kek)
	if got, err := o.Open(before, adA); err != nil || string(got) != "before" {
		t.Fatalf("old ciphertext after rotation: %q, %v", got, err)
	}
	after, err := sealer(t, rotated).Seal([]byte("after"), adA)
	if err != nil {
		t.Fatal(err)
	}
	// Only the new key opens it: the old keyset does not have that key.
	if _, err := opener(t, old, kek).Open(after, adA); err == nil {
		t.Fatal("the pre-rotation keyset opened a value sealed after rotation")
	}
	if got, err := o.Open(after, adA); err != nil || string(got) != "after" {
		t.Fatalf("new ciphertext: %q, %v", got, err)
	}
}

func TestKEKFileMustHoldThirtyTwoBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := KEKFromFile(path); err == nil {
		t.Fatal("a 5-byte key was accepted")
	}
}

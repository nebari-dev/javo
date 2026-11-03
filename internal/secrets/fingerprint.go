package secrets

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"

	"github.com/nebari-dev/javo/internal/store"
)

// MinFingerprintLength is the shortest value a fingerprint looks for: a
// shorter one would hide ordinary words and numbers everywhere.
const MinFingerprintLength = 4

// Fingerprinter keys the fingerprints of secret values, so the gateway can
// find a value in text (an app's log, before it goes to a model) without
// being able to open it, and a copy of the database alone cannot test
// guesses against them: the key lives outside the database, in a file both
// the controller (which writes fingerprints) and the gateway (which looks
// for them) read from one Kubernetes Secret.
type Fingerprinter struct {
	key []byte
	id  string
}

// Fingerprint is what is stored of a value: its length and an HMAC-SHA256
// of it under the key KeyID names. An empty MAC is a value too short to
// look for.
type Fingerprint struct {
	Name   string
	KeyID  string
	Length int
	MAC    string
}

// Match is where a fingerprinted value appears in text: bytes Start to End.
type Match struct {
	Start, End int
	Name       string
}

// FingerprinterFromBytes makes a Fingerprinter of b, which must be 32
// random bytes. Its errors never contain key material.
func FingerprinterFromBytes(b []byte) (*Fingerprinter, error) {
	if len(b) != KEKSize {
		return nil, fmt.Errorf("the fingerprint key is %d bytes; it must be %d random bytes", len(b), KEKSize)
	}
	f := &Fingerprinter{key: bytes.Clone(b)}
	m := hmac.New(sha256.New, f.key)
	m.Write([]byte("javo fingerprint key id v1"))
	f.id = hex.EncodeToString(m.Sum(nil))[:16]
	return f, nil
}

// KeyID names the key without revealing it, so fingerprints made under
// another key are known to be stale.
func (f *Fingerprinter) KeyID() string { return f.id }

func (f *Fingerprinter) mac() hash.Hash { return hmac.New(sha256.New, f.key) }

func sum(m hash.Hash, v []byte) string {
	m.Reset()
	m.Write([]byte("javo secret value v1\x00"))
	m.Write(v)
	return hex.EncodeToString(m.Sum(nil))
}

// Fingerprint is the fingerprint of the secret name's value.
func (f *Fingerprinter) Fingerprint(name string, value []byte) Fingerprint {
	fp := Fingerprint{Name: name, KeyID: f.id, Length: len(value)}
	if len(value) >= MinFingerprintLength {
		fp.MAC = sum(f.mac(), value)
	}
	return fp
}

// maxFindWork bounds one Find, in bytes hashed: a few hundred milliseconds.
// Each keyed hash costs about hashOverhead bytes beyond its input.
const (
	maxFindWork  = 1 << 30
	hashOverhead = 512
)

// Find returns every place in text where one of the values fps were made
// of appears, by hashing each stretch of text of each fingerprint's length.
// Fingerprints of another key, or of values too short, find nothing. When
// that would hash more than maxFindWork bytes, text is not searched and one
// match covering all of it is returned, so it is hidden whole rather than
// sent unchecked.
func (f *Fingerprinter) Find(text string, fps []Fingerprint) []Match {
	byLength := map[int]map[string]string{}
	for _, fp := range fps {
		if fp.KeyID != f.id || fp.MAC == "" || fp.Length > len(text) {
			continue
		}
		if byLength[fp.Length] == nil {
			byLength[fp.Length] = map[string]string{}
		}
		byLength[fp.Length][fp.MAC] = fp.Name
	}
	work := 0
	for n := range byLength {
		work += (len(text) - n + 1) * (n + hashOverhead)
	}
	if work > maxFindWork {
		return []Match{{Start: 0, End: len(text), Name: "too much to check"}}
	}
	var out []Match
	m := f.mac()
	b := []byte(text)
	for n, macs := range byLength {
		for i := 0; i+n <= len(b); i++ {
			if name, ok := macs[sum(m, b[i:i+n])]; ok {
				out = append(out, Match{Start: i, End: i + n, Name: name})
			}
		}
	}
	return out
}

// FingerprintsOf is the stored fingerprints as Find takes them.
func FingerprintsOf(stored []store.OwnerFingerprint) []Fingerprint {
	out := make([]Fingerprint, 0, len(stored))
	for _, s := range stored {
		out = append(out, Fingerprint{Name: s.Name, KeyID: s.KeyID, Length: s.Length, MAC: s.MAC})
	}
	return out
}

// Lookout is how to find owner's secret values in text: find returns where
// they are, and unchecked is true when some of them cannot be looked for
// (no fingerprint of the current value under f's key yet, or no f at all),
// so text that may hold them must not be sent anywhere.
func Lookout(ctx context.Context, db *store.Store, f *Fingerprinter, owner string) (find func(text string) []Match, unchecked bool, err error) {
	stored, err := db.Secrets().Fingerprints(ctx, owner)
	if err != nil {
		return nil, false, err
	}
	if f == nil {
		return func(string) []Match { return nil }, len(stored) > 0, nil
	}
	for _, s := range stored {
		if !s.Fresh(f.KeyID()) {
			unchecked = true
		}
	}
	fps := FingerprintsOf(stored)
	return func(text string) []Match { return f.Find(text, fps) }, unchecked, nil
}

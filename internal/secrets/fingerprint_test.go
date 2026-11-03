package secrets_test

import (
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/secrets"
)

func fingerprinter(t *testing.T, key string) *secrets.Fingerprinter {
	t.Helper()
	f, err := secrets.FingerprinterFromBytes([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A fingerprint finds every place a value appears in text without the value
// being stored: only its length and a keyed hash are.
func TestFingerprintsFindEveryOccurrenceOfAValue(t *testing.T) {
	t.Parallel()
	f := fingerprinter(t, strings.Repeat("k", 32))
	pw := f.Fingerprint("DB_PASSWORD", []byte("Vx9-kQ2pLr7mWz4s"))
	if strings.Contains(pw.MAC, "Vx9") || pw.Length != 16 || pw.KeyID != f.KeyID() {
		t.Fatalf("fingerprint %+v", pw)
	}
	text := "connecting to postgres as app with password Vx9-kQ2pLr7mWz4s\nretry: Vx9-kQ2pLr7mWz4s failed"
	got := f.Find(text, []secrets.Fingerprint{pw})
	if len(got) != 2 {
		t.Fatalf("found %+v, want 2", got)
	}
	for _, m := range got {
		if text[m.Start:m.End] != "Vx9-kQ2pLr7mWz4s" || m.Name != "DB_PASSWORD" {
			t.Fatalf("match %+v is %q", m, text[m.Start:m.End])
		}
	}
	// Another key finds nothing, and says so by its id.
	other := fingerprinter(t, strings.Repeat("o", 32))
	if other.KeyID() == f.KeyID() || len(other.Find(text, []secrets.Fingerprint{pw})) != 0 {
		t.Fatal("a fingerprint of another key matched")
	}
}

// Looking for many long values in a long text costs one keyed hash of each
// value's length at every place in it. Past a bound on that work the text is
// not searched but found whole, so it is hidden rather than sent unchecked.
func TestTooMuchToSearchIsFoundWhole(t *testing.T) {
	t.Parallel()
	f := fingerprinter(t, strings.Repeat("k", 32))
	var fps []secrets.Fingerprint
	for i := range 32 {
		fps = append(fps, f.Fingerprint("KEY", []byte(strings.Repeat("v", 16<<10+i))))
	}
	text := strings.Repeat("log line\n", 64<<10/9)
	got := f.Find(text, fps)
	if len(got) != 1 || got[0].Start != 0 || got[0].End != len(text) {
		t.Fatalf("found %+v in %d bytes", got, len(text))
	}
	// Few short values in the same text are searched as usual.
	if got := f.Find(text+"Vx9-kQ2pLr7mWz4s", []secrets.Fingerprint{f.Fingerprint("DB_PASSWORD", []byte("Vx9-kQ2pLr7mWz4s"))}); len(got) != 1 || got[0].Start != len(text) {
		t.Fatalf("short value: %+v", got)
	}
}

// Values too short to look for get an empty fingerprint that finds nothing.
func TestShortValuesAreNotLookedFor(t *testing.T) {
	t.Parallel()
	f := fingerprinter(t, strings.Repeat("k", 32))
	fp := f.Fingerprint("PIN", []byte("123"))
	if fp.MAC != "" || len(f.Find("pin 123 and 123", []secrets.Fingerprint{fp})) != 0 {
		t.Fatalf("fingerprint %+v", fp)
	}
}

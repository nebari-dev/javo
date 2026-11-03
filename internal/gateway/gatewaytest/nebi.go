package gatewaytest

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
)

// fakeNebiSource trades "id-alice" for a numbered token that expires a day
// after now.
type NebiSource struct {
	mu    sync.Mutex
	Calls int
	Now   func() time.Time
}

func (f *NebiSource) Token(_ context.Context, idToken string) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if idToken != "id-alice" {
		return "", time.Time{}, errors.New("refused")
	}
	f.Calls++
	return "nebi-" + string(rune('0'+f.Calls)), f.Now().Add(24 * time.Hour), nil
}

// withKeyset stores a fresh secrets keyset in db, as the controller makes
// one, and returns what opens values sealed under it.
func Keyset(t *testing.T, db *store.Store) *secrets.Opener {
	t.Helper()
	b := make([]byte, secrets.KEKSize)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	kek, err := secrets.KEKFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ks, err := secrets.NewKeyset(kek)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SecretKeys().Insert(context.Background(), store.SecretKeyset{Wrapped: ks.Wrapped, Public: ks.Public}); err != nil {
		t.Fatal(err)
	}
	opener, err := secrets.NewOpener(ks, kek)
	if err != nil {
		t.Fatal(err)
	}
	return opener
}

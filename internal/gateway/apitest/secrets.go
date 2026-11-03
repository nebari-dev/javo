package apitest

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
)

// The value tests look for: if any byte of it shows up where it must not, a
// test fails.
const SecretValue = "sk-test-PLACEHOLDER-value-0451"

type SecretJSON struct {
	Name, Note string
	Apps       []struct{ Name, DisplayName string }
}

// EnvJSON is one environment variable as the API shows it.
type EnvJSON struct{ Name, Value, Secret string }

type BoundJSON struct {
	WorkspaceJSON
	Env []EnvJSON `json:"env"`
}

// WithSecretKeys stores a data keyset, as the controller does at start, and
// returns what opens its values.
func (f *Fixture) WithSecretKeys() *secrets.Opener {
	f.T.Helper()
	b := make([]byte, secrets.KEKSize)
	if _, err := rand.Read(b); err != nil {
		f.T.Fatal(err)
	}
	path := filepath.Join(f.T.TempDir(), "kek")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		f.T.Fatal(err)
	}
	kek, err := secrets.KEKFromFile(path)
	if err != nil {
		f.T.Fatal(err)
	}
	ks, err := secrets.NewKeyset(kek)
	if err != nil {
		f.T.Fatal(err)
	}
	if _, err := f.Db.SecretKeys().Insert(context.Background(), store.SecretKeyset{Wrapped: ks.Wrapped, Public: ks.Public}); err != nil {
		f.T.Fatal(err)
	}
	o, err := secrets.NewOpener(ks, kek)
	if err != nil {
		f.T.Fatal(err)
	}
	return o
}

// CaptureLogs sends every log line of the test to a buffer.
func CaptureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func (f *Fixture) CreateSecret(c *http.Cookie, name, value string) {
	f.T.Helper()
	if w := f.Do("POST", "/api/secrets", fmt.Sprintf(`{"name":%q,"value":%q,"note":"for tests"}`, name, value), c); w.Code != http.StatusCreated {
		f.T.Fatalf("create secret %s: %d %s", name, w.Code, w.Body)
	}
}

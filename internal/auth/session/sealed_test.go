package session_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func keyedStore(t *testing.T, db *store.Store, now *time.Time) *session.DBStore {
	t.Helper()
	key, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return &session.DBStore{Sessions: db.Sessions(), Key: key, Now: func() time.Time { return *now }}
}

func oidcSession() session.Session {
	s := ui(alice)
	s.Provider, s.Refresh, s.IDToken = "oidc", "refresh-secret-1", "id-token-secret-1"
	return s
}

// A copy of the database holds the provider's tokens only sealed: the row
// never has them in clear, and the store with the key reads them back.
func TestProviderTokensAreSealedAtRest(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	db := storetest.New(t)
	st := keyedStore(t, db, &now)
	ctx := context.Background()
	tok := mustCreate(t, st, oidcSession())

	row, err := db.Sessions().Get(ctx, session.ID(tok))
	if err != nil {
		t.Fatal(err)
	}
	if row.Refresh == "" || row.IDToken == "" || strings.Contains(row.Refresh, "refresh-secret") || strings.Contains(row.IDToken, "id-token-secret") {
		t.Fatalf("row holds refresh %q, id token %q", row.Refresh, row.IDToken)
	}
	got, err := st.Get(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if got.Refresh != "refresh-secret-1" || got.IDToken != "id-token-secret-1" {
		t.Fatalf("read back refresh %q, id token %q", got.Refresh, got.IDToken)
	}

	// An update seals the new tokens too.
	got.Refresh = "refresh-secret-2"
	if err := st.Update(ctx, tok, got); err != nil {
		t.Fatal(err)
	}
	row, _ = db.Sessions().Get(ctx, session.ID(tok))
	if strings.Contains(row.Refresh, "refresh-secret") {
		t.Fatalf("updated row holds refresh %q", row.Refresh)
	}
	if again, err := st.Get(ctx, tok); err != nil || again.Refresh != "refresh-secret-2" {
		t.Fatalf("after update: %+v, %v", again, err)
	}
}

// A session without provider tokens (a local account) stores none.
func TestSessionWithoutTokensStoresNone(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	db := storetest.New(t)
	st := keyedStore(t, db, &now)
	tok := mustCreate(t, st, ui(alice))
	row, err := db.Sessions().Get(context.Background(), session.ID(tok))
	if err != nil {
		t.Fatal(err)
	}
	if row.Refresh != "" || row.IDToken != "" {
		t.Fatalf("row holds refresh %q, id token %q", row.Refresh, row.IDToken)
	}
}

// Sealed tokens that do not open (another key, or a row's tokens copied onto
// another row) end the session: the person signs in again.
func TestTokensThatDoNotOpenEndTheSession(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()

	t.Run("another key", func(t *testing.T) {
		db := storetest.New(t)
		tok := mustCreate(t, keyedStore(t, db, &now), oidcSession())
		other := keyedStore(t, db, &now)
		if _, err := other.Get(ctx, tok); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("Get with another key: %v, want ErrSessionNotFound", err)
		}
		if _, err := db.Sessions().Get(ctx, session.ID(tok)); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the session row is still there: %v", err)
		}
	})

	t.Run("copied onto another row", func(t *testing.T) {
		db := storetest.New(t)
		st := keyedStore(t, db, &now)
		from := mustCreate(t, st, oidcSession())
		to := mustCreate(t, st, ui(alice))
		row, err := db.Sessions().Get(ctx, session.ID(from))
		if err != nil {
			t.Fatal(err)
		}
		storetest.Exec(t, db, "UPDATE sessions SET refresh = ?, id_token = ? WHERE id = ?", row.Refresh, row.IDToken, session.ID(to))
		if _, err := st.Get(ctx, to); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("Get of the row with copied tokens: %v, want ErrSessionNotFound", err)
		}
	})

	t.Run("in clear", func(t *testing.T) {
		db := storetest.New(t)
		st := keyedStore(t, db, &now)
		tok := mustCreate(t, st, ui(alice))
		storetest.Exec(t, db, "UPDATE sessions SET refresh = ? WHERE id = ?", "refresh-in-clear", session.ID(tok))
		if _, err := st.Get(ctx, tok); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("Get of a row with a token in clear: %v, want ErrSessionNotFound", err)
		}
	})
}

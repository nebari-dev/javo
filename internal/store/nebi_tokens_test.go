package store_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/store"
)

// An app's sealed Nebi token is kept until replaced, with its expiry, and a
// new one announces the app so the controller copies it to its Secret.
func TestNebiTokenPutGetReplace(t *testing.T) {
	// Started, so on Postgres the LISTEN connection delivers announcements.
	s, _ := startedStore(t)
	app := createApp(t, s, "alice", "lab")
	if _, _, err := s.NebiTokens().Get(ctx, app.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get before Put = %v, want ErrNotFound", err)
	}
	drain(s)
	exp := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if err := s.NebiTokens().Put(ctx, app.ID, []byte("sealed-1"), exp); err != nil {
		t.Fatal(err)
	}
	if c := next(t, s); c.Table != "apps" || c.ID != app.ID {
		t.Fatalf("announced %+v, want the app", c)
	}
	sealed, got, err := s.NebiTokens().Get(ctx, app.ID)
	if err != nil || !bytes.Equal(sealed, []byte("sealed-1")) || !got.Equal(exp) {
		t.Fatalf("Get = %q, %v, %v", sealed, got, err)
	}
	if err := s.NebiTokens().Put(ctx, app.ID, []byte("sealed-2"), exp.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if sealed, got, _ := s.NebiTokens().Get(ctx, app.ID); !bytes.Equal(sealed, []byte("sealed-2")) || !got.Equal(exp.Add(time.Hour)) {
		t.Fatalf("after replace = %q, %v", sealed, got)
	}
	if err := s.NebiTokens().Put(ctx, "no-such-app", []byte("x"), exp); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Put for a missing app = %v, want ErrNotFound", err)
	}
}

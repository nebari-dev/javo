package store_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func TestThumbnailSetGetReplaceRemove(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	app := createApp(t, s, "alice", "dash")

	if _, err := s.Thumbnails().Get(ctx, app.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get before Set = %v, want ErrNotFound", err)
	}
	if d, err := s.Thumbnails().Digest(ctx, app.ID); err != nil || d != "" {
		t.Fatalf("Digest before Set = %q, %v", d, err)
	}

	first := []byte("\x89PNG first")
	if err := s.Thumbnails().Set(ctx, alice, app.ID, "image/png", first); err != nil {
		t.Fatal(err)
	}
	got, err := s.Thumbnails().Get(ctx, app.ID)
	if err != nil || got.ContentType != "image/png" || !bytes.Equal(got.Data, first) || got.Digest == "" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if d, _ := s.Thumbnails().Digest(ctx, app.ID); d != got.Digest {
		t.Fatalf("Digest = %q, want %q", d, got.Digest)
	}

	// A new picture replaces the old and changes the digest, so a cached
	// copy under the old address is never shown for the new one.
	second := []byte("\xff\xd8\xff second")
	if err := s.Thumbnails().Set(ctx, alice, app.ID, "image/jpeg", second); err != nil {
		t.Fatal(err)
	}
	now, _ := s.Thumbnails().Get(ctx, app.ID)
	if now.ContentType != "image/jpeg" || !bytes.Equal(now.Data, second) || now.Digest == got.Digest {
		t.Fatalf("after replace = %+v (old digest %q)", now, got.Digest)
	}

	if err := s.Thumbnails().Remove(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Thumbnails().Get(ctx, app.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after Remove = %v", err)
	}
	if err := s.Thumbnails().Remove(ctx, alice, app.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second Remove = %v, want ErrNotFound", err)
	}

	text := auditText(t, s)
	for _, action := range []string{"app.thumbnail_set", "app.thumbnail_remove"} {
		if !strings.Contains(text, action) {
			t.Errorf("no %s audit row", action)
		}
	}
}

func TestThumbnailRefusedForMissingOrDeletedApp(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	if err := s.Thumbnails().Set(ctx, alice, "no-such-app", "image/png", []byte("x")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Set on a missing app = %v, want ErrNotFound", err)
	}
	app := createApp(t, s, "alice", "gone")
	if err := s.Apps().MarkDeleted(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Thumbnails().Set(ctx, alice, app.ID, "image/png", []byte("x")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Set on a deleted app = %v, want ErrNotFound", err)
	}
}

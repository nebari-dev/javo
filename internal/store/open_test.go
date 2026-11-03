package store_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func TestOpenUnknownEngine(t *testing.T) {
	_, err := store.Open(context.Background(), store.Config{Engine: "mysql"})
	if err == nil || !strings.Contains(err.Error(), "mysql") {
		t.Fatalf("err = %v, want one naming the engine", err)
	}
}

func TestStoreTestFailsWithoutPostgres(t *testing.T) {
	// Nothing listens on port 1: the helper must report an error, not skip.
	_, _, err := storetest.FreshConfig(context.Background(), "postgres",
		"postgres://nobody@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=2", t.TempDir())
	if err == nil {
		t.Fatal("FreshConfig succeeded with no server")
	}
	if _, _, err := storetest.FreshConfig(context.Background(), "postgres", "", t.TempDir()); err == nil {
		t.Fatal("FreshConfig succeeded with no DSN")
	}
}

func TestMetaRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)
	if _, err := s.Meta(ctx, "absent"); err == nil {
		t.Fatal("Meta of a missing key succeeded")
	}
	if err := s.SetMeta(ctx, "k", "one"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta(ctx, "k", "two"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Meta(ctx, "k")
	if err != nil || got != "two" {
		t.Fatalf("Meta = %q, %v; want two", got, err)
	}
}

func TestWritersWait(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Go(func() {
			for j := range 10 {
				if err := s.SetMeta(ctx, fmt.Sprintf("k%d", i), fmt.Sprint(j)); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent write: %v", err)
	}
}

func TestReadOnlyRejectsWrites(t *testing.T) {
	ctx := context.Background()
	cfg := storetest.Config(t)
	rw := storetest.Open(t, cfg)
	if err := rw.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rw.SetMeta(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	cfg.ReadOnly = true
	ro := storetest.Open(t, cfg)
	if got, err := ro.Meta(ctx, "k"); err != nil || got != "v" {
		t.Fatalf("read on read-only store = %q, %v", got, err)
	}
	if err := ro.SetMeta(ctx, "k", "w"); err == nil {
		t.Fatal("write on read-only store succeeded")
	}
}

func TestOpenDBUnknownEngine(t *testing.T) {
	if _, err := store.OpenDB(store.Config{Engine: "mysql"}); err == nil || !strings.Contains(err.Error(), "mysql") {
		t.Fatalf("err = %v, want one naming the engine", err)
	}
}

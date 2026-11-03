package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func TestOpenMigratesEmpty(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t, storetest.Config(t))
	if err := s.Ready(ctx); err == nil {
		t.Fatal("Ready succeeded on an unmigrated database")
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(ctx); err != nil {
		t.Fatalf("Ready after Migrate: %v", err)
	}
}

func TestMigrateTwiceIsNoop(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)
	if err := s.SetMeta(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if got, err := s.Meta(ctx, "k"); err != nil || got != "v" {
		t.Fatalf("data after second Migrate = %q, %v", got, err)
	}
}

func TestConcurrentMigrateOneWins(t *testing.T) {
	ctx := context.Background()
	cfg := storetest.Config(t)
	// Postgres replicas are separate processes with their own connections; a
	// SQLite file belongs to one process, so there both calls share a Store.
	stores := []*store.Store{storetest.Open(t, cfg)}
	if storetest.Engine() == "postgres" {
		stores = append(stores, storetest.Open(t, cfg))
	} else {
		stores = append(stores, stores[0])
	}
	errs := make([]error, len(stores))
	var wg sync.WaitGroup
	for i, s := range stores {
		wg.Go(func() { errs[i] = s.Migrate(ctx) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Migrate %d: %v", i, err)
		}
	}
	if err := stores[0].Ready(ctx); err != nil {
		t.Fatalf("Ready: %v", err)
	}
}

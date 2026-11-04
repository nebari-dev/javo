package ai

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/store/storetest"
)

func TestBudgetIsPerPersonPerUTCDay(t *testing.T) {
	db := storetest.New(t)
	now := time.Date(2026, 11, 1, 23, 30, 0, 0, time.FixedZone("CET", 3600)) // 22:30 UTC
	b := &DBBudget{Usage: db.AIUsage(), Daily: 1000, Now: func() time.Time { return now }}
	ctx := context.Background()

	release, err := b.Reserve(ctx, "alice-a", 600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Reserve(ctx, "alice-a", 600)
	var used *BudgetUsedError
	if !errors.As(err, &used) || !used.ResetAt.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("second reserve: %v", err)
	}
	// The call used 250 of the 600 it reserved.
	release(250)
	if _, err := b.Reserve(ctx, "alice-a", 750); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if _, err := b.Reserve(ctx, "bob-b", 1000); err != nil {
		t.Fatalf("bob has his own allowance: %v", err)
	}
	now = now.Add(2 * time.Hour) // 00:30 UTC on the 2nd
	if _, err := b.Reserve(ctx, "alice-a", 1000); err != nil {
		t.Fatalf("a new UTC day: %v", err)
	}
}

// A call that uses more than it reserved (a retried answer, a longer reply
// than estimated) is charged in full: the overage counts against the day.
func TestBudgetChargesWhatACallUsedAboveItsReservation(t *testing.T) {
	db := storetest.New(t)
	b := &DBBudget{Usage: db.AIUsage(), Daily: 1000}
	ctx := context.Background()
	release, err := b.Reserve(ctx, "alice-a", 300)
	if err != nil {
		t.Fatal(err)
	}
	release(900)
	_, err = b.Reserve(ctx, "alice-a", 300)
	var used *BudgetUsedError
	if !errors.As(err, &used) {
		t.Fatalf("900 of 1000 used, a 300 reservation: %v", err)
	}
	if _, err := b.Reserve(ctx, "alice-a", 100); err != nil {
		t.Fatalf("900 of 1000 used, a 100 reservation: %v", err)
	}
}

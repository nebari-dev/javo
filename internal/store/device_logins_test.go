package store_test

import (
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// Two polls that both read the request as approved race to spend it: only
// one wins, so only one makes a token. A request is decided once.
func TestDeviceLoginIsDecidedAndSpentOnce(t *testing.T) {
	t.Parallel()
	d := storetest.New(t).DeviceLogins()
	if err := d.Create(ctx, store.DeviceLogin{
		UserCode: "BCDF-GHJK", CodeHash: []byte{1, 2, 3}, CreatedAt: t0, ExpiresAt: t0.Add(10 * time.Minute), State: store.DevicePending,
	}); err != nil {
		t.Fatal(err)
	}
	l, err := d.ByCodeHash(ctx, []byte{1, 2, 3})
	if err != nil || l.UserCode != "BCDF-GHJK" || !l.LastPollAt.IsZero() {
		t.Fatalf("ByCodeHash = %+v, %v", l, err)
	}
	if spent, err := d.Spend(ctx, l.ID); err != nil || spent {
		t.Fatalf("Spend while pending = %v, %v", spent, err)
	}
	if ok, err := d.Decide(ctx, alice, l.ID, store.DeviceApproved, "alice"); err != nil || !ok {
		t.Fatalf("Decide = %v, %v", ok, err)
	}
	if ok, err := d.Decide(ctx, alice, l.ID, store.DeviceDenied, "alice"); err != nil || ok {
		t.Fatalf("second Decide = %v, %v", ok, err)
	}
	if spent, err := d.Spend(ctx, l.ID); err != nil || !spent {
		t.Fatalf("first Spend = %v, %v", spent, err)
	}
	if spent, err := d.Spend(ctx, l.ID); err != nil || spent {
		t.Fatalf("second Spend = %v, %v", spent, err)
	}
	if got, err := d.ByUserCode(ctx, "BCDF-GHJK"); err != nil || got.State != store.DeviceSpent || got.Owner != "alice" {
		t.Fatalf("ByUserCode = %+v, %v", got, err)
	}
	if n, err := d.Sweep(ctx, t0.Add(11*time.Minute)); err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v", n, err)
	}
	if _, err := d.ByUserCode(ctx, "BCDF-GHJK"); err != store.ErrNotFound {
		t.Fatalf("after Sweep: %v", err)
	}
}

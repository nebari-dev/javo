package admin_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/store"
)

// Usage adds up how long apps ran in the last days, per person, from the
// intervals the controller recorded.
func TestAdminUsagePerPerson(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	f := apitest.New(t, apitest.Options{Now: func() time.Time { return now }})
	u := f.Db.Usage()
	ctx := context.Background()
	for _, i := range []store.UsageInterval{
		{AppID: "a1", Owner: apitest.AliceID, StartedAt: now.Add(-3 * time.Hour), StoppedAt: now.Add(-time.Hour)},
		{AppID: "a2", Owner: apitest.AliceID, StartedAt: now.Add(-30 * time.Minute)},
		// Ended before the 7-day window.
		{AppID: "c1", Owner: apitest.MemberID, StartedAt: now.Add(-10 * 24 * time.Hour), StoppedAt: now.Add(-9 * 24 * time.Hour)},
	} {
		if err := u.Start(ctx, i); err != nil {
			t.Fatal(err)
		}
		if !i.StoppedAt.IsZero() {
			if err := u.Stop(ctx, i.AppID, i.StoppedAt); err != nil {
				t.Fatal(err)
			}
		}
	}
	w := f.Do(http.MethodGet, "/api/admin/usage?days=7", "", f.Session(apitest.AdminID, "javo-admins"))
	if w.Code != http.StatusOK {
		t.Fatalf("usage: %d %s", w.Code, w.Body)
	}
	got := apitest.Decode[apitest.UsageJSON](t, w)
	if len(got.Items) != 1 || got.Items[0].Key != apitest.AliceID || got.Items[0].Hours != 2.5 || got.Items[0].Apps != 2 || got.Items[0].RunningNow != 1 {
		t.Errorf("items = %+v", got.Items)
	}
	if got.Totals.People != 1 || got.Totals.RunningNow != 1 || !got.Since.Equal(now.Add(-7*24*time.Hour)) {
		t.Errorf("totals = %+v since %v", got.Totals, got.Since)
	}
}

package admin_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
)

// The admin sees each restore's held apps and lets them go: with Start, the
// apps that were running start at the chosen rate (so some are scheduled for
// later); the ones that were stopped go at once.
func TestAdminReleasesARestore(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	f := apitest.New(t, apitest.Options{Now: func() time.Time { return now }}, gatewaytest.Template("t", "streamlit"),
		apitest.HeldApp(apitest.AliceID, "a", "r1", false), apitest.HeldApp(apitest.AliceID, "b", "r1", false), apitest.HeldApp(apitest.MemberID, "c", "r1", true))
	admin := f.Session(apitest.AdminID, "javo-admins")

	w := f.Do(http.MethodGet, "/api/admin/restores", "", admin)
	got := apitest.Decode[apitest.RestoresJSON](t, w)
	if w.Code != http.StatusOK || len(got.Items) != 1 || got.Items[0].Name != "r1" || got.Items[0].Held != 3 || got.Items[0].WasRunning != 2 {
		t.Fatalf("restores: %d %s", w.Code, w.Body)
	}

	w = f.Do(http.MethodPost, "/api/admin/restores/r1/release", `{"mode":"start","perMinute":1}`, admin)
	if w.Code != http.StatusOK {
		t.Fatalf("release: %d %s", w.Code, w.Body)
	}
	if a, b := f.GetWorkspace(apitest.AliceID, "a"), f.GetWorkspace(apitest.AliceID, "b"); !a.ReleaseAt.Equal(now) || !b.ReleaseAt.Equal(now.Add(time.Minute)) {
		t.Errorf("release times a %v b %v, want now and a minute later", a.ReleaseAt, b.ReleaseAt)
	}
	got = apitest.Decode[apitest.RestoresJSON](t, f.Do(http.MethodGet, "/api/admin/restores", "", admin))
	if len(got.Items) != 1 || got.Items[0].Held != 1 || got.Items[0].Scheduled != 1 {
		t.Errorf("after release: %+v", got.Items)
	}

	if w := f.Do(http.MethodPost, "/api/admin/restores/nope/release", `{"mode":"stopped"}`, admin); w.Code != http.StatusNotFound {
		t.Errorf("release of an unknown restore: %d %s", w.Code, w.Body)
	}
}

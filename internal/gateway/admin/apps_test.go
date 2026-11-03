package admin_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/reasons"
)

// A platform admin sees every person's apps, and can narrow them to one
// owner; a deleted app is not listed.
func TestAdminListsEveryonesApps(t *testing.T) {
	gone := gatewaytest.App(apitest.MemberID, "gone")
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("t", "streamlit"),
		gatewaytest.App(apitest.AliceID, "plots"), gatewaytest.App(apitest.MemberID, "notes"), gone)
	if err := f.Db.Apps().MarkDeleted(context.Background(), gatewaytest.Actor, f.GetWorkspace(apitest.MemberID, "gone").ID); err != nil {
		t.Fatal(err)
	}
	admin := f.Session(apitest.AdminID, "javo-admins")

	w := f.Do(http.MethodGet, "/api/admin/apps", "", admin)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	got := map[string]string{}
	for _, it := range apitest.Decode[apitest.AdminAppsJSON](t, w).Items {
		got[it.App.Name] = it.Owner.Name
	}
	if len(got) != 2 || got["plots"] != apitest.AliceID || got["notes"] != apitest.MemberID {
		t.Errorf("apps = %v, want plots of alice and notes of carol", got)
	}

	w = f.Do(http.MethodGet, "/api/admin/apps?owner="+apitest.AliceID, "", admin)
	if items := apitest.Decode[apitest.AdminAppsJSON](t, w).Items; len(items) != 1 || items[0].App.Name != "plots" {
		t.Errorf("owner filter: %+v", items)
	}
}

// The owner is labelled by their display name, else their username, never
// the internal identity name: a local account has no display name, and a
// person deleted since keeps only the readable part of their identity name.
func TestAdminAppsLabelOwnersByName(t *testing.T) {
	DaveID := apitest.PersonID("dave")
	f := apitest.New(t, apitest.Options{People: []apitest.TestPerson{apitest.SignedIn("alice", time.Time{}), apitest.SignedInNoName("carol")}},
		gatewaytest.Template("t", "streamlit"), gatewaytest.App(apitest.PersonID("alice"), "plots"), gatewaytest.App(apitest.PersonID("carol"), "notes"), gatewaytest.App(DaveID, "old"))
	w := f.Do(http.MethodGet, "/api/admin/apps", "", f.Session(apitest.AdminID, "javo-admins"))
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	type label struct{ display, username string }
	got := map[string]label{}
	for _, it := range apitest.Decode[apitest.AdminAppsJSON](t, w).Items {
		got[it.App.Name] = label{it.Owner.DisplayName, it.Owner.Username}
	}
	want := map[string]label{"plots": {"Alice", "alice"}, "notes": {"carol", "carol"}, "old": {"dave", ""}}
	for app, l := range want {
		if got[app] != l {
			t.Errorf("owner of %s = %+v, want %+v", app, got[app], l)
		}
	}
}

// A restored app waits for an admin: its owner cannot start it.
func TestHeldAppCannotBeStarted(t *testing.T) {
	held := gatewaytest.App(apitest.AliceID, "plots")
	held.RestoreHold, held.Paused = "r1", true
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("t", "streamlit"), held)
	w := f.Do(http.MethodPost, "/api/workspaces/plots/start", "", f.Session(apitest.AliceID))
	if w.Code != http.StatusConflict || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgRestoreHeld) {
		t.Fatalf("start of a held app: %d %s", w.Code, w.Body)
	}
}

// When an admin stops someone else's app it records that an admin did, so
// the owner is told why; the owner's own stop does not, and starting the
// app clears it.
func TestAdminStopIsRecordedForTheOwner(t *testing.T) {
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("t", "streamlit"), gatewaytest.App(apitest.AliceID, "plots"), gatewaytest.App(apitest.AliceID, "notes"))
	admin := f.Session(apitest.AdminID, "javo-admins")
	alice := f.Session(apitest.AliceID)

	if w := f.Do(http.MethodPost, "/api/workspaces/plots/stop?owner="+apitest.AliceID, "", admin); w.Code != http.StatusOK {
		t.Fatalf("admin stop: %d %s", w.Code, w.Body)
	}
	if a := f.GetWorkspace(apitest.AliceID, "plots"); !a.Paused || !a.StoppedByAdmin {
		t.Errorf("after the admin's stop: paused %v, stopped by admin %v", a.Paused, a.StoppedByAdmin)
	}
	if w := f.Do(http.MethodPost, "/api/workspaces/notes/stop", "", alice); w.Code != http.StatusOK {
		t.Fatalf("own stop: %d %s", w.Code, w.Body)
	}
	if a := f.GetWorkspace(apitest.AliceID, "notes"); !a.Paused || a.StoppedByAdmin {
		t.Errorf("after her own stop: paused %v, stopped by admin %v", a.Paused, a.StoppedByAdmin)
	}
	if w := f.Do(http.MethodPost, "/api/workspaces/plots/start", "", alice); w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	if a := f.GetWorkspace(apitest.AliceID, "plots"); a.Paused || a.StoppedByAdmin {
		t.Errorf("after start: paused %v, stopped by admin %v", a.Paused, a.StoppedByAdmin)
	}
	events, err := f.Db.Audit().List(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Actor == apitest.AdminID && e.ObjectName == "plots" {
			found = true
		}
	}
	if !found {
		t.Error("no audit row names the admin who stopped plots")
	}
}

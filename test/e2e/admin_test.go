//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"os"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/naming"
)

// adminSession signs in a platform admin: the local admin of a Compose
// install when JAVO_ADMIN_PASSWORD_FILE names its password, else Keycloak's
// alice (in /javo-admins) on kind.
func adminSession(t *testing.T) *session {
	t.Helper()
	s := newSession(t)
	if f := os.Getenv("JAVO_ADMIN_PASSWORD_FILE"); f != "" {
		s.loginLocal("admin", readSecret(t, f))
		return s
	}
	requireKeycloak(t)
	s.login(keycloakLogin, "alice")
	return s
}

type adminApps struct {
	Items []struct {
		ID    string
		Owner struct{ Name string }
		App   app
	}
}

type transferRows struct {
	Rows []struct{ Name, Owner, Outcome, Message string }
}

func firstReason(a app) string {
	if len(a.Steps) == 0 {
		return ""
	}
	return a.Steps[0].Reason
}

// A platform admin sees a member's app among everyone's and stops it; the
// member is told an admin did. The member cannot reach the admin routes.
func TestAdminStopsSomeonesApp(t *testing.T) {
	c := kube(t)
	alice := newSession(t)
	alice.login(localLogin, "alice")
	me := alice.me()
	a, _ := runningSample(t, alice, c, me.ID, "e2e admin "+time.Now().Format("150405"))
	t.Cleanup(func() { alice.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil) })

	if resp, _ := alice.do(http.MethodGet, "/api/admin/apps", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a member listing every app: %d, want 403", resp.StatusCode)
	}
	admin := adminSession(t)
	var list adminApps
	admin.json(http.MethodGet, "/api/admin/apps?owner="+me.ID, nil, http.StatusOK, &list)
	if !slices.ContainsFunc(list.Items, func(i struct {
		ID    string
		Owner struct{ Name string }
		App   app
	}) bool {
		return i.App.Name == a.Name
	}) {
		t.Fatalf("the admin's list lacks %s: %+v", a.Name, list.Items)
	}
	admin.json(http.MethodPost, "/api/workspaces/"+a.Name+"/stop?owner="+me.ID, nil, http.StatusOK, nil)
	got := alice.waitFor(a.Name, 2*time.Minute, func(a app) bool { return a.Phase == "Stopped" })
	if firstReason(got) != "StoppedByAdmin" {
		t.Fatalf("the owner sees %q, want StoppedByAdmin", summary(got))
	}
}

// Back up now, delete an app, preview the restore (the app comes back),
// restore it: it waits for the admin, then starts when released.
func TestRestorePreviewBringsBackADeletedApp(t *testing.T) {
	c := kube(t)
	if !sqliteInstall(t) {
		t.Skip("restore previews read SQLite backups; Postgres restores with pg_restore")
	}
	admin := adminSession(t)
	var backups struct {
		Database struct{ CanBackUpNow bool }
	}
	admin.json(http.MethodGet, "/api/admin/backups", nil, http.StatusOK, &backups)
	if !backups.Database.CanBackUpNow {
		t.Fatal("this install takes no database backups: set database.backup.schedule (deploy/dev/values.yaml does)")
	}
	alice := newSession(t)
	alice.login(localLogin, "alice")
	me := alice.me()
	a, _ := runningSample(t, alice, c, me.ID, "e2e restore "+time.Now().Format("150405"))

	var taken struct{ Name string }
	admin.json(http.MethodPost, "/api/admin/backups", nil, http.StatusCreated, &taken)
	if taken.Name == "" {
		t.Fatal("back up now named no backup")
	}
	if resp, data := alice.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d %s", resp.StatusCode, data)
	}

	scope := map[string]string{"owner": me.ID, "app": a.Name}
	// The row is purged once the controller removed the app's objects; until
	// then the preview says it exists.
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(2 * time.Second) {
		var p transferRows
		admin.json(http.MethodPost, "/api/admin/backups/"+taken.Name+"/preview", scope, http.StatusOK, &p)
		if len(p.Rows) == 1 && p.Rows[0].Outcome == "Create" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("preview: %+v, want one Create", p.Rows)
		}
	}
	var r transferRows
	admin.json(http.MethodPost, "/api/admin/backups/"+taken.Name+"/restore", scope, http.StatusOK, &r)
	if len(r.Rows) != 1 || r.Rows[0].Outcome != "Created" {
		t.Fatalf("restore: %+v", r.Rows)
	}
	t.Cleanup(func() { alice.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil) })

	held := alice.waitFor(a.Name, time.Minute, func(a app) bool { return firstReason(a) == "RestoreHeld" })
	if held.Phase != "Pending" {
		t.Fatalf("restored app: %s", summary(held))
	}
	if appPodExists(t, c, me.ID, a.Name) {
		t.Fatal("a held app has a pod")
	}
	releaseRestore(t, admin, taken.Name, 1) // the restore holds this one app
	alice.waitFor(a.Name, 4*time.Minute, running(t))
}

// releaseRestore starts the restore's apps that were running, perMinute of
// them a minute, in name order.
func releaseRestore(t *testing.T, admin *session, name string, perMinute int) {
	t.Helper()
	admin.json(http.MethodPost, "/api/admin/restores/"+name+"/release", map[string]any{"mode": "start", "perMinute": perMinute}, http.StatusOK, nil)
}

// appPodExists reports whether the app has a pod now.
func appPodExists(t *testing.T, c client.Client, ownerID, short string) bool {
	t.Helper()
	err := c.Get(context.Background(), client.ObjectKey{Namespace: appsNS, Name: naming.ObjectName(ownerID, short)}, &corev1.Pod{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

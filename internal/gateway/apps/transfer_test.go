package apps_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/transfer"
)

// Export holds the caller's own apps and nobody else's, also for an admin.
func TestExportOnlyCallersApps(t *testing.T) {
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"),
		gatewaytest.Workspace("plots", apitest.AliceID, "Plots"), gatewaytest.Workspace("notes", apitest.AliceID, "Notes"), gatewaytest.Workspace("secret", apitest.MemberID, "Secret"))
	w := f.Do(http.MethodGet, "/api/workspaces/export", "", f.Session(apitest.AliceID, "javo-admins"))
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/yaml") {
		t.Fatalf("export: %d %s %s", w.Code, w.Header(), w.Body)
	}
	items, err := transfer.Parse(w.Body.Bytes())
	if err != nil || len(items) != 2 {
		t.Fatalf("parse export: %d items, %v\n%s", len(items), err, w.Body)
	}
	if strings.Contains(w.Body.String(), "Secret") || strings.Contains(w.Body.String(), apitest.AliceID) {
		t.Errorf("export leaks another app or the owner id:\n%s", w.Body)
	}
}

// Export, delete one app, import: the preview says which app comes back and
// which already exists; the import creates it stopped and owned by the caller.
func TestExportImportRoundTrip(t *testing.T) {
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"),
		apitest.WithFile(gatewaytest.Workspace("plots", apitest.AliceID, "Plots")), apitest.WithFile(gatewaytest.Workspace("notes", apitest.AliceID, "Notes")))
	alice := f.Session(apitest.AliceID)
	exported := f.Do(http.MethodGet, "/api/workspaces/export", "", alice).Body.String()
	notes := f.GetWorkspace(apitest.AliceID, "notes")
	if w := f.Do(http.MethodDelete, "/api/workspaces/notes", "", alice); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	// The controller purges the row once the app's objects are gone.
	if err := f.Db.Apps().Purge(context.Background(), gatewaytest.Actor, notes.ID); err != nil {
		t.Fatal(err)
	}

	w := f.Do(http.MethodPost, "/api/workspaces/import/preview", apitest.ImportBody(exported, "skip"), alice)
	if w.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
	outcomes := map[string]string{}
	for _, r := range apitest.Decode[apitest.TransferJSON](t, w).Rows {
		outcomes[r.DisplayName] = r.Outcome
	}
	if outcomes["Plots"] != "Exists" || outcomes["Notes"] != "Create" {
		t.Fatalf("preview outcomes = %v", outcomes)
	}
	if f.WorkspaceCount() != 1 {
		t.Fatal("the preview created an app")
	}

	w = f.Do(http.MethodPost, "/api/workspaces/import", apitest.ImportBody(exported, "skip"), alice)
	if w.Code != http.StatusOK {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	notes = f.GetWorkspace(apitest.AliceID, "notes")
	if !notes.Paused || notes.Owner != apitest.AliceID || notes.File != "app.py" && notes.File == "" {
		t.Errorf("imported = %+v", notes)
	}
	if f.WorkspaceCount() != 2 {
		t.Errorf("apps after import = %d, want 2 (Plots skipped)", f.WorkspaceCount())
	}

	// Rename brings in a copy of an app that exists under a free name.
	w = f.Do(http.MethodPost, "/api/workspaces/import", apitest.ImportBody(apitest.StreamlitApp(apitest.AliceID, "Plots"), "rename"), alice)
	if w.Code != http.StatusOK {
		t.Fatalf("rename import: %d %s", w.Code, w.Body)
	}
	if f.GetWorkspace(apitest.AliceID, "plots-2").DisplayName != "Plots 2" {
		t.Error("rename did not create Plots 2")
	}
}

// Whatever owner a file names, the import belongs to the caller.
func TestImportForcesCallerAsOwner(t *testing.T) {
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"))
	withOwner := strings.Replace(apitest.StreamlitApp(apitest.MemberID, "Theirs"), "file: app.py}", "file: app.py, owner: "+apitest.MemberID+"}", 1)
	w := f.Do(http.MethodPost, "/api/workspaces/import", apitest.ImportBody(apitest.StreamlitApp(apitest.MemberID, "Plots")+withOwner, "skip"), f.Session(apitest.AliceID))
	if w.Code != http.StatusOK {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	if rows := apitest.Decode[apitest.TransferJSON](t, w).Rows; len(rows) != 2 || rows[1].Outcome != "Invalid" {
		t.Errorf("a document naming an owner was not refused: %+v", rows)
	}
	apps := f.Apps()
	if len(apps) != 1 || apps[0].Owner != apitest.AliceID {
		t.Errorf("apps = %+v, want one of alice's", apps)
	}
}

// A file that is too big, has too many apps, or is not YAML is refused with
// catalog text; an entry that is not a valid app is a row of its own.
func TestImportRejectsOversizeAndBadFiles(t *testing.T) {
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"))
	alice := f.Session(apitest.AliceID)

	big := strings.Repeat("#", transfer.MaxBytes+1)
	if w := f.Do(http.MethodPost, "/api/workspaces/import/preview", apitest.ImportBody(big, "skip"), alice); w.Code != http.StatusRequestEntityTooLarge ||
		apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgImportTooLarge) {
		t.Errorf("too large: %d %s", w.Code, w.Body)
	}
	many := strings.Repeat(apitest.StreamlitApp(apitest.AliceID, "A"), transfer.MaxDocuments+1)
	if w := f.Do(http.MethodPost, "/api/workspaces/import/preview", apitest.ImportBody(many, "skip"), alice); w.Code != http.StatusUnprocessableEntity ||
		apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgImportTooMany, transfer.MaxDocuments) {
		t.Errorf("too many: %d %s", w.Code, w.Body)
	}
	if w := f.Do(http.MethodPost, "/api/workspaces/import/preview", apitest.ImportBody("a: [unclosed", "skip"), alice); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("not YAML: %d %s", w.Code, w.Body)
	}
	bad := apitest.StreamlitApp(apitest.AliceID, "Good") + "---\napiVersion: v1\nkind: Secret\nmetadata: {name: s}\n" +
		"---\napiVersion: javo.nebari.dev/v1alpha1\nkind: App\nmetadata: {name: x}\nspec: {displayName: Nope, template: missing}\n"
	w := f.Do(http.MethodPost, "/api/workspaces/import", apitest.ImportBody(bad, "skip"), alice)
	if w.Code != http.StatusOK {
		t.Fatalf("mixed import: %d %s", w.Code, w.Body)
	}
	rows := apitest.Decode[apitest.TransferJSON](t, w).Rows
	if len(rows) != 3 || rows[0].Outcome != "Created" || rows[1].Outcome != "Invalid" || rows[2].Outcome != "Invalid" || rows[2].Message == "" {
		t.Errorf("rows = %+v", rows)
	}
	if f.WorkspaceCount() != 1 {
		t.Errorf("apps = %d, want only Good", f.WorkspaceCount())
	}
}

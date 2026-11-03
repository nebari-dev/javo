package apps_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/gateway/podlogs"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// A template gated to /org/ml: a platform admin sees it, a member outside the
// group neither sees nor uses it, and a member named in allowedUsers does both.
func TestTemplateGatingByUserAndGroup(t *testing.T) {
	t.Parallel()
	ml := gatewaytest.Template("ml-lab", "jupyterlab", "/org/ml")
	f := apitest.New(t, apitest.Options{Roles: apitest.RolesConfig()}, ml, gatewaytest.Template("streamlit", "streamlit"))
	alice := f.Session(apitest.AliceID, "javo-admins")
	bob := f.Session(apitest.BobID, "/org/members")

	if got := apitest.TemplateNames(t, f, alice); !slices.Equal(got, []string{"ml-lab", "streamlit"}) {
		t.Fatalf("admin sees %v", got)
	}
	if got := apitest.TemplateNames(t, f, bob); !slices.Equal(got, []string{"streamlit"}) {
		t.Fatalf("bob sees %v", got)
	}
	body := `{"displayName":"Lab","template":"ml-lab"}`
	w := f.Do("POST", "/api/workspaces", body, bob)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bob create %d: %s", w.Code, w.Body)
	}
	if _, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.template"); !ok {
		t.Fatalf("errors %s", w.Body)
	}

	stored, err := f.Db.Templates().Get(context.Background(), "ml-lab")
	if err != nil {
		t.Fatal(err)
	}
	stored.Spec.AllowedUsers = []string{apitest.BobID}
	if _, err := f.Db.Templates().Put(context.Background(), gatewaytest.Actor, stored, stored.Version); err != nil {
		t.Fatal(err)
	}
	if got := apitest.TemplateNames(t, f, bob); !slices.Equal(got, []string{"ml-lab", "streamlit"}) {
		t.Fatalf("listed bob sees %v", got)
	}
	if w := f.Do("POST", "/api/workspaces", body, bob); w.Code != http.StatusCreated {
		t.Fatalf("listed bob create %d: %s", w.Code, w.Body)
	}
}

func TestTemplateListCarriesMaxSharing(t *testing.T) {
	t.Parallel()
	tpl := gatewaytest.Template("streamlit", "streamlit")
	tpl.Spec.MaxSharing = model.AccessSignedIn
	f := apitest.New(t, apitest.Options{}, tpl)
	w := f.Do("GET", "/api/templates", "", f.Session(apitest.AliceID))
	items := apitest.Decode[struct {
		Items []struct{ Name, MaxSharing string }
	}](t, w).Items
	if len(items) != 1 || items[0].MaxSharing != model.AccessSignedIn {
		t.Fatalf("items %+v", items)
	}
}

// A signed-in person outside every member group can still list (nothing) but
// cannot create.
func TestNonMemberCannotCreate(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Roles: apitest.RolesConfig()}, gatewaytest.Template("streamlit", "streamlit"))
	carol := f.Session("carol-33333333", "/org/visitors")
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Sales","template":"streamlit","file":"app.py"}`, carol)
	if w.Code != http.StatusForbidden {
		t.Fatalf("create %d: %s", w.Code, w.Body)
	}
	if p := apitest.Decode[apitest.Problem](t, w); p.Title != reasons.Msg(reasons.MsgNotAMember) {
		t.Fatalf("title %q", p.Title)
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("created")
	}
	l := f.Do("GET", "/api/workspaces", "", carol)
	if l.Code != http.StatusOK || len(apitest.Decode[struct{ Items []apitest.WorkspaceJSON }](t, l).Items) != 0 {
		t.Fatalf("list %d: %s", l.Code, l.Body)
	}
}

// A platform admin reaches another person's app by naming its owner; anyone
// else naming an owner gets the same 404 as for a missing app.
func TestPlatformAdminManagesOthersApps(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Roles: apitest.RolesConfig(), Sizes: apitest.TestCatalog(t), Logs: podlogs.Clientset{Clientset: apitest.FakeLogs("")}},
		gatewaytest.Workspace("sales-app", apitest.BobID, "Sales app"), gatewaytest.Template("streamlit", "streamlit"))
	alice := f.Session(apitest.AliceID, "javo-admins")
	carol := f.Session("carol-33333333", "/org/members")
	q := "/api/workspaces/sales-app"

	if w := f.Do("GET", q+"?owner="+apitest.BobID, "", alice); w.Code != http.StatusOK {
		t.Fatalf("admin get %d: %s", w.Code, w.Body)
	}
	if w := f.Do("POST", q+"/stop?owner="+apitest.BobID, "", carol); w.Code != http.StatusNotFound {
		t.Fatalf("carol stop %d", w.Code)
	}
	if w := f.Do("POST", q+"/stop?owner="+apitest.BobID, "", alice); w.Code != http.StatusOK {
		t.Fatalf("admin stop %d: %s", w.Code, w.Body)
	}
	if ws := f.GetWorkspace(apitest.BobID, "sales-app"); !ws.Paused {
		t.Fatalf("not stopped: %+v", ws)
	}
	if w := f.Do("PUT", q+"/size?owner="+apitest.BobID, `{"size":"medium"}`, carol); w.Code != http.StatusNotFound {
		t.Fatalf("carol size %d", w.Code)
	}
	if w := f.Do("PUT", q+"/size?owner="+apitest.BobID, `{"size":"medium"}`, alice); w.Code != http.StatusOK {
		t.Fatalf("admin size %d: %s", w.Code, w.Body)
	}
	if got := f.StoredSize(apitest.BobID, "sales-app"); got != "medium" {
		t.Fatalf("size %q", got)
	}
	for _, sub := range []string{"/events", "/logs"} {
		if w := f.Do("GET", q+sub+"?owner="+apitest.BobID, "", carol); w.Code != http.StatusNotFound {
			t.Fatalf("carol %s %d", sub, w.Code)
		}
		if w := f.Do("GET", q+sub+"?owner="+apitest.BobID, "", alice); w.Code != http.StatusOK {
			t.Fatalf("admin %s %d: %s", sub, w.Code, w.Body)
		}
	}
	if w := f.Do("DELETE", q+"?owner="+apitest.BobID, "", carol); w.Code != http.StatusNotFound {
		t.Fatalf("carol delete %d", w.Code)
	}
	if w := f.Do("DELETE", q+"?owner="+apitest.BobID, "", alice); w.Code != http.StatusNoContent {
		t.Fatalf("admin delete %d: %s", w.Code, w.Body)
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("not deleted")
	}
}

// Template admins create, replace and delete templates; anyone else gets
// the 404 of an address with nothing behind it.
func TestTemplateAdminManagesTemplates(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Roles: apitest.RolesConfig()})
	tadmin := f.Session("tara-44444444", "tmpl-admins")
	bob := f.Session(apitest.BobID, "/org/members")

	for _, req := range []struct{ method, body string }{{"PUT", apitest.TemplateBody}, {"DELETE", ""}} {
		w := f.Do(req.method, "/api/templates/lab", req.body, bob)
		if w.Code != http.StatusNotFound {
			t.Fatalf("bob %s %d: %s", req.method, w.Code, w.Body)
		}
		if p := apitest.Decode[apitest.Problem](t, w); p.Title != reasons.Msg(reasons.MsgNoSuchAddress) {
			t.Fatalf("bob %s title %q", req.method, p.Title)
		}
	}
	if _, err := f.Db.Templates().Get(context.Background(), "lab"); err == nil {
		t.Fatal("bob created a template")
	}

	if w := f.Do("PUT", "/api/templates/lab", apitest.TemplateBody, tadmin); w.Code != http.StatusOK {
		t.Fatalf("create %d: %s", w.Code, w.Body)
	}
	replaced := `{"displayName":"Lab 2","kind":"jupyterlab","image":"example.org/lab:2","port":8888}`
	if w := f.Do("PUT", "/api/templates/lab", replaced, tadmin); w.Code != http.StatusOK {
		t.Fatalf("replace %d: %s", w.Code, w.Body)
	}
	got, err := f.Db.Templates().Get(context.Background(), "lab")
	if err != nil || got.Spec.Image != "example.org/lab:2" || got.Source != store.SourceAdmin {
		t.Fatalf("after replace: %v %+v", err, got)
	}
	if w := f.Do("PUT", "/api/templates/lab", `{"displayName":"x","nope":1}`, tadmin); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown field %d: %s", w.Code, w.Body)
	}
	if w := f.Do("DELETE", "/api/templates/lab", "", tadmin); w.Code != http.StatusNoContent {
		t.Fatalf("delete %d: %s", w.Code, w.Body)
	}
	if _, err := f.Db.Templates().Get(context.Background(), "lab"); err == nil {
		t.Fatal("not deleted")
	}
}

// Replacing a template changes what runs in every app on it, so the audit
// row names the fields that changed, and who changed them.
func TestReplacingATemplateAuditsWhatChanged(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Roles: apitest.RolesConfig()})
	tadmin := f.Session("tara-44444444", "tmpl-admins")
	if w := f.Do("PUT", "/api/templates/lab", apitest.TemplateBody, tadmin); w.Code != http.StatusOK {
		t.Fatalf("create %d: %s", w.Code, w.Body)
	}
	before, err := f.Db.Templates().Get(context.Background(), "lab")
	if err != nil {
		t.Fatal(err)
	}
	next := before.Spec
	next.Image = "example.org/other:1"
	body, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	if w := f.Do("PUT", "/api/templates/lab", string(body), tadmin); w.Code != http.StatusOK {
		t.Fatalf("replace %d: %s", w.Code, w.Body)
	}
	events, err := f.Db.Audit().List(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for _, e := range events {
		if e.Action == "template.put" {
			if e.Actor != "tara-44444444" {
				t.Fatalf("actor %q", e.Actor)
			}
			details = append(details, string(e.Detail))
		}
	}
	if len(details) != 2 || !slices.Contains(details, `{"changed":["image"]}`) {
		t.Fatalf("template.put details %q", details)
	}
}

// A template that comes with the installation is changed in the Helm values:
// replacing or deleting it through the API is a 409 that says so, and
// changes nothing. An administrator's template of another name is unaffected.
func TestBuiltinTemplateReadOnly(t *testing.T) {
	t.Parallel()
	builtin := gatewaytest.Template("lab", "jupyterlab")
	builtin.Source = store.SourceBuiltin
	f := apitest.New(t, apitest.Options{Roles: apitest.RolesConfig()}, builtin)
	tadmin := f.Session("tara-44444444", "tmpl-admins")
	for _, req := range []struct{ method, body string }{{"PUT", apitest.TemplateBody}, {"DELETE", ""}} {
		w := f.Do(req.method, "/api/templates/lab", req.body, tadmin)
		if w.Code != http.StatusConflict {
			t.Fatalf("%s %d: %s", req.method, w.Code, w.Body)
		}
		if p := apitest.Decode[apitest.Problem](t, w); p.Title != reasons.Msg(reasons.MsgTemplateManagedByHelm) {
			t.Fatalf("%s title %q", req.method, p.Title)
		}
	}
	got, err := f.Db.Templates().Get(context.Background(), "lab")
	if err != nil || got.Source != store.SourceBuiltin || got.Spec.Image != "example.org/lab:1" || got.Version != 1 {
		t.Fatalf("builtin changed: %v %+v", err, got)
	}
	if w := f.Do("PUT", "/api/templates/mine", apitest.TemplateBody, tadmin); w.Code != http.StatusOK {
		t.Fatalf("own template: %d %s", w.Code, w.Body)
	}
}

// A template name goes into the address of every app and job made from it:
// one that is not a lowercase DNS-1123 name is refused with catalog text and
// nothing is stored.
func TestTemplateWithAnInvalidNameIsRefused(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Roles: apitest.RolesConfig()})
	tadmin := f.Session("tara-44444444", "tmpl-admins")
	for _, name := range []string{"My%20Lab", "Lab", strings.Repeat("a", 254)} {
		w := f.Do("PUT", "/api/templates/"+name, apitest.TemplateBody, tadmin)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		if msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("path.name"); !ok || msg != reasons.Msg(reasons.MsgTemplateNameInvalid) {
			t.Fatalf("%s: %s", name, w.Body)
		}
	}
	if l, err := f.Db.Templates().List(context.Background()); err != nil || len(l) != 0 {
		t.Fatalf("stored %+v, %v", l, err)
	}
}

func TestMeHasDisplayNameAndRoles(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Roles: apitest.RolesConfig()})
	tok, err := f.Store.Create(context.Background(), apitest.SessionFor(apitest.AliceID, "Alice Example", "javo-admins"))
	if err != nil {
		t.Fatal(err)
	}
	w := f.Do("GET", "/api/me", "", f.Cookie(tok))
	me := apitest.Decode[struct {
		DisplayName string
		Roles       []string
	}](t, w)
	if me.DisplayName != "Alice Example" || !slices.Contains(me.Roles, "platform-admin") {
		t.Fatalf("me %s", w.Body)
	}
}

// Changes in a row do not turn into conflicts: each is built on the app as
// the last one left it.
func TestStopStartStopInARowDoNotConflict(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{},
		gatewaytest.Workspace("sales-app", apitest.AliceID, "Sales app"), gatewaytest.Template("streamlit", "streamlit"))
	alice := f.Session(apitest.AliceID)
	for _, act := range []string{"stop", "start", "stop"} {
		if w := f.Do("POST", "/api/workspaces/sales-app/"+act, "", alice); w.Code != http.StatusOK {
			t.Fatalf("%s %d: %s", act, w.Code, w.Body)
		}
	}
}

package apps_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// A private repository's token is one of the owner's secrets, named in the
// request; its value is in no answer, log line or audit row.
func TestRepoTokenComesFromASecret(t *testing.T) {
	logs := apitest.CaptureLog(t)
	f := apitest.New(t, apitest.Options{}, apitest.RepoTemplates()...)
	f.WithSecretKeys()
	alice, bob := f.Session(apitest.AliceID), f.Session(apitest.BobID)
	f.CreateSecret(alice, "GH_TOKEN", apitest.TestRepoToken)

	w := f.Do("POST", "/api/workspaces", apitest.CreatePrivate, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	created := apitest.Decode[apitest.RepoWorkspaceJSON](t, w)
	if created.Source == nil || created.Source.URL != apitest.PrivateRepo || created.Source.Path != "web" || !created.Source.HasToken || created.Source.TokenSecret != "GH_TOKEN" {
		t.Fatalf("source view %+v", created.Source)
	}
	app := f.GetWorkspace(apitest.AliceID, "private")
	if name, err := f.Db.Secrets().GitTokenSecret(context.Background(), app.ID); err != nil || name != "GH_TOKEN" {
		t.Fatalf("stored token secret %q, %v", name, err)
	}
	if _, _, has, _ := f.Db.GitTokens().Pending(context.Background(), app.ID); has {
		t.Fatal("a pasted token row was made")
	}
	// A secret the caller does not have is refused, also bob naming alice's.
	for _, c := range []*http.Cookie{alice, bob} {
		w := f.Do("POST", "/api/workspaces", strings.Replace(strings.Replace(apitest.CreatePrivate, "GH_TOKEN", "NOPE", 1), "Private", "Other", 1), c)
		if msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.source.tokenSecret"); w.Code != http.StatusUnprocessableEntity || !ok || msg != reasons.Msg(reasons.MsgSecretUnknown, "NOPE") {
			t.Fatalf("unknown secret: %d %s", w.Code, w.Body)
		}
	}
	if w := f.Do("POST", "/api/workspaces", strings.Replace(apitest.CreatePrivate, "Private", "Bobs", 1), bob); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bob names alice's secret: %d %s", w.Code, w.Body)
	}
	outputs := map[string]string{
		"create": w.Body.String(),
		"get":    f.Do("GET", "/api/workspaces/private", "", alice).Body.String(),
		"list":   f.Do("GET", "/api/workspaces", "", alice).Body.String(),
		"export": f.Do("GET", "/api/workspaces/export", "", alice).Body.String(),
		"log":    logs.String(),
	}
	audit, err := f.Db.Audit().List(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(audit)
	outputs["audit"] = string(raw)
	for what, out := range outputs {
		if strings.Contains(out, apitest.TestRepoToken) {
			t.Errorf("%s contains the token: %s", what, out)
		}
	}
}

func TestCreateFromRepoRefusesWhatTheModelRefuses(t *testing.T) {
	f := apitest.New(t, apitest.Options{}, apitest.RepoTemplates()...)
	alice := f.Session(apitest.AliceID)
	cases := []struct {
		name, body, field string
		want              reasons.MessageCode
	}{
		{"http url", `{"displayName":"A","template":"streamlit","file":"app.py","source":{"url":"http://github.com/example-org/site"}}`, "body.source.url", reasons.MsgGitURLNotHTTPS},
		{"credentials", `{"displayName":"A","template":"streamlit","file":"app.py","source":{"url":"https://alice:pw@github.com/example-org/site"}}`, "body.source.url", reasons.MsgGitURLHasCredentials},
		{"no url", `{"displayName":"A","template":"streamlit","file":"app.py","source":{}}`, "body.source.url", reasons.MsgGitURLInvalid},
		{"path out of the repo", `{"displayName":"A","template":"streamlit","file":"app.py","source":{"url":"https://github.com/example-org/site","path":"../x"}}`, "body.source.path", reasons.MsgSourcePathInvalid},
		{"template needs a repo", `{"displayName":"A","template":"backend-node","command":"node server.js"}`, "body.source", reasons.MsgTemplateNeedsRepo},
		{"template takes no repo", `{"displayName":"A","template":"lab","source":{"url":"https://github.com/example-org/site"}}`, "body.source", reasons.MsgTemplateNoRepo},
	}
	for _, c := range cases {
		w := f.Do("POST", "/api/workspaces", strings.ReplaceAll(c.body, "\n", `\n`), alice)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d %s", c.name, w.Code, w.Body)
		}
		got, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage(c.field)
		if !ok || got != reasons.Msg(c.want) {
			t.Fatalf("%s: field %s = %q (%v), want %q; body %s", c.name, c.field, got, ok, reasons.Msg(c.want), w.Body)
		}
	}
	if n := f.WorkspaceCount(); n != 0 {
		t.Fatalf("%d apps were created by refused requests", n)
	}
}

func TestCreateFromRepoSkipsTheHomeFileRules(t *testing.T) {
	f := apitest.New(t, apitest.Options{}, apitest.RepoTemplates()...)
	alice := f.Session(apitest.AliceID)
	// A static site from a repo has no file; from the home it needs an .html one.
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Site","template":"static-site","source":{"url":"https://github.com/example-org/site"}}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("static from repo: %d %s", w.Code, w.Body)
	}
	w = f.Do("POST", "/api/workspaces", `{"displayName":"Home site","template":"static-site"}`, alice)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("static from home without a file: %d %s", w.Code, w.Body)
	}
	w = f.Do("POST", "/api/workspaces", `{"displayName":"Open","template":"streamlit","source":{"url":"https://github.com/example-org/site"}}`, alice)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("streamlit from repo without a file: %d %s", w.Code, w.Body)
	}
	if msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.file"); !ok || msg != reasons.Msg(reasons.MsgFileWrongType, ".py") {
		t.Fatalf("file message %q (%v), body %s", msg, ok, w.Body)
	}
	w = f.Do("POST", "/api/workspaces", `{"displayName":"Samp","template":"streamlit","sample":true,"source":{"url":"https://github.com/example-org/site"}}`, alice)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("sample with a source: %d %s", w.Code, w.Body)
	}
}

func TestCreateFromRepoWithoutTokenStoresNoTokenRow(t *testing.T) {
	f := apitest.New(t, apitest.Options{}, apitest.RepoTemplates()...)
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Open","template":"streamlit","file":"app.py","source":{"url":"https://github.com/example-org/site"}}`, f.Session(apitest.AliceID))
	if w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := apitest.Decode[apitest.RepoWorkspaceJSON](t, w); got.Source == nil || got.Source.HasToken {
		t.Fatalf("source %+v", got.Source)
	}
	if name, _ := f.Db.Secrets().GitTokenSecret(context.Background(), f.GetWorkspace(apitest.AliceID, "open").ID); name != "" {
		t.Fatalf("the app got token secret %q though none was named", name)
	}
}

func TestSourceViewLinksToTheCommitAndSaysWhenPreviewsAreAvailable(t *testing.T) {
	app := apitest.RepoApp("site")
	app.Status = model.AppStatus{Phase: "Running", Commit: "0123456789abcdef"}
	other := apitest.RepoApp("elsewhere")
	other.Source.URL = "https://git.example.org/example-org/site"
	plain := gatewaytest.Workspace("plain", apitest.AliceID, "Plain")
	for _, c := range []struct {
		name     string
		previews bool
	}{{"on", true}, {"off", false}} {
		f := apitest.New(t, apitest.Options{Previews: c.previews}, gatewaytest.Template("streamlit", "streamlit"), app, other, plain)
		alice := f.Session(apitest.AliceID)
		got := apitest.Decode[apitest.RepoWorkspaceJSON](t, f.Do("GET", "/api/workspaces/site", "", alice))
		if got.Source == nil || got.Source.Commit != "0123456789abcdef" ||
			got.Source.WebURL != "https://github.com/example-org/site/commit/0123456789abcdef" || got.Source.PreviewsAvailable != c.previews {
			t.Fatalf("previews %s: source %+v", c.name, got.Source)
		}
		el := apitest.Decode[apitest.RepoWorkspaceJSON](t, f.Do("GET", "/api/workspaces/elsewhere", "", alice))
		if el.Source == nil || el.Source.WebURL != "" || el.Source.PreviewsAvailable {
			t.Fatalf("previews %s: a repo outside github.com has source %+v", c.name, el.Source)
		}
		if p := apitest.Decode[apitest.RepoWorkspaceJSON](t, f.Do("GET", "/api/workspaces/plain", "", alice)); p.Source != nil || p.PreviewOf != nil {
			t.Fatalf("an app from the home has source %+v", p.Source)
		}
	}
}

func TestPutAndDeleteTheRepoToken(t *testing.T) {
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), apitest.RepoApp("site"), gatewaytest.Workspace("plain", apitest.AliceID, "Plain"))
	f.WithSecretKeys()
	alice := f.Session(apitest.AliceID)
	f.CreateSecret(alice, "GH_TOKEN", apitest.TestRepoToken)
	id := f.GetWorkspace(apitest.AliceID, "site").ID
	tokenSecret := func() string {
		name, err := f.Db.Secrets().GitTokenSecret(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		return name
	}

	if w := f.Do("PUT", "/api/workspaces/site/source/token", `{"secret":"GH_TOKEN"}`, alice); w.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	if got := tokenSecret(); got != "GH_TOKEN" {
		t.Fatalf("after put: %q", got)
	}
	if got := apitest.Decode[apitest.RepoWorkspaceJSON](t, f.Do("GET", "/api/workspaces/site", "", alice)); !got.Source.HasToken || got.Source.TokenSecret != "GH_TOKEN" {
		t.Fatalf("source %+v", got.Source)
	}
	w := f.Do("PUT", "/api/workspaces/site/source/token", `{"secret":"NOPE"}`, alice)
	if msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.secret"); w.Code != http.StatusUnprocessableEntity || !ok || msg != reasons.Msg(reasons.MsgSecretUnknown, "NOPE") {
		t.Fatalf("put unknown: %d %s", w.Code, w.Body)
	}
	if w := f.Do("PUT", "/api/workspaces/site/source/token", `{}`, alice); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("put without a secret: %d %s", w.Code, w.Body)
	}
	if w := f.Do("DELETE", "/api/workspaces/site/source/token", "", alice); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if got := tokenSecret(); got != "" {
		t.Fatalf("the token is still %q after delete", got)
	}
	if w := f.Do("DELETE", "/api/workspaces/site/source/token", "", alice); w.Code != http.StatusNoContent {
		t.Fatalf("delete again: %d %s", w.Code, w.Body)
	}
	// An app from the home has no repository to hold a token for.
	w = f.Do("PUT", "/api/workspaces/plain/source/token", `{"secret":"GH_TOKEN"}`, alice)
	if msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.source"); w.Code != http.StatusUnprocessableEntity || !ok || msg != reasons.Msg(reasons.MsgTemplateNoRepo) {
		t.Fatalf("put on a home app: %d %s", w.Code, w.Body)
	}
}

// Only the owner picks the token, from their own secrets: bob cannot reach
// alice's app, and a platform admin who manages it is refused. (An app's own
// API token is refused by authn.NoAppTokens, as for its variables.)
func TestOnlyTheOwnerPicksTheRepoToken(t *testing.T) {
	ws := apitest.RepoApp("site")
	ws.Owner = apitest.LocalAlice()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), ws)
	f.WithSecretKeys()
	c := f.LoginAlice()
	f.CreateSecret(c, "GH_TOKEN", apitest.TestRepoToken)
	bob := f.Session(apitest.BobID)
	admin := f.Session("root-cccccccc", "javo-admins")
	for _, path := range []string{"/api/workspaces/site/source/token", "/api/workspaces/site/source/token?owner=" + apitest.LocalAlice()} {
		if w := f.Do("PUT", path, `{"secret":"GH_TOKEN"}`, bob); w.Code != http.StatusNotFound {
			t.Fatalf("bob PUT %s: %d %s", path, w.Code, w.Body)
		}
		if w := f.Do("DELETE", path, "", bob); w.Code != http.StatusNotFound {
			t.Fatalf("bob DELETE %s: %d %s", path, w.Code, w.Body)
		}
	}
	w := f.Do("PUT", "/api/workspaces/site/source/token?owner="+apitest.LocalAlice(), `{"secret":"GH_TOKEN"}`, admin)
	if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgSecretsOwnerOnly) {
		t.Fatalf("admin PUT: %d %s", w.Code, w.Body)
	}
	if name, _ := f.Db.Secrets().GitTokenSecret(context.Background(), f.GetWorkspace(apitest.LocalAlice(), "site").ID); name != "" {
		t.Fatalf("someone set the token to %q", name)
	}
}

func TestRedeployAsksForOneRestart(t *testing.T) {
	site := gatewaytest.Workspace("page", apitest.AliceID, "Page")
	site.Template, site.File = "static-site", "index.html"
	homeOnly := gatewaytest.Workspace("plain", apitest.AliceID, "Plain")
	f := apitest.New(t, apitest.Options{}, append(apitest.RepoTemplates(), apitest.RepoApp("site"), site, homeOnly)...)
	alice := f.Session(apitest.AliceID)

	for _, name := range []string{"site", "page"} {
		before := f.GetWorkspace(apitest.AliceID, name)
		w := f.Do("POST", "/api/workspaces/"+name+"/redeploy", "", alice)
		if w.Code != http.StatusOK || apitest.Decode[apitest.WorkspaceJSON](t, w).Name != name {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		after := f.GetWorkspace(apitest.AliceID, name)
		if time.Since(after.RestartRequestedAt) > time.Minute || after.RestartRequestedAt.IsZero() || after.Version != before.Version+1 {
			t.Fatalf("%s: restartRequestedAt %v, version %d -> %d", name, after.RestartRequestedAt, before.Version, after.Version)
		}
	}
	w := f.Do("POST", "/api/workspaces/plain/redeploy", "", alice)
	if w.Code != http.StatusUnprocessableEntity || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgNothingToRedeploy) {
		t.Fatalf("home app: %d %s", w.Code, w.Body)
	}
	if !f.GetWorkspace(apitest.AliceID, "plain").RestartRequestedAt.IsZero() {
		t.Fatal("a refused redeploy still set a restart")
	}
	if w := f.Do("POST", "/api/workspaces/site/redeploy", "", f.Session(apitest.BobID)); w.Code != http.StatusNotFound {
		t.Fatalf("bob: %d %s", w.Code, w.Body)
	}
}

func TestWorkspaceSaysWhetherItCanBeRedeployed(t *testing.T) {
	page := gatewaytest.Workspace("page", apitest.AliceID, "Page")
	page.Template, page.File = "static-site", "index.html"
	homeOnly := gatewaytest.Workspace("plain", apitest.AliceID, "Plain")
	f := apitest.New(t, apitest.Options{}, append(apitest.RepoTemplates(), apitest.RepoApp("site"), page, homeOnly)...)
	alice := f.Session(apitest.AliceID)
	for name, want := range map[string]bool{"site": true, "page": true, "plain": false} {
		got := apitest.Decode[struct{ Redeployable bool }](t, f.Do("GET", "/api/workspaces/"+name, "", alice)).Redeployable
		code := f.Do("POST", "/api/workspaces/"+name+"/redeploy", "", alice).Code
		if got != want || (code == http.StatusOK) != want {
			t.Errorf("%s: redeployable %v, redeploy answered %d, want %v", name, got, code, want)
		}
	}
}

func TestReadTokenCannotRedeploy(t *testing.T) {
	app := apitest.RepoApp("mine")
	app.Owner = apitest.LocalAlice()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), app)
	c := f.LoginAlice()
	read := f.NewToken(c, `{"name":"ci","scope":"read"}`).Token
	w := f.Bearer("POST", "/api/workspaces/mine/redeploy", "", read, nil)
	if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgTokenReadOnly) {
		t.Fatalf("read token: %d %s", w.Code, w.Body)
	}
	if !f.GetWorkspace(apitest.LocalAlice(), "mine").RestartRequestedAt.IsZero() {
		t.Fatal("a read token set a restart")
	}
	write := f.NewToken(c, `{"name":"ci2","scope":"read-write"}`).Token
	if w := f.Bearer("POST", "/api/workspaces/mine/redeploy", "", write, nil); w.Code != http.StatusOK {
		t.Fatalf("write token: %d %s", w.Code, w.Body)
	}
}

func TestPreviewsAreHiddenUnlessAsked(t *testing.T) {
	parent := apitest.RepoApp("site")
	parent.Sharing = model.Sharing{Access: model.AccessRestricted, Users: []string{apitest.BobID}}
	tpl := gatewaytest.Template("streamlit", "streamlit")
	tpl.Spec.MaxSharing = model.AccessSignedIn
	now := time.Now()
	f := apitest.New(t, apitest.Options{Urls: apitest.SeparateHosts, People: []apitest.TestPerson{apitest.SignedIn("alice", now), apitest.SignedIn("bob", now)}}, tpl, parent)
	child := f.PreviewChild(parent, 7)
	alice, bob := f.Session(apitest.AliceID), f.Session(apitest.BobID)

	Names := func(w *bytes.Buffer) []string {
		var l struct{ Items []struct{ Name string } }
		if err := json.Unmarshal(w.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, i := range l.Items {
			out = append(out, i.Name)
		}
		return out
	}
	for _, path := range []string{"/api/workspaces", "/api/shared"} {
		who := alice
		if path == "/api/shared" {
			who = bob
		}
		if got := Names(f.Do("GET", path, "", who).Body); strings.Join(got, ",") != "site" {
			t.Fatalf("GET %s = %v, want the parent only", path, got)
		}
		got := Names(f.Do("GET", path+"?previews=true", "", who).Body)
		if len(got) != 2 || !(got[0] == child.Name || got[1] == child.Name) {
			t.Fatalf("GET %s?previews=true = %v, want the parent and %s", path, got, child.Name)
		}
	}
	w := f.Do("GET", "/api/workspaces/"+child.Name, "", alice)
	got := apitest.Decode[apitest.RepoWorkspaceJSON](t, w)
	if w.Code != http.StatusOK || got.PreviewOf == nil || got.PreviewOf.Name != "site" || got.PreviewOf.PullNumber != 7 {
		t.Fatalf("preview view: %d %s", w.Code, w.Body)
	}
}

func TestPreviewSpecIsManagedByTheParent(t *testing.T) {
	parent := apitest.RepoApp("site")
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), parent)
	child := f.PreviewChild(parent, 7)
	alice := f.Session(apitest.AliceID)
	base := "/api/workspaces/" + child.Name

	refused := []struct{ method, path, body string }{
		{"DELETE", base, ""},
		{"PUT", base + "/launch", `{}`},
		{"PUT", base + "/size", `{"size":"small"}`},
		{"PUT", base + "/api-access", `{"apiAccess":"None"}`},
		{"PUT", base + "/sharing", `{"access":"Private","users":[],"groups":[]}`},
		{"PUT", base + "/source/token", `{"secret":"GH_TOKEN"}`},
		{"DELETE", base + "/source/token", ""},
	}
	for _, r := range refused {
		w := f.Do(r.method, r.path, r.body, alice)
		if w.Code != http.StatusConflict || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgPreviewManaged) {
			t.Errorf("%s %s: %d %s", r.method, r.path, w.Code, w.Body)
		}
	}
	for _, verb := range []string{"stop", "start", "redeploy"} {
		if w := f.Do("POST", base+"/"+verb, "", alice); w.Code != http.StatusOK {
			t.Errorf("%s: %d %s", verb, w.Code, w.Body)
		}
	}
	if !f.GetWorkspace(apitest.AliceID, child.Name).DeletedAt.IsZero() {
		t.Fatal("the refused delete marked the preview deleted")
	}
}

func TestTemplateViewSaysWhetherItTakesARepoAndItsBuild(t *testing.T) {
	f := apitest.New(t, apitest.Options{}, apitest.RepoTemplates()...)
	list := apitest.Decode[struct {
		Items []struct {
			Name     string
			FromRepo string
			Build    *struct{ Command, Output, IfFile string }
		}
	}](t, f.Do("GET", "/api/templates", "", f.Session(apitest.AliceID)))
	got := map[string]string{}
	for _, tpl := range list.Items {
		got[tpl.Name] = tpl.FromRepo
		switch {
		case tpl.Name == "static-site" && (tpl.Build == nil || tpl.Build.Command != "npm ci && npm run build" || tpl.Build.Output != "dist" || tpl.Build.IfFile != "package.json"):
			t.Errorf("static-site build %+v", tpl.Build)
		case tpl.Name != "static-site" && tpl.Build != nil:
			t.Errorf("%s has build %+v", tpl.Name, tpl.Build)
		}
	}
	want := map[string]string{"streamlit": "Optional", "backend-node": "Required", "static-site": "Optional", "lab": "None"}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s fromRepo = %q, want %q", name, got[name], w)
		}
	}
}

func TestPreviewSettingsAreReadAndSavedOnTheApp(t *testing.T) {
	f := apitest.New(t, apitest.Options{Previews: true}, gatewaytest.Template("streamlit", "streamlit"), apitest.RepoApp("site"), gatewaytest.Workspace("plain", apitest.AliceID, "Plain"))
	alice := f.Session(apitest.AliceID)

	got := apitest.Decode[apitest.PreviewsJSON](t, f.Do("GET", "/api/workspaces/site/previews", "", alice))
	if got.Enabled || got.Max != model.DefaultPreviewMax || got.Forks || len(got.Items) != 0 || got.SyncedAt != "" || got.ForksWarning == "" {
		t.Fatalf("before any setting: %+v", got)
	}
	w := f.Do("PUT", "/api/workspaces/site/previews", `{"enabled":true,"max":2,"forks":true}`, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	if p := f.GetWorkspace(apitest.AliceID, "site").Source.Previews; p == nil || !p.Enabled || p.Max != 2 || !p.Forks {
		t.Fatalf("stored %+v", p)
	}
	if got := apitest.Decode[apitest.PreviewsJSON](t, f.Do("GET", "/api/workspaces/site/previews", "", alice)); !got.Enabled || got.Max != 2 || !got.Forks {
		t.Fatalf("after: %+v", got)
	}
	// Off keeps the other two settings for the next time.
	f.Do("PUT", "/api/workspaces/site/previews", `{"enabled":false,"max":2,"forks":true}`, alice)
	if got := apitest.Decode[apitest.PreviewsJSON](t, f.Do("GET", "/api/workspaces/site/previews", "", alice)); got.Enabled || got.Max != 2 || !got.Forks {
		t.Fatalf("off: %+v", got)
	}
}

func TestPreviewSettingsRefuseWhatCannotWork(t *testing.T) {
	elsewhere := apitest.RepoApp("elsewhere")
	elsewhere.Source.URL = "https://git.example.org/example-org/site"
	f := apitest.New(t, apitest.Options{Previews: true}, gatewaytest.Template("streamlit", "streamlit"), apitest.RepoApp("site"), elsewhere, gatewaytest.Workspace("plain", apitest.AliceID, "Plain"))
	alice := f.Session(apitest.AliceID)
	child := f.PreviewChild(f.GetWorkspace(apitest.AliceID, "site"), 7)

	cases := []struct {
		name, path, body string
		who              *http.Cookie
		code             int
		field, msg       string
	}{
		{"max too high", "site", `{"enabled":true,"max":11}`, alice, 422, "max", reasons.Msg(reasons.MsgPreviewsTooMany, model.MaxPreviewMax)},
		{"max zero", "site", `{"enabled":true,"max":0}`, alice, 422, "max", reasons.Msg(reasons.MsgPreviewsTooMany, model.MaxPreviewMax)},
		{"not github", "elsewhere", `{"enabled":true,"max":3}`, alice, 422, "source", reasons.Msg(reasons.MsgPreviewsNeedGitHub)},
		{"no repo", "plain", `{"enabled":true,"max":3}`, alice, 422, "source", reasons.Msg(reasons.MsgPreviewsNeedGitHub)},
		{"bob", "site", `{"enabled":true,"max":3}`, f.Session(apitest.BobID), 404, "", ""},
		{"a preview", child.Name, `{"enabled":true,"max":3}`, alice, 409, "", reasons.Msg(reasons.MsgPreviewManaged)},
	}
	for _, c := range cases {
		w := f.Do("PUT", "/api/workspaces/"+c.path+"/previews", c.body, c.who)
		p := apitest.Decode[apitest.Problem](t, w)
		if w.Code != c.code || (c.field != "" && (len(p.Errors) != 1 || p.Errors[0].Location != "body."+c.field || p.Errors[0].Message != c.msg)) ||
			(c.field == "" && c.msg != "" && p.Title != c.msg) {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
	if p := f.GetWorkspace(apitest.AliceID, "site").Source.Previews; p != nil {
		t.Fatalf("a refused change saved %+v", p)
	}
	if w := f.Do("GET", "/api/workspaces/site/previews", "", f.Session(apitest.BobID)); w.Code != http.StatusNotFound {
		t.Fatalf("bob reads: %d", w.Code)
	}
}

func TestPreviewLimitFollowsThePlatformCap(t *testing.T) {
	f := apitest.New(t, apitest.Options{Previews: true, PreviewsMax: 3}, gatewaytest.Template("streamlit", "streamlit"), apitest.RepoApp("site"))
	alice := f.Session(apitest.AliceID)
	if got := apitest.Decode[apitest.PreviewsJSON](t, f.Do("GET", "/api/workspaces/site/previews", "", alice)); got.MaxAllowed != 3 {
		t.Fatalf("maxAllowed with cap 3: %+v", got)
	}
	w := f.Do("PUT", "/api/workspaces/site/previews", `{"enabled":true,"max":4}`, alice)
	p := apitest.Decode[apitest.Problem](t, w)
	if w.Code != 422 || len(p.Errors) != 1 || p.Errors[0].Location != "body.max" || p.Errors[0].Message != reasons.Msg(reasons.MsgPreviewsTooMany, 3) {
		t.Fatalf("max 4 under cap 3: %d %s", w.Code, w.Body)
	}
	if w := f.Do("PUT", "/api/workspaces/site/previews", `{"enabled":true,"max":3}`, alice); w.Code != http.StatusOK {
		t.Fatalf("max 3 under cap 3: %d %s", w.Code, w.Body)
	}
	g := apitest.New(t, apitest.Options{Previews: true}, gatewaytest.Template("streamlit", "streamlit"), apitest.RepoApp("site"))
	if got := apitest.Decode[apitest.PreviewsJSON](t, g.Do("GET", "/api/workspaces/site/previews", "", g.Session(apitest.AliceID))); got.MaxAllowed != model.MaxPreviewMax {
		t.Fatalf("maxAllowed without a cap: %+v", got)
	}
}

func TestReportedPreviewLimitNeverExceedsThePlatformCap(t *testing.T) {
	lowered := apitest.RepoApp("old")
	lowered.Source.Previews = &model.Previews{Max: 3}
	f := apitest.New(t, apitest.Options{Previews: true, PreviewsMax: 2}, gatewaytest.Template("streamlit", "streamlit"), apitest.RepoApp("site"), lowered)
	alice := f.Session(apitest.AliceID)
	for _, name := range []string{"site", "old"} {
		got := apitest.Decode[apitest.PreviewsJSON](t, f.Do("GET", "/api/workspaces/"+name+"/previews", "", alice))
		if got.MaxAllowed != 2 || got.Max != 2 {
			t.Fatalf("%s under cap 2: %+v", name, got)
		}
		body := fmt.Sprintf(`{"enabled":true,"max":%d}`, got.Max)
		if w := f.Do("PUT", "/api/workspaces/"+name+"/previews", body, alice); w.Code != http.StatusOK {
			t.Fatalf("%s turning previews on with the reported max: %d %s", name, w.Code, w.Body)
		}
	}
}

func TestPreviewSyncResultCarriesTheCatalogsWords(t *testing.T) {
	parent := apitest.RepoApp("site")
	parent.Source.Previews = &model.Previews{Enabled: true, Max: 1}
	f := apitest.New(t, apitest.Options{Previews: true}, gatewaytest.Template("streamlit", "streamlit"), parent)
	p := f.GetWorkspace(apitest.AliceID, "site")
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	err := f.Db.Previews().RecordSync(context.Background(), p.ID, model.PreviewSync{Items: []model.PreviewItem{
		{Number: 12, Title: "Add a chart", HeadSHA: "abc123", App: "site--pr-12"},
		{Number: 9, Title: "Old idea", Reason: string(reasons.PreviewLimitReached)},
	}}, at)
	if err != nil {
		t.Fatal(err)
	}
	got := apitest.Decode[apitest.PreviewsJSON](t, f.Do("GET", "/api/workspaces/site/previews", "", f.Session(apitest.AliceID)))
	limit, _ := reasons.Lookup(reasons.PreviewLimitReached)
	if got.SyncedAt != "2026-10-31T09:00:00Z" || len(got.Items) != 2 || got.Items[0].Number != 12 || got.Items[0].Text != "" ||
		got.Items[1].Reason != "PreviewLimitReached" || got.Items[1].Text != limit.What {
		t.Fatalf("%+v", got)
	}
	err = f.Db.Previews().RecordSync(context.Background(), p.ID, model.PreviewSync{Reason: string(reasons.PreviewRateLimited), Message: "Try again at 10:00."}, at)
	if err != nil {
		t.Fatal(err)
	}
	rl, _ := reasons.Lookup(reasons.PreviewRateLimited)
	got = apitest.Decode[apitest.PreviewsJSON](t, f.Do("GET", "/api/workspaces/site/previews", "", f.Session(apitest.AliceID)))
	if got.Title != rl.Title || got.Todo != rl.Todo {
		t.Fatalf("title %q todo %q, want the catalog's %q %q", got.Title, got.Todo, rl.Title, rl.Todo)
	}
	if got.Reason != "PreviewRateLimited" || !strings.Contains(got.Text, rl.What) || !strings.Contains(got.Text, "Try again at 10:00.") {
		t.Fatalf("%+v", got)
	}
}

package authn_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// bobsSales is a running app of someone else than alice, the platform admin.
func bobsSales() *model.App { return apitest.RunningSalesOf("bob-5e6f7a8b") }

// alicesAgent seeds an app of alice on the agent template and returns a
// read-write token made for it, as the controller makes one.
func alicesAgent(t *testing.T, f *apitest.Fixture) string {
	t.Helper()
	ws := gatewaytest.Workspace("agent", apitest.LocalAlice(), "Agent")
	ws.Template = "agent"
	gatewaytest.Seed(t, f.Db, []any{ws})
	text, _, err := f.Tokens.Create(context.Background(), model.Actor{System: model.SystemController}, token.Token{Name: "agent", Owner: apitest.LocalAlice(),
		Scope: token.ScopeReadWrite, App: &token.App{Namespace: ws.Namespace, Name: ws.ObjectName(), Short: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// The token of a platform admin's coding-agent app acts as a member: code in
// the app (a prompt injection, a dependency) cannot use the admin console or
// act on other people's apps.
func TestAnAdminsAppTokenIsOnlyAMember(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.AgentTemplate(), bobsSales())
	f.LoginAlice()
	text := alicesAgent(t, f)
	if w := f.Bearer("GET", "/api/admin/apps", "", text, nil); w.Code != http.StatusForbidden {
		t.Fatalf("admin console with the app token: %d %s", w.Code, w.Body)
	}
	if w := f.Bearer("POST", "/api/workspaces/sales/stop?owner=bob-5e6f7a8b", "", text, nil); w.Code != http.StatusNotFound {
		t.Fatalf("stopping bob's app with the app token: %d %s", w.Code, w.Body)
	}
	me := apitest.Decode[struct{ Roles []string }](t, f.Bearer("GET", "/api/me", "", text, nil))
	if !slices.Equal(me.Roles, []string{"member"}) {
		t.Fatalf("roles of the app token: %v", me.Roles)
	}
	// The app's own work still goes through.
	if w := f.Bearer("GET", "/api/workspaces", "", text, nil); w.Code != http.StatusOK {
		t.Fatalf("list own apps: %d %s", w.Code, w.Body)
	}
}

// A read-write personal token of an admin is a member too.
func TestAnAdminsReadWriteTokenIsOnlyAMember(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), bobsSales())
	tok := f.NewToken(f.LoginAlice(), `{"name":"ci","scope":"read-write"}`).Token
	if w := f.Bearer("GET", "/api/admin/apps", "", tok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("admin console with a read-write token: %d %s", w.Code, w.Body)
	}
	if w := f.Bearer("POST", "/api/workspaces/sales/stop?owner=bob-5e6f7a8b", "", tok, nil); w.Code != http.StatusNotFound {
		t.Fatalf("stopping bob's app with a read-write token: %d %s", w.Code, w.Body)
	}
}

// An admin token is one an admin asks for by name, with an expiry of at most
// 30 days; it keeps the admin's roles.
func TestAnAdminTokenKeepsTheAdminRoles(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, bobsSales())
	c := f.LoginAlice()
	if w := f.Do("POST", "/api/tokens", `{"name":"ops","scope":"admin"}`, c); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("admin token without an expiry: %d %s", w.Code, w.Body)
	}
	if w := f.Do("POST", "/api/tokens", `{"name":"ops","scope":"admin","expiresInDays":31}`, c); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("admin token for 31 days: %d %s", w.Code, w.Body)
	}
	tok := f.NewToken(c, `{"name":"ops","scope":"admin","expiresInDays":7}`)
	if tok.Scope != "admin" {
		t.Fatalf("scope %q", tok.Scope)
	}
	if w := f.Bearer("GET", "/api/admin/apps", "", tok.Token, nil); w.Code != http.StatusOK {
		t.Fatalf("admin console with an admin token: %d %s", w.Code, w.Body)
	}
}

// A member cannot make an admin token.
func TestAMemberCannotMakeAnAdminToken(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{People: []apitest.TestPerson{{Username: "bob"}}})
	c := f.Session(apitest.PersonID("bob"))
	if w := f.Do("POST", "/api/tokens", `{"name":"ops","scope":"admin","expiresInDays":7}`, c); w.Code != http.StatusForbidden {
		t.Fatalf("a member's admin token: %d %s", w.Code, w.Body)
	}
}

// backupJob seeds alice's job on the built-in backup template and returns
// its token.
func backupJob(t *testing.T, f *apitest.Fixture) (text string, job model.Job) {
	t.Helper()
	ctx := context.Background()
	j := apitest.JobObj(apitest.LocalAlice(), "backups")
	j.Spec.Template, j.Spec.Engine, j.Spec.Source = model.BackupTemplate, "command", model.JobSource{}
	j.Spec.Command = []string{"javo", "backup", "run"}
	gatewaytest.Seed(t, f.Db, []any{j})
	job, err := f.Db.Jobs().GetByOwnerName(ctx, apitest.LocalAlice(), "backups")
	if err != nil {
		t.Fatal(err)
	}
	text, _, err = f.Tokens.Create(ctx, model.Actor{System: model.SystemController}, token.Token{Name: "job backups", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite, Job: job.ID})
	if err != nil {
		t.Fatal(err)
	}
	return text, job
}

func backupTemplate() *model.Template {
	t := gatewaytest.Template(model.BackupTemplate, "python")
	t.Spec.APIToken = true
	t.Spec.Jobs = &model.TemplateJobs{Engines: []string{"command"}}
	return t
}

// The backup job's token, of a platform admin, may start and follow backup
// and restore runs, and nothing else an admin may do.
func TestTheBackupJobsTokenReachesOnlyBackupRuns(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Engines: apitest.TestEngines()}, backupTemplate(), bobsSales())
	f.LoginAlice()
	text, _ := backupJob(t, f)
	if w := f.Bearer("GET", "/api/admin/backups/runs/nope", "", text, nil); w.Code == http.StatusForbidden || w.Code == http.StatusUnauthorized {
		t.Fatalf("backup run with the backup job's token: %d %s", w.Code, w.Body)
	}
	for _, p := range []string{"/api/admin/apps", "/api/admin/backups", "/api/admin/report"} {
		if w := f.Bearer("GET", p, "", text, nil); w.Code != http.StatusForbidden {
			t.Fatalf("%s with the backup job's token: %d %s", p, w.Code, w.Body)
		}
	}
	if w := f.Bearer("POST", "/api/workspaces/sales/stop?owner=bob-5e6f7a8b", "", text, nil); w.Code != http.StatusNotFound {
		t.Fatalf("stopping bob's app with the backup job's token: %d %s", w.Code, w.Body)
	}
}

// A job's token stops working as soon as its job is deleted or its template
// stops giving one, without waiting for the controller.
func TestAJobTokenEndsWithItsJob(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Engines: apitest.TestEngines()}, backupTemplate())
	f.LoginAlice()
	text, job := backupJob(t, f)
	if w := f.Bearer("GET", "/api/me", "", text, nil); w.Code != http.StatusOK {
		t.Fatalf("live job token: %d %s", w.Code, w.Body)
	}
	if err := f.Db.Jobs().MarkDeleted(context.Background(), gatewaytest.Actor, job.ID); err != nil {
		t.Fatal(err)
	}
	if w := f.Bearer("GET", "/api/me", "", text, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("token of a deleted job: %d %s", w.Code, w.Body)
	}
}

func TestAJobTokenEndsWhenItsTemplateStopsGivingOne(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Engines: apitest.TestEngines()}, backupTemplate())
	f.LoginAlice()
	text, _ := backupJob(t, f)
	tpl := backupTemplate()
	tpl.Spec.APIToken = false
	tpl.Source = store.SourceAdmin
	tpl.Spec.ApplyDefaults()
	cur, err := f.Db.Templates().Get(context.Background(), tpl.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Db.Templates().Put(context.Background(), gatewaytest.Actor, *tpl, cur.Version); err != nil {
		t.Fatal(err)
	}
	if w := f.Bearer("GET", "/api/me", "", text, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("token of a job whose template gives none: %d %s", w.Code, w.Body)
	}
}

// A propose token, the one an agent gets over MCP, reads and stores plans but
// cannot make a change itself, so the approval step means something.
func TestAProposeTokenOnlyReadsAndProposes(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), apitest.RunningSalesOf(apitest.LocalAlice()))
	c := f.LoginAlice()
	tok := f.NewToken(c, `{"name":"agent","scope":"propose","expiresInDays":7}`).Token
	if w := f.Bearer("GET", "/api/workspaces", "", tok, nil); w.Code != http.StatusOK {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	if w := f.Bearer("POST", "/api/plans", `{"source":"mcp:agent","proposal":{"action":"stop","target":"sales"}}`, tok, nil); w.Code != http.StatusCreated {
		t.Fatalf("propose: %d %s", w.Code, w.Body)
	}
	for _, req := range []struct{ method, path, body string }{
		{"POST", "/api/workspaces", `{"displayName":"Mine","template":"streamlit","file":"app.py"}`},
		{"POST", "/api/workspaces/sales/stop", ""},
		{"DELETE", "/api/workspaces/sales", ""},
		{"PUT", "/api/files/raw?path=x.py", "print(1)"},
	} {
		if w := f.Bearer(req.method, req.path, req.body, tok, nil); w.Code != http.StatusForbidden {
			t.Fatalf("%s %s with a propose token: %d %s", req.method, req.path, w.Code, w.Body)
		}
	}
	if f.GetWorkspace(apitest.LocalAlice(), "sales").Paused {
		t.Fatal("a propose token stopped the app")
	}
}

// An admin token acts as a member once its owner's groups were last confirmed
// by the provider more than a day ago, so a person disabled at the provider
// does not keep admin power for the 30 days a token may otherwise last.
func TestAnAdminTokenNeedsRecentlyConfirmedGroups(t *testing.T) {
	t.Parallel()
	now := time.Now()
	clock := func() time.Time { return now }
	f := apitest.New(t, apitest.Options{Now: clock})
	tok := f.NewToken(f.LoginAlice(), `{"name":"ops","scope":"admin","expiresInDays":7}`).Token
	if w := f.Bearer("GET", "/api/admin/apps", "", tok, nil); w.Code != http.StatusOK {
		t.Fatalf("fresh admin token: %d %s", w.Code, w.Body)
	}
	now = now.Add(authn.ElevatedGroupsMaxAge + time.Minute)
	if w := f.Bearer("GET", "/api/admin/apps", "", tok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("admin token a day after the last confirmation: %d %s", w.Code, w.Body)
	}
	if w := f.Bearer("GET", "/api/me", "", tok, nil); w.Code != http.StatusOK {
		t.Fatalf("as a member: %d %s", w.Code, w.Body)
	}
}

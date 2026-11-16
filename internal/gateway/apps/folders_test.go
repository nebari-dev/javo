package apps_test

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// lockedAgents is a gateway of a platform that starts coding agents locked
// in (workloads.landlock) or not, with alice's agent app "agent" in
// ~/projects/api and her notebook app "lab".
func lockedAgents(t *testing.T, lock bool) *apitest.Fixture {
	t.Helper()
	tpl := apitest.AgentTemplate()
	tpl.Spec.Category = model.CategoryAgents
	agent := gatewaytest.Workspace("agent", apitest.LocalAlice(), "Agent")
	agent.Template, agent.File = "agent", "projects/api"
	lab := gatewaytest.Workspace("lab", apitest.LocalAlice(), "Lab")
	lab.Template = "lab"
	return apitest.New(t, apitest.Options{LockAgents: lock}, tpl, gatewaytest.Template("lab", "jupyterlab"), agent, lab)
}

type foldersJSON struct {
	Folders *foldersView
}

type foldersView struct {
	Project string
	Grants  []model.FolderGrant
	Places  []workspace.LockPlace
	Hidden  workspace.LockPlace
}

// The owner of a locked agent adds folders of their home to what it can
// open; the app then shows its project, what was added and the places no
// agent is let into.
func TestOwnerAddsFoldersToALockedAgent(t *testing.T) {
	t.Parallel()
	f := lockedAgents(t, true)
	alice := f.LoginAlice()
	got := apitest.Decode[foldersJSON](t, f.Do("GET", "/api/workspaces/agent", "", alice)).Folders
	if got == nil || got.Project != "~/projects/api" || len(got.Grants) != 0 {
		t.Fatalf("before: folders = %+v, want the project and nothing added", got)
	}
	if !reflect.DeepEqual(got.Places, workspace.LockPlaces()) || got.Hidden != workspace.HiddenPlace() {
		t.Fatalf("places = %+v, hidden = %+v, want the hidden places with words of their own and how every other hidden name is held", got.Places, got.Hidden)
	}
	w := f.Do("PUT", "/api/workspaces/agent/folders", `{"folders":[{"path":"/home/user/data/sales-2026/","mode":"read"},{"path":"notes.md","mode":"write"}]}`, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	want := []model.FolderGrant{{Path: "data/sales-2026", Mode: model.FolderRead}, {Path: "notes.md", Mode: model.FolderWrite}}
	if got := apitest.Decode[foldersJSON](t, w).Folders; got == nil || !reflect.DeepEqual(got.Grants, want) {
		t.Fatalf("answer: folders = %+v, want %v", got, want)
	}
	if got := apitest.Decode[foldersJSON](t, f.Do("GET", "/api/workspaces/agent", "", alice)).Folders; got == nil || !reflect.DeepEqual(got.Grants, want) {
		t.Fatalf("read back: folders = %+v, want %v", got, want)
	}
	// Taking them all away again.
	w = f.Do("PUT", "/api/workspaces/agent/folders", `{"folders":[]}`, alice)
	if got := apitest.Decode[foldersJSON](t, w).Folders; w.Code != http.StatusOK || got == nil || len(got.Grants) != 0 {
		t.Fatalf("removed: status %d, folders = %+v", w.Code, got)
	}
}

// A hidden place at the top of the home (the owner's keys, a tool's
// sign-in) is added once the owner accepted its warning, which the grant
// says itself and keeps; the same path without it is refused with what the
// place holds, and the platform's own sign-in whatever the grant says.
func TestOwnerAllowsAHiddenPlaceAfterItsWarning(t *testing.T) {
	t.Parallel()
	f := lockedAgents(t, true)
	alice := f.LoginAlice()
	w := f.Do("PUT", "/api/workspaces/agent/folders", `{"folders":[{"path":".ssh","mode":"read","risky":true},{"path":".config/gh","mode":"write","risky":true}]}`, alice)
	want := []model.FolderGrant{{Path: ".ssh", Mode: model.FolderRead, Risky: true}, {Path: ".config/gh", Mode: model.FolderWrite, Risky: true}}
	if got := apitest.Decode[foldersJSON](t, w).Folders; w.Code != http.StatusOK || got == nil || !reflect.DeepEqual(got.Grants, want) {
		t.Fatalf("status %d, folders = %+v, want %v: %s", w.Code, got, want, w.Body)
	}
	if got := f.GetWorkspace(apitest.LocalAlice(), "agent").Folders; !reflect.DeepEqual(got, want) {
		t.Fatalf("stored folders = %v, want %v", got, want)
	}
	for body, why := range map[string]string{
		`{"folders":[{"path":".aws","mode":"read"}]}`:                      "your cloud login, and opens only once you accept its warning",
		`{"folders":[{"path":".config/javo","mode":"read","risky":true}]}`: "can lift its own lock",
		`{"folders":[{"path":".config","mode":"read","risky":true}]}`:      "tick it there, not the whole folder",
	} {
		w := f.Do("PUT", "/api/workspaces/agent/folders", body, alice)
		msg, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.folders")
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(msg, why) {
			t.Errorf("%s: status %d, message %q, want 422 about %q", body, w.Code, msg, why)
		}
	}
	if got := f.GetWorkspace(apitest.LocalAlice(), "agent").Folders; !reflect.DeepEqual(got, want) {
		t.Fatalf("after the refusals: stored folders = %v, want %v", got, want)
	}
}

// A folder the check refuses is told why beside the field, and nothing of
// the list is saved.
func TestRefusedFolderIsNotSaved(t *testing.T) {
	t.Parallel()
	f := lockedAgents(t, true)
	alice := f.LoginAlice()
	for body, why := range map[string]string{
		`{"folders":[{"path":"data","mode":"read"},{"path":".ssh","mode":"read"}]}`: "SSH keys",
		`{"folders":[{"path":".npmrc","mode":"read"}]}`:                             "a tool's settings or sign-ins",
		`{"folders":[{"path":"projects/api/src","mode":"write"}]}`:                  "already open",
		`{"folders":[{"path":"../bob","mode":"read"}]}`:                             `".."`,
		`{"folders":[{"path":"/","mode":"write"}]}`:                                 "inside your home",
	} {
		w := f.Do("PUT", "/api/workspaces/agent/folders", body, alice)
		msg, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.folders")
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(msg, why) {
			t.Errorf("%s: status %d, message %q, want 422 about %q: %s", body, w.Code, msg, why, w.Body)
		}
	}
	if got := f.GetWorkspace(apitest.LocalAlice(), "agent").Folders; len(got) != 0 {
		t.Fatalf("stored folders = %v", got)
	}
}

// Only the owner, signed in in the browser, changes what their agent can
// open: not an admin who manages the app, not another person, and no API
// token, least of all the agent's own, also while its owner trusts it.
func TestOnlyTheOwnerInTheBrowserChangesFolders(t *testing.T) {
	t.Parallel()
	f := lockedAgents(t, true)
	const body = `{"folders":[{"path":"data","mode":"write"}]}`
	refused := reasons.Msg(reasons.MsgFoldersOwnerOnly)
	if w := f.Do("PUT", "/api/workspaces/agent/folders?owner="+apitest.LocalAlice(), body, f.Session("bob-bbbbbbbb")); w.Code != http.StatusNotFound {
		t.Errorf("bob: status %d: %s", w.Code, w.Body)
	}
	w := f.Do("PUT", "/api/workspaces/agent/folders?owner="+apitest.LocalAlice(), body, f.Session("root-rrrrrrrr", "javo-admins"))
	if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != refused {
		t.Errorf("an admin: status %d: %s", w.Code, w.Body)
	}
	session := f.LoginAlice()
	app := f.GetWorkspace(apitest.LocalAlice(), "agent")
	alice := model.Actor{Person: apitest.LocalAlice()}
	personal, _, err := f.Tokens.Create(context.Background(), alice, token.Token{Name: "laptop", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite})
	if err != nil {
		t.Fatal(err)
	}
	own, _, err := f.Tokens.Create(context.Background(), alice, token.Token{Name: "agent", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: app.Namespace, Name: app.ObjectName(), Short: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	if w := f.Do("PUT", "/api/workspaces/agent/trust", `{"trusted":true}`, session); w.Code != http.StatusOK {
		t.Fatalf("trusting the agent: status %d: %s", w.Code, w.Body)
	}
	for name, tok := range map[string]string{"a personal token": personal, "the trusted agent's own token": own} {
		if w := f.Bearer("PUT", "/api/workspaces/agent/folders", body, tok, nil); w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != refused {
			t.Errorf("%s: status %d: %s", name, w.Code, w.Body)
		}
	}
	if got := f.GetWorkspace(apitest.LocalAlice(), "agent").Folders; len(got) != 0 {
		t.Fatalf("stored folders = %v", got)
	}
}

// An app that does not start locked in has nothing to add to: an agent
// while the platform's lock is off, and any app that is no coding agent.
func TestOnlyALockedAgentHasFolders(t *testing.T) {
	t.Parallel()
	const body = `{"folders":[{"path":"data","mode":"read"}]}`
	for name, tc := range map[string]struct {
		lock bool
		app  string
	}{"lock off": {false, "agent"}, "no agent": {true, "lab"}} {
		f := lockedAgents(t, tc.lock)
		alice := f.LoginAlice()
		if got := apitest.Decode[foldersJSON](t, f.Do("GET", "/api/workspaces/"+tc.app, "", alice)).Folders; got != nil {
			t.Errorf("%s: folders = %+v, want none", name, got)
		}
		w := f.Do("PUT", "/api/workspaces/"+tc.app+"/folders", body, alice)
		if w.Code != http.StatusUnprocessableEntity || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgFoldersNotLocked) {
			t.Errorf("%s: status %d: %s", name, w.Code, w.Body)
		}
	}
}

// What a locked agent can open is chosen while creating it: the owner, in
// the browser, sends the grants with the app, which starts with them. The
// same check as for a change applies, and nothing is created when it
// refuses one.
func TestOwnerCreatesALockedAgentWithItsFolders(t *testing.T) {
	t.Parallel()
	f := lockedAgents(t, true)
	alice := f.LoginAlice()
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Sales agent","template":"agent","file":"projects/sales",
		"folders":[{"path":"data/sales-2026","mode":"read"},{"path":".ssh","mode":"read","risky":true}]}`, alice)
	want := []model.FolderGrant{{Path: "data/sales-2026", Mode: model.FolderRead}, {Path: ".ssh", Mode: model.FolderRead, Risky: true}}
	if got := apitest.Decode[foldersJSON](t, w).Folders; w.Code != http.StatusCreated || got == nil || got.Project != "~/projects/sales" || !reflect.DeepEqual(got.Grants, want) {
		t.Fatalf("status %d, folders = %+v, want %v: %s", w.Code, got, want, w.Body)
	}
	if got := f.GetWorkspace(apitest.LocalAlice(), "sales-agent").Folders; !reflect.DeepEqual(got, want) {
		t.Fatalf("stored folders = %v, want %v", got, want)
	}
	for name, tc := range map[string]struct{ body, why string }{
		"not acknowledged": {`{"displayName":"Two","template":"agent","folders":[{"path":".aws","mode":"read"}]}`, "accept its warning"},
		"its own project":  {`{"displayName":"Two","template":"agent","folders":[{"path":"projects/two/src","mode":"read"}]}`, "already open"},
		"no agent":         {`{"displayName":"Two","template":"lab","folders":[{"path":"data","mode":"read"}]}`, reasons.Msg(reasons.MsgFoldersNotLocked)},
	} {
		w := f.Do("POST", "/api/workspaces", tc.body, alice)
		p := apitest.Decode[apitest.Problem](t, w)
		msg, _ := p.FieldMessage("body.folders")
		if msg += p.Title; w.Code != http.StatusUnprocessableEntity || !strings.Contains(msg, tc.why) {
			t.Errorf("%s: status %d, message %q, want 422 about %q: %s", name, w.Code, msg, tc.why, w.Body)
		}
	}
	if _, err := f.Db.Apps().GetByOwnerName(context.Background(), apitest.LocalAlice(), "two"); err == nil {
		t.Fatal("an app with a refused folder was created")
	}
}

// As for a change, only the owner in the browser chooses what a new agent
// opens: a token that could would make itself an agent with its owner's
// keys. A token still creates an agent that opens its project only.
func TestNoTokenCreatesAnAgentWithFolders(t *testing.T) {
	t.Parallel()
	f := lockedAgents(t, true)
	if w := f.Do("PUT", "/api/workspaces/agent/trust", `{"trusted":true}`, f.LoginAlice()); w.Code != http.StatusOK {
		t.Fatalf("trusting the agent: status %d: %s", w.Code, w.Body)
	}
	alice := model.Actor{Person: apitest.LocalAlice()}
	app := f.GetWorkspace(apitest.LocalAlice(), "agent")
	personal, _, err := f.Tokens.Create(context.Background(), alice, token.Token{Name: "laptop", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite})
	if err != nil {
		t.Fatal(err)
	}
	own, _, err := f.Tokens.Create(context.Background(), alice, token.Token{Name: "agent", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: app.Namespace, Name: app.ObjectName(), Short: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"a personal token": personal, "a trusted agent's token": own} {
		w := f.Bearer("POST", "/api/workspaces", `{"displayName":"Mine","template":"agent","folders":[{"path":"data","mode":"read"}]}`, tok, nil)
		if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgFoldersOwnerOnly) {
			t.Errorf("%s: status %d: %s", name, w.Code, w.Body)
		}
	}
	if _, err := f.Db.Apps().GetByOwnerName(context.Background(), apitest.LocalAlice(), "mine"); err == nil {
		t.Fatal("a token created an agent with folders")
	}
	if w := f.Bearer("POST", "/api/workspaces", `{"displayName":"Mine","template":"agent","folders":[]}`, personal, nil); w.Code != http.StatusCreated {
		t.Fatalf("a personal token, no folders: status %d: %s", w.Code, w.Body)
	}
}

// The New app form asks what an agent it is about to create would open:
// the folder it is locked to (the one picked, else one of its own, by the
// name the app would get) and how the hidden places are held, the same
// answer the app gives once it exists. Nothing for a template whose apps
// do not start locked in.
func TestNewAppFormIsToldWhatItsAgentWouldOpen(t *testing.T) {
	t.Parallel()
	f := lockedAgents(t, true)
	alice := f.LoginAlice()
	for query, project := range map[string]string{
		"template=agent&displayName=Sales+agent&file=projects/sales": "~/projects/sales",
		"template=agent&displayName=Sales+agent":                     "~/projects/sales-agent",
		"template=agent&displayName=Sales+agent&file=.ssh":           "~/projects/sales-agent",
	} {
		w := f.Do("GET", "/api/workspace-folders?"+query, "", alice)
		got := apitest.Decode[foldersJSON](t, w).Folders
		if w.Code != http.StatusOK || got == nil || got.Project != project || len(got.Grants) != 0 || !reflect.DeepEqual(got.Places, workspace.LockPlaces()) || got.Hidden != workspace.HiddenPlace() {
			t.Errorf("%s: status %d, folders = %+v, want the project %s and the hidden places: %s", query, w.Code, got, project, w.Body)
		}
	}
	if w := f.Do("GET", "/api/workspace-folders?template=lab&displayName=Lab+two", "", alice); w.Code != http.StatusOK || apitest.Decode[foldersJSON](t, w).Folders != nil {
		t.Errorf("no agent: status %d: %s", w.Code, w.Body)
	}
	if w := f.Do("GET", "/api/workspace-folders?template=agent&displayName=!!!", "", alice); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("a name with no letter: status %d: %s", w.Code, w.Body)
	}
	if w := f.Do("GET", "/api/workspace-folders?template=gone&displayName=X", "", alice); w.Code < 400 {
		t.Errorf("no such template: status %d: %s", w.Code, w.Body)
	}
	off := lockedAgents(t, false)
	if w := off.Do("GET", "/api/workspace-folders?template=agent&displayName=Sales+agent", "", off.LoginAlice()); w.Code != http.StatusOK || apitest.Decode[foldersJSON](t, w).Folders != nil {
		t.Errorf("lock off: status %d: %s", w.Code, w.Body)
	}
}

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
	Folders *struct {
		Project string
		Grants  []model.FolderGrant
		Never   []struct{ Path, Why string }
		Hidden  string
	}
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
	if !neverHas(got.Never, ".ssh") || got.Hidden != workspace.HiddenFolderWhy {
		t.Fatalf("never = %+v, hidden = %q, want ~/.ssh on the list and the reason for every other hidden name", got.Never, got.Hidden)
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

func neverHas(never []struct{ Path, Why string }, p string) bool {
	for _, n := range never {
		if n.Path == p && strings.TrimSpace(n.Why) != "" {
			return true
		}
	}
	return false
}

// A folder the check refuses is told why beside the field, and nothing of
// the list is saved.
func TestRefusedFolderIsNotSaved(t *testing.T) {
	t.Parallel()
	f := lockedAgents(t, true)
	alice := f.LoginAlice()
	for body, why := range map[string]string{
		`{"folders":[{"path":"data","mode":"read"},{"path":".ssh","mode":"read"}]}`: "SSH keys",
		`{"folders":[{"path":".npmrc","mode":"read"}]}`:                             "hidden files and folders",
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

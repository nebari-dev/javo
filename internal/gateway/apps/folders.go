package apps

import (
	"context"
	"errors"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/gateway/templates"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// Folders are what the owner of a coding agent that starts locked in
// (workloads.landlock) lets it open besides its project: folders and files
// of their home, to read or to read and change (model.App.Folders). One
// function decides what may be added, for this API and for the controller
// that starts the agent (workspace.LockFolders). Like trust, only the owner
// chooses them, in the browser, with a new app (createWorkspace) or later:
// a token that could would let an agent open its own lock.

// FoldersView is what a locked coding agent can open, as the app view
// shows it.
type FoldersView struct {
	Project string                `json:"project" doc:"The folder the agent works in and is locked to, always open to read and change; ~ is the home"`
	Grants  []model.FolderGrant   `json:"grants" doc:"What the owner added, as paths relative to the home; risky on what is hidden at the top of it"`
	Places  []workspace.LockPlace `json:"places" doc:"The hidden files and folders at the top of a home with a rule and words of their own. A path that starts as hidden.path does is held by the longest place it is or is in (a place of rule around holds only itself), else by hidden; any other path is held by none"`
	Hidden  workspace.LockPlace   `json:"hidden" doc:"How every other hidden file or folder at the top of a home is held; its path is not one but what the path of each starts with (a dot)"`
}

// foldersView is the view of what app's agent can open; nil when it does
// not start locked in.
func (s *Workspaces) foldersView(app *model.App, t *model.Template) *FoldersView {
	project, locked := s.cfg.Run.LockedTo(app, t)
	if !locked {
		return nil
	}
	// What the agent is started with, which is what of the stored list
	// still passes.
	grants, _ := workspace.LockFolders(app, app.Folders)
	if grants == nil {
		grants = []model.FolderGrant{}
	}
	return &FoldersView{Project: project, Grants: grants, Places: workspace.LockPlaces(), Hidden: workspace.HiddenPlace()}
}

type foldersInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
	Body  struct {
		Folders []model.FolderGrant `json:"folders" maxItems:"16" doc:"Every folder and file of your home the agent can open besides its project: the whole list, each with a path relative to the home and read or write, and risky: true on a hidden file or folder at the top of the home, once you read what it holds"`
	}
}

func (s *Workspaces) registerFolders(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "setWorkspaceFolders", Method: http.MethodPut, Path: "/api/workspaces/{name}/folders",
		Summary: "Change what a locked coding agent can open besides its project (browser session only; a running agent restarts)",
		Description: "For a coding agent on a platform that starts agents locked to their project. The agent, and every " +
			"command it runs, can then also read (read) or read and change (write) the folders and files listed. A " +
			"hidden file or folder at the top of the home (keys, sign-ins: folders.places and folders.hidden of the app " +
			"say what each holds) is refused unless its grant says risky: true; the places there of rule closed, around " +
			"and open are always refused. The lock is set when " +
			"the agent starts, so a running agent restarts.",
		Tags: []string{"workspaces"}, Security: authn.SessionOnly,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *foldersInput) (*workspaceOutput, error) {
		app, err := s.authorizedEdit(ctx, &nameInput{Name: in.Name, Owner: in.Owner}, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		if err := foldersOwnerOnly(ctx, app.Owner); err != nil {
			return nil, err
		}
		t, err := s.db.Templates().Get(ctx, app.Template)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil, problem.FieldError("template", reasons.MsgTemplateUnavailable)
		case err != nil:
			return nil, problem.Internal("read template", err)
		}
		folders, err := s.lockFolders(app, &t, in.Body.Folders)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(app.Folders, folders) {
			if app, err = s.updateApp(ctx, app, func(a *model.App) { a.Folders = folders }); err != nil {
				return nil, err
			}
		}
		return s.output(ctx, app)
	})
}

type newFoldersInput struct {
	Template    string `query:"template" required:"true" maxLength:"253"`
	DisplayName string `query:"displayName" required:"true" maxLength:"100" doc:"The name the app would get: an agent with no folder works in a folder of its own, named after it"`
	File        string `query:"file" maxLength:"4096" doc:"The folder the app would open, relative to the home"`
	Sample      bool   `query:"sample" doc:"The app would run the template's sample; file is then ignored"`
}

type newFoldersOutput struct {
	Body struct {
		Folders *FoldersView `json:"folders,omitempty" doc:"Absent when the app would not start locked in"`
	}
}

// registerNewFolders adds the route the New app form shows the choice of
// folders from, before the app exists: what createWorkspace would lock it
// to, by the same functions.
func (s *Workspaces) registerNewFolders(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "newWorkspaceFolders", Method: http.MethodGet, Path: "/api/workspace-folders",
		Summary: "What a coding agent app would open if it were created now: the folders of GET /api/workspaces/{name}, with nothing added yet",
		Tags:    []string{"workspaces"},
		Errors:  []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *newFoldersInput) (*newFoldersOutput, error) {
		u := authn.User(ctx)
		if err := s.mayCreate(u); err != nil {
			return nil, err
		}
		name, err := naming.WorkspaceName(in.DisplayName)
		if err != nil {
			return nil, problem.FieldError("displayName", reasons.MsgNameNeedsLetter)
		}
		t, err := templates.Usable(ctx, s.db.Templates(), s.Roles, u, in.Template)
		if err != nil {
			return nil, err
		}
		b := appRequest{file: in.File}
		if in.Sample && t.Spec.Sample != nil {
			b = withSample(t, b)
		}
		// A path create would refuse is no folder: the form says so itself.
		file, _ := templates.CleanFile(b.file)
		out := &newFoldersOutput{}
		out.Body.Folders = s.foldersView(&model.App{Name: name, Owner: u.ID, Template: t.Name, File: file}, t)
		return out, nil
	})
}

// foldersOwnerOnly refuses whoever may not say what owner's coding agent
// can open: anyone but the owner, and the owner with an API token, for a
// change and for the grants a new app comes with alike.
func foldersOwnerOnly(ctx context.Context, owner string) error {
	if authn.NoTokens(ctx) != nil || authn.User(ctx).ID != owner {
		return problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgFoldersOwnerOnly))
	}
	return nil
}

// lockFolders is asked as the grants to store on app, whose template is
// t, or the refusal: app does not start locked in, or one of them is not
// to be added (workspace.LockFolders).
func (s *Workspaces) lockFolders(app *model.App, t *model.Template, asked []model.FolderGrant) ([]model.FolderGrant, error) {
	if _, locked := s.cfg.Run.LockedTo(app, t); !locked {
		return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgFoldersNotLocked))
	}
	folders, err := workspace.LockFolders(app, asked)
	if err != nil {
		return nil, problem.FieldError("folders", reasons.MsgFolderRefused, err)
	}
	return folders, nil
}

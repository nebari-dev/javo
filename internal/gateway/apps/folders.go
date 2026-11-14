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
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// Folders are what the owner of a coding agent that starts locked in
// (workloads.landlock) lets it open besides its project: folders and files
// of their home, to read or to read and change (model.App.Folders). One
// function decides what may be added, for this API and for the controller
// that starts the agent (workspace.LockFolders). Like trust, only the owner
// changes them, in the browser: a token that could would let an agent open
// its own lock.

// FoldersView is what a locked coding agent can open, as the app view
// shows it.
type FoldersView struct {
	Project string                  `json:"project" doc:"The folder the agent works in and is locked to, always open to read and change; ~ is the home"`
	Grants  []model.FolderGrant     `json:"grants" doc:"What the owner added, as paths relative to the home"`
	Never   []workspace.NeverFolder `json:"never" doc:"Places of a home no agent can be let into, each with why"`
	Hidden  string                  `json:"hidden" doc:"Why nothing else hidden at the top of the home can be added either: any path whose first name starts with a dot"`
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
	return &FoldersView{Project: project, Grants: grants, Never: workspace.NeverFolders(), Hidden: workspace.HiddenFolderWhy}
}

type foldersInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
	Body  struct {
		Folders []model.FolderGrant `json:"folders" maxItems:"16" doc:"Every folder and file of your home the agent can open besides its project: the whole list, each with a path relative to the home and read or write"`
	}
}

func (s *Workspaces) registerFolders(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "setWorkspaceFolders", Method: http.MethodPut, Path: "/api/workspaces/{name}/folders",
		Summary: "Change what a locked coding agent can open besides its project (browser session only; a running agent restarts)",
		Description: "For a coding agent on a platform that starts agents locked to their project. The agent, and every " +
			"command it runs, can then also read (read) or read and change (write) the folders and files listed. The " +
			"places in folders.never of the app, and every other hidden file or folder at the top of the home, are always " +
			"refused. The lock is set when " +
			"the agent starts, so a running agent restarts.",
		Tags: []string{"workspaces"}, Security: authn.SessionOnly,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *foldersInput) (*workspaceOutput, error) {
		if authn.NoTokens(ctx) != nil {
			return nil, problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgFoldersOwnerOnly))
		}
		app, err := s.authorizedEdit(ctx, &nameInput{Name: in.Name, Owner: in.Owner}, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		if authn.User(ctx).ID != app.Owner {
			return nil, problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgFoldersOwnerOnly))
		}
		t, err := s.db.Templates().Get(ctx, app.Template)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil, problem.FieldError("template", reasons.MsgTemplateUnavailable)
		case err != nil:
			return nil, problem.Internal("read template", err)
		}
		if _, locked := s.cfg.Run.LockedTo(app, &t); !locked {
			return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgFoldersNotLocked))
		}
		folders, err := workspace.LockFolders(app, in.Body.Folders)
		if err != nil {
			return nil, problem.FieldError("folders", reasons.MsgFolderRefused, err)
		}
		if !reflect.DeepEqual(app.Folders, folders) {
			if app, err = s.updateApp(ctx, app, func(a *model.App) { a.Folders = folders }); err != nil {
				return nil, err
			}
		}
		return s.output(ctx, app)
	})
}

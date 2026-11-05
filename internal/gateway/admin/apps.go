package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/gateway/apps"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
)

// PersonRef names a person: Name is the identity name used in the API
// (?owner=), DisplayName is for people to read.
type PersonRef struct {
	Name string `json:"name"`
	// DisplayName is the person's name, else their username; for a person
	// deleted since, the readable part of their identity name.
	DisplayName string `json:"displayName"`
	// Username tells apart people with the same display name; empty for a
	// person deleted since.
	Username string `json:"username,omitempty"`
}

// personRef labels the person called name for people to read.
func (a *API) personRef(ctx context.Context, name string) PersonRef {
	p, err := a.People.ByName(ctx, name)
	if err != nil {
		return PersonRef{Name: name, DisplayName: naming.IdentityReadable(name)}
	}
	display := p.DisplayName
	if display == "" {
		display = p.Username
	}
	return PersonRef{Name: name, DisplayName: display, Username: p.Username}
}

// AdminApp is one app as a platform admin sees it in the list of everyone's
// apps.
type AdminApp struct {
	ID    string         `json:"id"`
	Owner PersonRef      `json:"owner"`
	App   apps.Workspace `json:"app"`
	// LastActiveAt is when traffic last went through the gateway to the app;
	// absent before any.
	LastActiveAt *time.Time `json:"lastActiveAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	// CanOpen is whether the calling admin may open the app, exactly as the
	// proxy decides (authz.OpenOn): their own (but no preview while apps
	// share an origin), or, when apps have their own origins, someone else's
	// that does not run code as its owner.
	CanOpen bool `json:"canOpen"`
}

type adminAppsInput struct {
	Owner string `query:"owner" maxLength:"253" doc:"Only this person's apps (identity name)"`
}

type adminAppsOutput struct {
	Body struct {
		Items []AdminApp `json:"items"`
	}
}

// registerApps adds the list of every person's apps. Stopping one is the
// ordinary stop with ?owner=, which records that an admin did it.
func (a *API) registerApps(api huma.API) {
	s := a.Workspaces
	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminListApps", Method: http.MethodGet, Path: "/api/admin/apps",
		Summary: "Every person's apps, newest first (platform admins)",
	}, func(ctx context.Context, in *adminAppsInput) (*adminAppsOutput, error) {
		all, err := a.DB.Apps().ListAll(ctx)
		if err != nil {
			return nil, problem.Internal("list apps", err)
		}
		tpls, err := s.Templates(ctx)
		if err != nil {
			return nil, err
		}
		caller, origin := authn.User(ctx), s.Origin()
		roles := authz.RolesOf(caller, a.Roles)
		live := map[string]*model.App{}
		for i := range all {
			if all[i].DeletedAt.IsZero() {
				live[all[i].ID] = &all[i]
			}
		}
		owners := map[string]PersonRef{}
		out := &adminAppsOutput{}
		out.Body.Items = []AdminApp{}
		for i := range all {
			app := &all[i]
			if !app.DeletedAt.IsZero() || (in.Owner != "" && app.Owner != in.Owner) {
				continue
			}
			owner, ok := owners[app.Owner]
			if !ok {
				owner = a.personRef(ctx, app.Owner)
				owners[app.Owner] = owner
			}
			item := AdminApp{
				ID: app.ID, Owner: owner,
				App: s.View(ctx, app, tpls), CreatedAt: app.CreatedAt,
			}
			// The parent as ParentOf reads it: nil when gone or deleted.
			var parent *model.App
			if app.ParentID != "" {
				parent = live[app.ParentID]
			}
			d, refusal := authz.OpenOn(caller, roles, authz.Object{App: app, Parent: parent, Template: tpls[app.Template]}, origin)
			item.CanOpen = d.Allowed && refusal == ""
			if !app.LastActiveAt.IsZero() {
				t := app.LastActiveAt
				item.LastActiveAt = &t
			}
			out.Body.Items = append(out.Body.Items, item)
		}
		return out, nil
	})
}

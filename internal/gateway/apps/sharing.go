package apps

import (
	"context"
	"errors"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// directoryLimit is the most people one directory search returns.
const directoryLimit = 10

// accessWords is the plain-words catalog message for each access level.
var accessWords = map[string]reasons.MessageCode{
	model.AccessPrivate:    reasons.MsgAccessPrivate,
	model.AccessRestricted: reasons.MsgAccessRestricted,
	model.AccessSignedIn:   reasons.MsgAccessSignedIn,
	model.AccessPublic:     reasons.MsgAccessPublic,
}

// sharing serves the Sharing tab: reading and changing who may open an app,
// the people and group picker, and the list of apps shared with the caller.
type sharing struct {
	ws         *Workspaces
	people     authn.People
	roles      config.Roles
	urls       config.URLs
	oneHost    bool
	staleAfter time.Duration
	now        func() time.Time
}

// Person is a person an app is shared with.
type Person struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	// Active is false when the person is gone or has not signed in for
	// identity.staleAfter. Such people are shown, never removed.
	Active bool `json:"active"`
	// Note says why an inactive person is flagged; empty for active people.
	Note string `json:"note,omitempty"`
}

// AccessLevel is one choice of General access with its button text.
type AccessLevel struct {
	Value string `json:"value" enum:"Restricted,SignedIn,Public"`
	Label string `json:"label"`
}

// Group is a group an app is shared with.
type Group struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// SharingView is who may open an app besides its owner.
type SharingView struct {
	Access    string   `json:"access" enum:"Private,Restricted,SignedIn,Public"`
	MaxAccess string   `json:"maxAccess" enum:"Private,Restricted,SignedIn,Public"`
	Users     []Person `json:"users"`
	Groups    []Group  `json:"groups"`
	Link      string   `json:"link"`
	// Levels are the General access choices in order, with their labels.
	Levels []AccessLevel `json:"levels"`
	// TooOpen explains the cap when some level is above MaxAccess; empty otherwise.
	TooOpen string `json:"tooOpen,omitempty"`
	// Exposes says what the people the app is shared with get through it;
	// empty when it cannot be shared.
	Exposes string `json:"exposes,omitempty"`
	// APIAccess is the app's API access (see Workspace.apiAccess).
	APIAccess string `json:"apiAccess" enum:"None,Read,ReadWrite"`
	// ActsAsOwner says that the app calls the API as its owner, so anyone it
	// is shared with can make it do so; empty when it has no API access.
	ActsAsOwner string `json:"actsAsOwner,omitempty"`
}

// accessLevels are the General access choices; Private is "Restricted with nobody".
var accessLevels = []AccessLevel{
	{model.AccessRestricted, reasons.Msg(reasons.MsgAccessLabelRestricted)},
	{model.AccessSignedIn, reasons.Msg(reasons.MsgAccessLabelSignedIn)},
	{model.AccessPublic, reasons.Msg(reasons.MsgAccessLabelPublic)},
}

type sharingOutput struct{ Body SharingView }

type sharingInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
	Body  struct {
		Access string   `json:"access" enum:"Private,Restricted,SignedIn,Public"`
		Users  []string `json:"users" maxItems:"200" doc:"Identity names"`
		Groups []string `json:"groups" maxItems:"50" doc:"Group values as the identity provider reports them"`
	}
}

// DirectoryPerson is one match in the people picker.
type DirectoryPerson struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
}

type directoryInput struct {
	Q string `query:"q" maxLength:"200"`
}

type directoryOutput struct {
	Body struct {
		People []DirectoryPerson `json:"people"`
		Groups []struct {
			Value string `json:"value"`
		} `json:"groups"`
	}
}

// Owner is who shared an app.
type Owner struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// SharedApp is an app another person shared with the caller.
type SharedApp struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Template    string `json:"template"`
	Kind        string `json:"kind"`
	Phase       string `json:"phase"`
	URL         string `json:"url"`
	Owner       Owner  `json:"owner"`
	// Via is what granted access: "user" or "group:<value>".
	Via string `json:"via"`
	// Thumbnail is the app's picture; absent when thumbnails are off.
	Thumbnail *Thumbnail `json:"thumbnail,omitempty"`
}

type sharedOutput struct {
	Body struct {
		Items []SharedApp `json:"items"`
		// Empty is the sentence to show when Items is empty.
		Empty string `json:"empty,omitempty"`
	}
}

func (s *sharing) register(api huma.API) {
	tagged := func(op huma.Operation) huma.Operation {
		op.Tags = []string{"sharing"}
		op.Errors = append(op.Errors, http.StatusUnauthorized)
		return op
	}
	huma.Register(api, tagged(huma.Operation{
		OperationID: "getSharing", Method: http.MethodGet, Path: "/api/workspaces/{name}/sharing",
		Summary: "Who can open one of the user's apps", Errors: []int{http.StatusNotFound},
	}), func(ctx context.Context, in *nameInput) (*sharingOutput, error) {
		app, err := s.ws.authorized(ctx, in, authz.ActShareApp)
		if err != nil {
			return nil, err
		}
		return s.output(ctx, app)
	})
	huma.Register(api, tagged(huma.Operation{
		OperationID: "setSharing", Method: http.MethodPut, Path: "/api/workspaces/{name}/sharing",
		Summary: "Change who can open one of the user's apps",
		Errors:  []int{http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}), s.set)
	huma.Register(api, tagged(huma.Operation{
		OperationID: "searchDirectory", Method: http.MethodGet, Path: "/api/directory",
		Summary: "People and the caller's own groups matching a search",
	}), s.directory)
	huma.Register(api, tagged(huma.Operation{
		OperationID: "listShared", Method: http.MethodGet, Path: "/api/shared",
		Summary: "Apps other people shared with the user",
	}), s.shared)
}

// template reads the app's template; nil when it is gone.
func (s *sharing) template(ctx context.Context, app *model.App) (*model.Template, error) {
	t, err := s.ws.db.Templates().Get(ctx, app.Template)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, problem.Internal("read template", err)
	}
	return &t, nil
}

// exposes is what sharing an app from t hands the people it is shared with.
func exposes(t *model.Template) reasons.MessageCode {
	switch {
	case t.Spec.Build != nil:
		// The pod serves the built files and never mounts the home.
		return reasons.MsgShareExposesSite
	case model.RunsAnyCode(t):
		return reasons.MsgShareExposesCode
	}
	return reasons.MsgShareExposesApp
}

func (s *sharing) output(ctx context.Context, app *model.App) (*sharingOutput, error) {
	t, err := s.template(ctx, app)
	if err != nil {
		return nil, err
	}
	max := model.MaxSharing(t)
	v := SharingView{
		Access: model.AccessPrivate, MaxAccess: max, Users: []Person{}, Groups: []Group{},
		Link: s.ws.appURL(app), Levels: accessLevels, APIAccess: model.APIAccessNone,
	}
	if t != nil {
		v.APIAccess = model.APIAccessOf(app, t)
	}
	if max != model.AccessPrivate {
		v.Exposes = reasons.Msg(exposes(t))
	}
	if v.APIAccess != model.APIAccessNone {
		v.ActsAsOwner = reasons.Msg(reasons.MsgAPIAccessActsAsOwner)
	}
	if model.AccessRank(max) < model.AccessRank(model.AccessPublic) {
		v.TooOpen = reasons.Msg(reasons.MsgShareTooOpen, reasons.Msg(accessWords[max]))
	}
	if sh := app.Sharing; model.AccessRank(sh.Access) >= 0 {
		v.Access = sh.Access
	}
	for _, id := range app.Sharing.Users {
		p, err := s.person(ctx, id)
		if err != nil {
			return nil, err
		}
		v.Users = append(v.Users, p)
	}
	for _, g := range app.Sharing.Groups {
		v.Groups = append(v.Groups, Group{Value: g, Label: path.Base(g)})
	}
	return &sharingOutput{Body: v}, nil
}

// person describes one shared-with Identity; a deleted one shows by id and
// inactive. Any other read error fails the request, so an API hiccup never
// makes active people look gone.
func (s *sharing) person(ctx context.Context, id string) (Person, error) {
	ident, err := s.people.ByName(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return Person{ID: id, DisplayName: id, Note: reasons.Msg(reasons.MsgPersonInactive)}, nil
	case err != nil:
		return Person{}, problem.Internal("read identity", err)
	}
	p := Person{
		ID: id, DisplayName: displayName(ident), Email: ident.Email,
		Active: s.staleAfter <= 0 || s.now().Sub(ident.LastLoginAt) < s.staleAfter,
	}
	if !p.Active {
		p.Note = reasons.Msg(reasons.MsgPersonInactive)
	}
	return p, nil
}

func displayName(p model.Person) string {
	for _, n := range []string{p.DisplayName, p.Username, p.Name} {
		if n != "" {
			return n
		}
	}
	return ""
}

func (s *sharing) set(ctx context.Context, in *sharingInput) (*sharingOutput, error) {
	app, err := s.ws.authorizedEdit(ctx, &nameInput{Name: in.Name, Owner: in.Owner}, authz.ActShareApp)
	if err != nil {
		return nil, err
	}
	t, err := s.template(ctx, app)
	if err != nil {
		return nil, err
	}
	max := model.MaxSharing(t)
	b := in.Body
	var errs []problem.Field
	if model.AccessRank(b.Access) > model.AccessRank(max) {
		errs = append(errs, problem.Field{Name: "access", Msg: reasons.Msg(reasons.MsgShareTooOpen, reasons.Msg(accessWords[max]))})
	} else if b.Access != model.AccessPrivate && !s.urls.AppsOnOtherHost() && !s.oneHost {
		// An app on the UI host can act through the viewer's UI session.
		errs = append(errs, problem.Field{Name: "access", Msg: reasons.Msg(reasons.MsgShareNeedsAppsHost)})
	}
	for i, id := range b.Users {
		if _, err := s.people.ByName(ctx, id); errors.Is(err, store.ErrNotFound) {
			errs = append(errs, problem.Field{Name: "users[" + strconv.Itoa(i) + "]", Msg: reasons.Msg(reasons.MsgShareTargetUnknown, id)})
		} else if err != nil {
			return nil, problem.Internal("read identity", err)
		}
	}
	if len(errs) > 0 {
		return nil, problem.Fields(errs...)
	}

	next := model.Sharing{Access: b.Access}
	if b.Access != model.AccessPrivate {
		next.Users, next.Groups = dedupe(b.Users), dedupe(b.Groups)
	}
	saved, err := s.ws.updateApp(ctx, app, func(a *model.App) { a.Sharing = next })
	if err != nil {
		return nil, err
	}
	return s.output(ctx, saved)
}

func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func (s *sharing) directory(ctx context.Context, in *directoryInput) (*directoryOutput, error) {
	out := &directoryOutput{}
	out.Body.People = []DirectoryPerson{}
	out.Body.Groups = []struct {
		Value string `json:"value"`
	}{}
	q := strings.TrimSpace(in.Q)
	if len(q) < store.MinSearchLen {
		return out, nil
	}
	found, err := s.people.Search(ctx, q, directoryLimit)
	if err != nil {
		return nil, problem.Internal("search identities", err)
	}
	for _, id := range found {
		out.Body.People = append(out.Body.People, DirectoryPerson{ID: id.Name, DisplayName: displayName(id), Email: id.Email})
	}
	// Only the caller's own groups are searchable, so the directory never
	// lists a group the caller is not in.
	for _, g := range authn.User(ctx).Groups {
		if strings.Contains(strings.ToLower(g), strings.ToLower(q)) {
			out.Body.Groups = append(out.Body.Groups, struct {
				Value string `json:"value"`
			}{g})
		}
	}
	return out, nil
}

// shared lists the apps other people shared with the caller by name or by
// group. Apps open to everyone signed in or to the public are not listed:
// they are reached by link.
func (s *sharing) shared(ctx context.Context, in *previewsInput) (*sharedOutput, error) {
	p := authn.User(ctx)
	apps, err := s.ws.db.Apps().ListShared(ctx, p.ID, p.Groups, in.Previews) // newest first
	if err != nil {
		return nil, problem.Internal("list workspaces", err)
	}
	API, err := s.ws.Templates(ctx)
	if err != nil {
		return nil, err
	}
	roles := authz.RolesOf(p, s.roles)
	out := &sharedOutput{}
	out.Body.Items = []SharedApp{}
	for i := range apps {
		app := &apps[i]
		d := authz.Decide(p, roles, authz.ActOpenApp, authz.Object{App: app, Template: API[app.Template]})
		if !d.Allowed || d.Via != "user" && !strings.HasPrefix(d.Via, "group:") {
			continue
		}
		owner := Owner{ID: app.Owner, DisplayName: app.Owner}
		switch id, err := s.people.ByName(ctx, app.Owner); {
		case err == nil:
			owner.DisplayName = displayName(id)
		case !errors.Is(err, store.ErrNotFound):
			return nil, problem.Internal("read identity", err)
		}
		v := s.ws.View(ctx, app, API)
		out.Body.Items = append(out.Body.Items, SharedApp{
			Name: v.Name, DisplayName: v.DisplayName, Template: v.Template, Kind: v.Kind, Phase: v.Phase,
			URL: v.URL, Owner: owner, Via: d.Via, Thumbnail: v.Thumbnail,
		})
	}
	if len(out.Body.Items) == 0 {
		out.Body.Empty = reasons.Msg(reasons.MsgNothingShared)
	}
	return out, nil
}

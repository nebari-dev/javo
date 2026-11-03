// Package apps serves the app API: /api/workspaces and the routes about one
// app (logs and step streams, diagnosis and proposed changes, AI help,
// sizes, launch settings, API access, "Stop when idle", variables, source
// and previews, pictures, import and export, sharing).
package apps

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"cmp"
	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/ai"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/core/registry"
	"github.com/nebari-dev/javo/internal/diagnose"
	"github.com/nebari-dev/javo/internal/ext/codehost"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/environments"
	"github.com/nebari-dev/javo/internal/gateway/envvars"
	"github.com/nebari-dev/javo/internal/gateway/namespaces"
	"github.com/nebari-dev/javo/internal/gateway/nebi"
	"github.com/nebari-dev/javo/internal/gateway/podlogs"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/gateway/sizing"
	"github.com/nebari-dev/javo/internal/gateway/storage"
	"github.com/nebari-dev/javo/internal/gateway/templates"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/plans"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/status"
	"github.com/nebari-dev/javo/internal/store"
)

// stepOrder is the order steps appear in, matching startup order.
var stepOrder = []string{
	model.ConditionScheduled,
	model.ConditionHomeReady,
	model.ConditionImageReady,
	model.ConditionBuilt,
	model.ConditionEnvReady,
	model.ConditionStarted,
	model.ConditionReady,
}

// Step is one startup step; its text comes from the reason catalog.
type Step struct {
	Type     string   `json:"type"`
	Status   string   `json:"status" enum:"True,False,Unknown"`
	Reason   string   `json:"reason"`
	Title    string   `json:"title"`
	What     string   `json:"what"`
	Todo     string   `json:"todo"`
	Blame    string   `json:"blame"`
	Who      string   `json:"who"`
	Evidence []string `json:"evidence"`
	// Since is when the step last changed status; absent before it has a status.
	Since *time.Time `json:"since,omitempty"`
}

// Workspace is the API view of a workspace.
type Workspace struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Template    string `json:"template"`
	Kind        string `json:"kind"`
	File        string `json:"file"`
	// FilePath is File as a path inside the app's container; empty with no file.
	FilePath string `json:"filePath,omitempty"`
	// Size is the name of the size the app runs at; empty when no sizes
	// are configured.
	Size string `json:"size"`
	// Resources is the custom amount the app runs at instead of a size.
	Resources *sizing.Amount `json:"resources,omitempty"`
	// Command is the typed command line; empty when none.
	Command string `json:"command"`
	// Image and Port are what an app on a userImage template runs; empty
	// and 0 for other apps.
	Image string `json:"image"`
	Port  int32  `json:"port"`
	Phase string `json:"phase"`
	URL   string `json:"url"`
	Steps []Step `json:"steps"`
	// APIAccess is the API access the app has, its own or its template's
	// default, capped by the template.
	APIAccess string `json:"apiAccess" enum:"None,Read,ReadWrite"`
	// CanShare is whether the caller may change who can open the app (authz ShareApp).
	CanShare bool `json:"canShare"`
	// IdleStopMinutes stops the app after that many minutes without use; 0
	// is Off.
	IdleStopMinutes int32 `json:"idleStopMinutes"`
	// StoppedIdleAfter is the timeout the app was stopped at for being idle;
	// 0 when it runs or a person stopped it.
	StoppedIdleAfter int32 `json:"stoppedIdleAfter"`
	// Source is the repository the app runs from; absent for an app from the home.
	Source *SourceView `json:"source,omitempty"`
	// PreviewOf is set on the preview of a pull request.
	PreviewOf *PreviewOf `json:"previewOf,omitempty"`
	// Redeployable is whether Redeploy works: the app runs from a repository,
	// or copies a home file that its template builds.
	Redeployable bool `json:"redeployable"`
	// Env are the app's environment variables: plain values as stored,
	// secrets by name, never their values.
	Env []envvars.EnvVar `json:"env"`
	// Thumbnail is the app's picture; absent when thumbnails are off.
	Thumbnail *Thumbnail `json:"thumbnail,omitempty"`
	// Environment is the pixi environment the app runs in; absent for none.
	Environment *environments.EnvironmentView `json:"environment,omitempty"`
}

// Workspaces is the app API: /api/workspaces and every route about one app.
type Workspaces struct {
	// db holds apps and templates; k reads the app's Pod, its events and the
	// home claim.
	db    *store.Store
	k     client.Reader
	nss   *namespaces.Finder
	homes storage.Homes
	// sizing holds the roles every route checks, with the sizes and machines.
	sizing.Rules
	exp  exposure.Backend
	logs podlogs.Source
	// policy is the NetworkPolicy probe result (see gateway.Config.NetworkPolicy).
	policy netprobe.Reader
	// images governs apps on userImage templates (see gateway.Config.ContainerImages).
	images config.ContainerImagesCfg
	// tokens are revoked with the app they were made for.
	tokens *token.DBStore
	// nebi gets an app's Nebi token from idToken (the request's session)
	// when its owner creates, starts or restarts it.
	nebi    *nebi.Tokens
	idToken func(context.Context) (string, error)
	// idle is the admin's "Stop when idle" policy (see gateway.Config.IdleStop).
	idle config.IdleStopCfg
	// engine diagnoses failed apps; actions are the changes it may propose.
	engine  *diagnose.Engine
	actions *actions.Catalog
	// previews is gateway.Config.PreviewsEnabled; now is the clock.
	previews bool
	// thumbnails is gateway.Config.Thumbnails.
	thumbnails bool
	// previewsCap is the most previews one app may ask for.
	previewsCap int32
	now         func() time.Time
	// hosts and urls make the address a code host delivers webhooks to.
	hosts *registry.Registry[codehost.Host]
	urls  config.URLs
	// envs copies in the environment a new app picks.
	envs *environments.API
	// cfg is what the routes Register adds besides these are wired with.
	cfg Config
}

// appURL is the address people open the app at.
func (s *Workspaces) appURL(app *model.App) string {
	u := s.exp.URLFor(app.Owner, app.Name, app.ObjectName())
	return u.String()
}

type nameInput struct {
	Name string `path:"name" maxLength:"63"`
	// Owner names whose app it is; empty means the caller's. Only a
	// platform admin is allowed another person's app.
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
}

type workspaceOutput struct{ Body Workspace }

// previewsInput lets a list ask for the previews of pull requests too.
type previewsInput struct {
	Previews bool `query:"previews" doc:"Also list the previews of pull requests"`
}

type workspaceListOutput struct {
	Body struct {
		Items []Workspace `json:"items"`
	}
}

type createInput struct {
	Body struct {
		DisplayName string         `json:"displayName" minLength:"1" maxLength:"100"`
		Template    string         `json:"template" minLength:"1" maxLength:"253"`
		File        string         `json:"file,omitempty" maxLength:"4096" doc:"Path relative to the home folder"`
		Sample      bool           `json:"sample,omitempty" doc:"Run the template's sample; file is then ignored"`
		Size        string         `json:"size,omitempty" maxLength:"63" doc:"One of the names from GET /api/sizes; empty means the default size"`
		Resources   *sizing.Amount `json:"resources,omitempty" doc:"A custom amount instead of a size, for people the admin allows (GET /api/sizes custom)"`
		Team        string         `json:"team,omitempty" maxLength:"63" doc:"Team the app belongs to; needed only when namespaces are per team and the user is in several"`
		Source      *SourceBody    `json:"source,omitempty" doc:"Run the app from a Git repository instead of the home folder; file is then relative to the repository folder"`
		APIAccess   string         `json:"apiAccess,omitempty" enum:"None,Read,ReadWrite" doc:"Give the app JAVO_URL and JAVO_TOKEN so it can call this API as you: None, Read (GET only) or ReadWrite. Empty means the template's apiAccessDefault. Anyone the app is shared with can make it act as you."`
		// IdleStopMinutes is a pointer so an absent field takes the admin's default.
		IdleStopMinutes *int32           `json:"idleStopMinutes,omitempty" minimum:"0" doc:"Stop the app after this many minutes without use; 0 is Off. Absent means the admin's default (GET /api/idle-stop)."`
		Env             []envvars.EnvVar `json:"env,omitempty" doc:"The app's environment variables: plain values and values from your secrets (see PUT /api/workspaces/{name}/env)"`
		LaunchBody

		Environment *environments.EnvironmentPick `json:"environment,omitempty" doc:"Run the app in this pixi environment from GET /api/environments; its newest version is copied in now"`
	}
}

func (s *Workspaces) registerWorkspaces(api huma.API) {
	tagged := func(op huma.Operation) huma.Operation {
		op.Tags = []string{"workspaces"}
		op.Errors = append(op.Errors, http.StatusUnauthorized)
		return op
	}

	huma.Register(api, tagged(huma.Operation{
		OperationID: "listWorkspaces", Method: http.MethodGet, Path: "/api/workspaces",
		Summary: "The user's apps",
	}), func(ctx context.Context, in *previewsInput) (*workspaceListOutput, error) {
		tpls, err := s.Templates(ctx)
		if err != nil {
			return nil, err
		}
		apps, err := s.ownApps(ctx, authn.User(ctx), in.Previews)
		if err != nil {
			return nil, err
		}
		out := &workspaceListOutput{}
		out.Body.Items = []Workspace{}
		for i := range apps {
			out.Body.Items = append(out.Body.Items, s.View(ctx, &apps[i], tpls))
		}
		return out, nil
	})

	huma.Register(api, tagged(huma.Operation{
		OperationID: "getWorkspace", Method: http.MethodGet, Path: "/api/workspaces/{name}",
		Summary: "One of the user's apps", Errors: []int{http.StatusNotFound},
	}), func(ctx context.Context, in *nameInput) (*workspaceOutput, error) {
		app, err := s.authorized(ctx, in, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		return s.output(ctx, app)
	})

	huma.Register(api, tagged(huma.Operation{
		OperationID: "createWorkspace", Method: http.MethodPost, Path: "/api/workspaces",
		Summary: "Create an app", DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusForbidden, http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}), s.create)

	huma.Register(api, tagged(huma.Operation{
		OperationID: "startSample", Method: http.MethodPost, Path: "/api/samples/{template}",
		Summary: "Start a template's sample app", DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusForbidden, http.StatusUnprocessableEntity, http.StatusConflict, http.StatusServiceUnavailable},
	}), s.startSample)

	for _, act := range []struct {
		verb, summary string
		paused        bool
	}{{"stop", "Stop an app", true}, {"start", "Start an app", false}} {
		huma.Register(api, tagged(huma.Operation{
			OperationID: act.verb + "Workspace", Method: http.MethodPost, Path: "/api/workspaces/{name}/" + act.verb,
			Summary: act.summary, Errors: []int{http.StatusNotFound, http.StatusConflict},
		}), func(ctx context.Context, in *nameInput) (*workspaceOutput, error) {
			app, err := s.authorized(ctx, in, authz.ActManageApp)
			if err != nil {
				return nil, err
			}
			if !act.paused {
				s.keepNebi(ctx, app)
			}
			if app, err = s.setPaused(ctx, app, act.paused); err != nil {
				return nil, err
			}
			return s.output(ctx, app)
		})
	}

	huma.Register(api, tagged(huma.Operation{
		OperationID: "restartWorkspace", Method: http.MethodPost, Path: "/api/workspaces/{name}/restart",
		Summary: "Restart an app (its pod is replaced; a stopped app stays stopped)", Errors: []int{http.StatusNotFound, http.StatusConflict},
	}), func(ctx context.Context, in *nameInput) (*workspaceOutput, error) {
		app, err := s.authorized(ctx, in, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		s.keepNebi(ctx, app)
		if app, err = s.requestRestart(ctx, app); err != nil {
			return nil, err
		}
		return s.output(ctx, app)
	})

	huma.Register(api, tagged(huma.Operation{
		OperationID: "deleteWorkspace", Method: http.MethodDelete, Path: "/api/workspaces/{name}",
		Summary: "Delete an app (the home folder is kept)", DefaultStatus: http.StatusNoContent,
		Errors: []int{http.StatusNotFound, http.StatusConflict},
	}), func(ctx context.Context, in *nameInput) (*struct{}, error) {
		app, err := s.authorizedEdit(ctx, in, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		return nil, s.deleteApp(ctx, app)
	})
}

// ownApps are u's own apps, newest first: only theirs, even for an admin who
// may manage all.
func (s *Workspaces) ownApps(ctx context.Context, u authz.Principal, previews bool) ([]model.App, error) {
	apps, err := s.db.Apps().List(ctx, u.ID, previews)
	if err != nil {
		return nil, problem.Internal("list workspaces", err)
	}
	roles := authz.RolesOf(u, s.Roles)
	out := apps[:0]
	for i := range apps {
		d := authz.Decide(u, roles, authz.ActManageApp, authz.Object{App: &apps[i]})
		if d.Allowed && d.Via == "owner" {
			out = append(out, apps[i])
		}
	}
	return out, nil
}

// errNotFound is the one answer for missing and not-yours, so existence
// of other users' apps does not leak.
func errNotFound() error {
	return problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgAppNotFound))
}

// authorized returns the app in addresses (the caller's unless it names
// another owner) if authz allows the caller to do act on it. Missing, being
// deleted and not-allowed answer the same 404.
func (s *Workspaces) authorized(ctx context.Context, in *nameInput, act authz.Action) (*model.App, error) {
	app, err := s.byName(ctx, in)
	if err != nil {
		return nil, err
	}
	p := authn.User(ctx)
	if d := authz.Decide(p, authz.RolesOf(p, s.Roles), act, authz.Object{App: app}); !d.Allowed {
		return nil, errNotFound()
	}
	return app, nil
}

// byName reads the app in names, the caller's when no owner is given, with
// no check of who may see it; a deleted app is not found.
func (s *Workspaces) byName(ctx context.Context, in *nameInput) (*model.App, error) {
	owner := in.Owner
	if owner == "" {
		owner = authn.User(ctx).ID
	}
	app, err := s.db.Apps().GetByOwnerName(ctx, owner, in.Name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, errNotFound()
	case err != nil:
		return nil, problem.Internal("read workspace", err)
	case !app.DeletedAt.IsZero():
		return nil, errNotFound()
	}
	return &app, nil
}

// updateApp changes app's spec with fn, on the version the caller read: if
// the app changed since, the answer is the 409 "app changed". The result is
// the stored app.
func (s *Workspaces) updateApp(ctx context.Context, app *model.App, fn func(*model.App)) (*model.App, error) {
	next, err := s.db.Apps().UpdateSpec(ctx, authn.Actor(ctx), app.ID, app.Version, func(a *model.App) error {
		fn(a)
		return nil
	})
	switch {
	case errors.Is(err, store.ErrConflict):
		return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgAppChanged))
	case errors.Is(err, store.ErrNotFound):
		return nil, errNotFound()
	case err != nil:
		if fe := problem.Invalid(err); fe != nil {
			return nil, fe
		}
		return nil, problem.Internal("change workspace", err)
	}
	return &next, nil
}

func (s *Workspaces) output(ctx context.Context, app *model.App) (*workspaceOutput, error) {
	tpls, err := s.Templates(ctx)
	if err != nil {
		return nil, err
	}
	return &workspaceOutput{Body: s.View(ctx, app, tpls)}, nil
}

// Templates maps template name to template.
func (s *Workspaces) Templates(ctx context.Context) (map[string]*model.Template, error) {
	l, err := s.db.Templates().List(ctx)
	if err != nil {
		return nil, problem.Internal("list templates", err)
	}
	m := make(map[string]*model.Template, len(l))
	for i := range l {
		m[l[i].Name] = &l[i]
	}
	return m, nil
}

func (s *Workspaces) create(ctx context.Context, in *createInput) (*workspaceOutput, error) {
	b := in.Body
	u := authn.User(ctx)
	app := appRequest{displayName: b.DisplayName, template: b.Template, file: b.File, size: b.Size, resources: b.Resources, team: b.Team, apiAccess: b.APIAccess, idleStop: b.IdleStopMinutes, launch: b.launch(), source: b.Source.model(), env: b.Env, environment: b.Environment}
	previews, secrets, err := s.repoExtras(ctx, b.Source)
	if err != nil {
		return nil, err
	}
	if previews != nil {
		app.source.Previews = previews
	}
	app.secrets = secrets
	if b.Source != nil {
		app.tokenSecret = b.Source.TokenSecret
	}
	if b.Sample && b.Source != nil {
		return nil, problem.FieldError("source", reasons.MsgInvalidRequest)
	}
	if b.Sample {
		if err := s.mayCreate(u); err != nil {
			return nil, err
		}
		t, err := s.sampleTemplate(ctx, u, b.Template)
		if err != nil {
			return nil, err
		}
		app = withSample(t, app)
	}
	return s.createFor(ctx, u, app)
}

// withSample points app at t's sample. A folder sample is copied into the
// home and opened; an image sample (userImage templates) runs its image and
// port and has nothing to copy. The sample's command, image and port fill
// only what app leaves empty.
func withSample(t *model.Template, app appRequest) appRequest {
	sm := t.Spec.Sample
	if strings.TrimSpace(app.command) == "" {
		app.command = sm.Command
	}
	if sm.Image == "" {
		app.file, app.sample = workspace.SampleFileOf(t), true
		return app
	}
	app.file = ""
	if strings.TrimSpace(app.image) == "" {
		app.image = sm.Image
	}
	if app.port == 0 {
		app.port = sm.Port
	}
	return app
}

// mayCreate refuses a caller who may not create apps at all.
func (s *Workspaces) mayCreate(u authz.Principal) error {
	if d := authz.Decide(u, authz.RolesOf(u, s.Roles), authz.ActCreateApp, authz.Object{}); !d.Allowed {
		return problem.New(http.StatusForbidden, reasons.Msg(d.Code))
	}
	return nil
}

// sampleTemplate returns the template if u may use it and it has a sample.
func (s *Workspaces) sampleTemplate(ctx context.Context, u authz.Principal, name string) (*model.Template, error) {
	t, err := templates.Usable(ctx, s.db.Templates(), s.Roles, u, name)
	if err != nil {
		return nil, err
	}
	if t.Spec.Sample == nil {
		return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgNoSample, t.Spec.DisplayName))
	}
	return t, nil
}

// startSample creates a workspace on the template's sample. The display name
// is "<template> sample", then "... sample 2" and so on, unique per user.
func (s *Workspaces) startSample(ctx context.Context, in *templates.SampleInput) (*workspaceOutput, error) {
	u := authn.User(ctx)
	if err := s.mayCreate(u); err != nil {
		return nil, err
	}
	t, err := s.sampleTemplate(ctx, u, in.Template)
	if err != nil {
		return nil, err
	}
	for n := 1; n <= templates.MaxSampleNames; n++ {
		display := templates.SampleName(t.Spec.DisplayName+" sample", n)
		out, err := s.createFor(ctx, u, withSample(t, appRequest{displayName: display, template: t.Name}))
		var taken nameTaken
		if errors.As(err, &taken) {
			continue
		}
		return out, err
	}
	return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgSampleNameTaken))
}

// appRequest is what createFor makes: file is not yet cleaned, an empty size
// means the default and an empty team is fine unless placement needs one.
type appRequest struct {
	displayName, template, file, size, team, apiAccess string
	resources                                          *sizing.Amount
	sample                                             bool
	// idleStop is the asked "Stop when idle" timeout; nil is the default.
	idleStop *int32
	// source is the repository to run from, nil for the home folder; secrets
	// are the token pasted for it (handed to the controller apart from the
	// spec) and the webhook made for it.
	source  *model.Source
	secrets store.RepoSecrets
	// env are the variables the app gets, not yet checked.
	env []envvars.EnvVar
	// tokenSecret names the caller's secret its repository token comes
	// from, not yet checked; empty for none.
	tokenSecret string
	// environment is the pixi environment picked, not yet fetched; nil for none.
	environment *environments.EnvironmentPick
	launch
}

// createFor creates u's app and is the one create path for every operation.
// A name already in use is reported as nameTaken. An empty size stores the
// default size's name, so a later change of the default does not restart it.
func (s *Workspaces) createFor(ctx context.Context, u authz.Principal, b appRequest) (*workspaceOutput, error) {
	app, err := s.prepare(ctx, u, b)
	if err != nil {
		return nil, err
	}
	plain, bindings, err := envvars.Check(ctx, s.db, u, s.rolesOf(u), authz.Object{App: app}, b.env)
	if err != nil {
		return nil, err
	}
	app.Env = plain
	env, files, err := s.envs.Pick(ctx, b.environment)
	if err != nil {
		return nil, err
	}
	app.Environment = env
	if b.tokenSecret != "" && app.Source != nil {
		token, err := checkGitTokenSecret(ctx, s.db, u, s.rolesOf(u), app, b.tokenSecret, "source.tokenSecret")
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, token)
	}
	created, err := s.insert(ctx, u, app, store.AppExtras{Repo: b.secrets, Secrets: bindings, Environment: files})
	if err != nil {
		return nil, err
	}
	return s.output(ctx, created)
}

// prepare checks everything a create checks and returns the app to store,
// without storing it. Import previews ask it for each app, and an AI-filled
// form is checked with it, so both get the same field errors as a create.
func (s *Workspaces) prepare(ctx context.Context, u authz.Principal, b appRequest) (*model.App, error) {
	if err := s.mayCreate(u); err != nil {
		return nil, err
	}
	name, err := naming.WorkspaceName(b.displayName)
	if err != nil {
		return nil, problem.FieldError("displayName", reasons.MsgNameNeedsLetter)
	}
	t, err := templates.Usable(ctx, s.db.Templates(), s.Roles, u, b.template)
	if err != nil {
		return nil, err
	}
	if t.Spec.JobOnly {
		return nil, problem.FieldError("template", reasons.MsgTemplateUnavailable)
	}
	if err := s.checkLoginless(ctx, t); err != nil {
		return nil, err
	}
	run, err := s.checkLaunch(t, b.launch)
	if err != nil {
		return nil, err
	}
	if err := checkAPIAccess(t, b.apiAccess); err != nil {
		return nil, err
	}
	idle, err := s.idleStopFor(u, b.idleStop)
	if err != nil {
		return nil, err
	}
	if err := checkRepoFit(t, b.source); err != nil {
		return nil, err
	}
	file, ok := templates.CleanFile(b.file)
	if !ok {
		return nil, problem.FieldError("file", reasons.MsgFileInvalid)
	}
	// An app from a repository may leave the file out only on a template that
	// builds and serves a folder (a static site); any other file is checked by extension.
	if !(b.source != nil && file == "" && t.Spec.Build != nil) && !templates.OpensFile(file, t.Spec.FileExtensions) {
		return nil, problem.FieldError("file", reasons.MsgFileWrongType, strings.Join(t.Spec.FileExtensions, ", "))
	}
	sz, err := s.Allowed(ctx, b.size, b.resources)
	if err != nil {
		return nil, err
	}

	ns, err := s.nss.For(u, b.team)
	if err != nil {
		return nil, problem.Team(err)
	}
	switch ok, err := s.nss.Managed(ctx, ns); {
	case err != nil:
		return nil, problem.Internal("check namespace", err)
	case !ok:
		// The controller makes it right after the person's first sign-in.
		return nil, problem.Reason(http.StatusServiceUnavailable, reasons.NamespacePending).WithHeader("Retry-After", "2")
	}
	app := &model.App{
		Namespace: ns, Name: name, Owner: u.ID, DisplayName: b.displayName, Template: b.template,
		File: file, Sample: b.sample, Command: run.command, Image: run.image, Port: run.port,
		APIAccess: b.apiAccess, IdleStopMinutes: idle, Source: b.source,
	}
	app.Size, app.Resources = sz.Stored()
	return app, nil
}

// insert stores the app prepare made, with the repository secrets of an app
// from a repository and the secrets envvars.Check passed, in one transaction:
// no app without its token, webhook or secrets. Model validation rejects a
// bad spec before anything is stored, and its field errors come back to the form.
func (s *Workspaces) insert(ctx context.Context, u authz.Principal, app *model.App, extras store.AppExtras) (*model.App, error) {
	created, err := s.db.Apps().CreateWith(ctx, authn.Actor(ctx), app, extras)
	var missing *store.SecretMissingError
	if errors.As(err, &missing) {
		// Deleted since envvars.Check read it.
		return nil, problem.FieldError("env", reasons.MsgSecretUnknown, missing.Name)
	}
	if err != nil {
		return nil, s.createError(ctx, err, u, app.DisplayName)
	}
	if s.homes != nil {
		// The file picker pod holds a single-node home; free it for the app.
		if err := s.homes.Release(files.WithNamespace(ctx, app.Namespace), u.ID); err != nil {
			slog.Warn("release home after create", "workspace", app.Name, "err", err)
		}
	}
	s.keepNebi(ctx, &created)
	return &created, nil
}

// keepNebi stores the Nebi token of app, when it gets one, from the session
// of the request, if that is its owner's, before its pod is built: the pod
// then finds the file when it first starts, instead of after the kubelet's
// next refresh of a Secret that did not exist yet (about a minute).
func (s *Workspaces) keepNebi(ctx context.Context, app *model.App) {
	if s.nebi == nil {
		return
	}
	t, err := s.db.Templates().Get(ctx, app.Template)
	if err != nil {
		return
	}
	// A Nebi that does not answer must not hold the request up for long;
	// the proxy tries again when the owner opens the app.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s.nebi.Keep(ctx, app, &t, authn.User(ctx).ID, s.idToken)
}

// checkLoginless refuses a template whose app has no login of its own unless
// the cluster is known to enforce NetworkPolicy; the policy is all that keeps
// other pods away from such an app.
func (s *Workspaces) checkLoginless(ctx context.Context, t *model.Template) error {
	if t.Spec.AppLogin != "Disabled" {
		return nil
	}
	res, err := s.policy.Get(ctx)
	if err != nil {
		return problem.Internal("read NetworkPolicy probe result", err)
	}
	if !status.LoginlessBlocked(t.Spec.AppLogin, res.State) {
		return nil
	}
	return problem.FieldError("template", reasons.MsgAppLoginNeedsNetworkPolicy)
}

// nameTaken is the problem for a name already in use, so callers that can
// pick another name tell it from other failures.
type nameTaken struct{ *problem.Details }

// nameFree is nil when u has no app with the short name display gives, or
// the same error a create of it would get.
func (s *Workspaces) nameFree(ctx context.Context, u authz.Principal, display string) error {
	name, err := naming.WorkspaceName(display)
	if err != nil {
		return nil
	}
	existing, err := s.db.Apps().GetByOwnerName(ctx, u.ID, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil
	case err != nil:
		return problem.Internal("read workspace", err)
	}
	return takenError(&existing, display)
}

// takenError reports a name already in use by one of u's apps.
func takenError(existing *model.App, requested string) error {
	shown := existing.DisplayName
	if shown == "" {
		shown = requested
	}
	return nameTaken{problem.Fields(problem.Field{Name: "displayName", Msg: reasons.Msg(reasons.MsgNameTakenByYou, shown)})}
}

func (s *Workspaces) createError(ctx context.Context, err error, u authz.Principal, requested string) error {
	if errors.Is(err, store.ErrExists) {
		// The short name is the one the person's own display name gave.
		if name, nerr := naming.WorkspaceName(requested); nerr == nil {
			if existing, gerr := s.db.Apps().GetByOwnerName(ctx, u.ID, name); gerr == nil {
				return takenError(&existing, requested)
			}
		}
		return nameTaken{problem.Fields(problem.Field{Name: "displayName", Msg: reasons.Msg(reasons.MsgNameTaken)})}
	}
	if fe := problem.Invalid(err); fe != nil {
		return fe
	}
	return problem.Internal("create workspace", err)
}

// View is the API view of app; tpls are the templates by name.
func (s *Workspaces) View(ctx context.Context, app *model.App, tpls map[string]*model.Template) Workspace {
	p := authn.User(ctx)
	canShare := authz.Decide(p, authz.RolesOf(p, s.Roles), authz.ActShareApp, authz.Object{App: app}).Allowed
	phase := app.Status.Phase
	if phase == "" {
		phase = "Pending"
	}
	// A spec change (such as a new size) restarts a running app; until the
	// controller observes it, report the restart rather than stale Running.
	if phase == "Running" && !app.Paused && app.Version > app.Status.ObservedVersion {
		phase = "Starting"
	}
	// A started app is Starting until the controller has seen the start.
	if !app.Paused && phase == "Stopped" && app.Version > app.Status.ObservedVersion {
		phase = "Starting"
	}
	// A stopped app is Stopping until its pod is gone.
	// A held restored app is Pending whether it was running or not.
	if app.Paused && phase != "Stopped" && phase != "Failed" && !app.Held(time.Now()) {
		phase = "Stopping"
	}
	size, custom := s.Shown(app.Size, app.Resources)
	kind, access := "", model.APIAccessNone
	if t := tpls[app.Template]; t != nil {
		kind, access = t.Spec.Kind, model.APIAccessOf(app, t)
	}
	return Workspace{
		Name: app.Name, DisplayName: app.DisplayName, Template: app.Template,
		Kind: kind, File: app.File, FilePath: workspace.FilePath(app), Size: size, Resources: custom, Phase: phase, APIAccess: access,
		Command: app.Command, Image: app.Image, Port: app.Port,
		URL:   s.appURL(app),
		Steps: steps(app.Status.Conditions), CanShare: canShare,
		IdleStopMinutes: app.IdleStopMinutes, StoppedIdleAfter: app.StoppedIdleAfter,
		Source: s.sourceView(ctx, app), PreviewOf: s.previewOf(ctx, app),
		Redeployable: redeployable(app, tpls[app.Template]),
		Env:          s.envOf(ctx, app),
		Thumbnail:    s.thumbnailOf(ctx, app),
		Environment:  environments.ViewOf(app.Environment),
	}
}

// steps renders the conditions in startup order: five, the Built step of
// an app that builds and the EnvReady step of one that installs an
// environment. All text comes from
// the reason catalog; evidence is the condition message, one line each,
// minus a line that only repeats the catalog title.
func steps(conds []metav1.Condition) []Step {
	out := make([]Step, 0, len(stepOrder))
	for _, t := range stepOrder {
		st := Step{Type: t, Status: string(metav1.ConditionUnknown), Evidence: []string{}}
		i := slices.IndexFunc(conds, func(c metav1.Condition) bool { return c.Type == t })
		if i < 0 && (t == model.ConditionBuilt || t == model.ConditionEnvReady) {
			continue
		}
		if i >= 0 {
			c := conds[i]
			st.Status, st.Reason = string(c.Status), c.Reason
			if !c.LastTransitionTime.IsZero() {
				since := c.LastTransitionTime.UTC()
				st.Since = &since
			}
			r, _ := reasons.Lookup(reasons.Code(c.Reason))
			st.Title, st.What, st.Todo, st.Blame, st.Who = r.Title, r.What, r.Todo, r.Blame, r.Who
			for _, line := range strings.Split(c.Message, "\n") {
				if line = strings.TrimSpace(line); line != "" && line != r.Title {
					st.Evidence = append(st.Evidence, line)
				}
			}
		}
		out = append(out, st)
	}
	return out
}

func (s *Workspaces) rolesOf(u authz.Principal) []authz.Role { return authz.RolesOf(u, s.Roles) }

// Config wires the app API: the workspaces routes and the ones that
// hang off an app (diagnosis, plans, AI, logs, sizes, launch, API access,
// "Stop when idle", import and export, variables, pictures, source and
// sharing).
type Config struct {
	Store         *store.Store
	Client        client.Reader
	Namespaces    *namespaces.Finder
	Homes         storage.Homes
	Sizing        sizing.Rules
	Exposure      exposure.Backend
	Logs          podlogs.Source
	NetworkPolicy netprobe.Reader
	Images        config.ContainerImagesCfg
	Tokens        *token.DBStore
	IdleStop      config.IdleStopCfg
	Diagnose      *diagnose.Engine
	Environments  *environments.API
	NebiTokens    *nebi.Tokens
	// IDToken is the request's session ID token (authn.Authenticator.IDToken).
	IDToken           func(context.Context) (string, error)
	PreviewsEnabled   bool
	Thumbnails        bool
	PreviewsMaxPerApp int32
	Now               func() time.Time
	URLs              config.URLs
	CodeHosts         *registry.Registry[codehost.Host]
	Plans             *plans.Service
	AI                *ai.Service
	AIFeatures        config.AIFeatures
	AIIncludeOwners   bool
	People            authn.People
	Sharing           config.SharingCfg
	StaleAfter        time.Duration
}

// New builds the app API from c; c.Now and c.Plans must be set.
func New(c Config) *Workspaces {
	ws := &Workspaces{
		db: c.Store, k: c.Client, nss: c.Namespaces, homes: c.Homes, Rules: c.Sizing, exp: c.Exposure,
		logs: c.Logs, policy: c.NetworkPolicy, images: c.Images, tokens: c.Tokens,
		idle: c.IdleStop, engine: c.Diagnose,
		envs: c.Environments,
		nebi: c.NebiTokens, idToken: c.IDToken,
		previews: c.PreviewsEnabled, thumbnails: c.Thumbnails, previewsCap: min(cmp.Or(c.PreviewsMaxPerApp, model.MaxPreviewMax), model.MaxPreviewMax), now: c.Now, urls: c.URLs, hosts: c.CodeHosts,
		cfg: c,
	}
	ws.actions = actions.NewCatalog(ws.appActions()...)
	return ws
}

// Register adds every app route to api.
func (s *Workspaces) Register(api huma.API) {
	c := s.cfg
	s.registerWorkspaces(api)
	s.registerDiagnosis(api)
	pa := &plansAPI{ws: s, plans: c.Plans}
	pa.register(api)
	(&aiAPI{ws: s, svc: c.AI, features: c.AIFeatures, plans: pa, includeOwners: c.AIIncludeOwners}).register(api)
	s.registerLogs(api)
	s.registerSizes(api)
	s.registerLaunch(api)
	s.registerAPIAccess(api)
	s.registerIdleStop(api)
	s.registerTransfer(api)
	s.registerEnv(api)
	s.registerThumbnails(api)
	s.registerSource(api)
	(&sharing{
		ws: s, people: c.People, roles: s.Roles, urls: c.URLs,
		oneHost: c.Sharing.AllowOneHost, staleAfter: c.StaleAfter, now: c.Now,
	}).register(api)
}

// DescribeActions lists the changes an agent may propose, for MCP.
func (s *Workspaces) DescribeActions() []actions.Info { return s.actions.Describe() }

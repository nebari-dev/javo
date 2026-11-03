package apps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/ext/codehost"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/envvars"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// SourceBody is the repository an app runs from, in a create request. A
// private repository's token is one of the owner's secrets, by name; the
// controller hands its value to the clone.
type SourceBody struct {
	URL         string       `json:"url,omitempty" maxLength:"2048" doc:"https address of the repository, such as https://github.com/example-org/site"`
	Ref         string       `json:"ref,omitempty" maxLength:"255" doc:"Branch, tag or commit; empty means the repository's default branch"`
	Path        string       `json:"path,omitempty" maxLength:"1024" doc:"Folder inside the repository; empty means its root"`
	TokenSecret string       `json:"tokenSecret,omitempty" maxLength:"100" doc:"The name of one of your secrets that holds the access token of a private repository; its value is never shown"`
	Build       *SourceBuild `json:"build,omitempty" doc:"Replaces the template's build step"`
	// Previews and Webhook set up pull request previews with the app (GitHub
	// repositories only), as PUT .../previews and POST .../previews/webhook do
	// after it exists.
	Previews *PreviewsBody `json:"previews,omitempty" doc:"Preview settings the app starts with"`
	Webhook  *WebhookBody  `json:"webhook,omitempty" doc:"A webhook made by POST /api/previews/webhook, stored with the app"`
}

// WebhookBody is a webhook address id and secret made by POST
// /api/previews/webhook for an app not yet created.
type WebhookBody struct {
	ID     string `json:"id" maxLength:"64"`
	Secret string `json:"secret" maxLength:"128" doc:"Write-only: never shown again"`
}

// What a webhook address id and secret made here look like:
// base64url of 16 or 32 random bytes (store.NewHook).
var (
	hookIDPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)
	hookSecretPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// SourceBuild is the command that turns the repository into the files to
// serve and the folder (relative to the repository folder) it writes them to.
type SourceBuild struct {
	Command string `json:"command,omitempty" maxLength:"1024"`
	Output  string `json:"output,omitempty" maxLength:"1024"`
}

// model is the spec part of the request; the token goes to the store apart.
func (b *SourceBody) model() *model.Source {
	if b == nil {
		return nil
	}
	src := &model.Source{URL: strings.TrimSpace(b.URL), Ref: strings.TrimSpace(b.Ref), Path: strings.TrimSpace(b.Path)}
	if b.Build != nil {
		src.Build = &model.SourceBuild{Command: strings.TrimSpace(b.Build.Command), Output: strings.TrimSpace(b.Build.Output)}
	}
	return src
}

// SourceView is what an app shows of its repository. The token is never
// part of it: TokenSecret names the secret it comes from.
type SourceView struct {
	URL    string `json:"url"`
	Ref    string `json:"ref"`
	Path   string `json:"path"`
	Commit string `json:"commit" doc:"Commit the running pod cloned; empty until it has"`
	// HasToken is true when the clone gets a token: from TokenSecret, or
	// pasted before tokens came from secrets.
	HasToken bool `json:"hasToken"`
	// TokenSecret is the secret the token comes from; empty when none.
	TokenSecret string `json:"tokenSecret"`
	// WebURL is the commit's page on the code host; empty when unknown.
	WebURL string `json:"webURL"`
	// PreviewsAvailable is whether previews can be turned on for this app.
	PreviewsAvailable bool `json:"previewsAvailable"`
	// WebhookConfigured is true once a webhook address and secret were made.
	// Neither is ever shown again.
	WebhookConfigured bool `json:"webhookConfigured"`
}

// PreviewsView is an app's preview settings and what the last look at its
// pull requests found. Every text is the catalog's.
type PreviewsView struct {
	Enabled bool  `json:"enabled"`
	Max     int32 `json:"max" doc:"How many open pull requests get a preview"`
	// MaxAllowed is the most the platform lets one app ask for.
	MaxAllowed int32 `json:"maxAllowed"`
	Forks      bool  `json:"forks" doc:"Also preview pull requests from forks"`
	// ForksWarning is the sentence to show next to the forks switch.
	ForksWarning string `json:"forksWarning"`
	// SyncedAt is when GitHub last answered; absent before the first answer.
	SyncedAt *time.Time `json:"syncedAt,omitempty"`
	// Reason is set when the last look failed; Text is its words.
	// Title and Todo are the catalog's headline and next step for Reason.
	Reason string            `json:"reason,omitempty"`
	Title  string            `json:"title,omitempty"`
	Text   string            `json:"text,omitempty"`
	Todo   string            `json:"todo,omitempty"`
	Items  []PreviewItemView `json:"items"`
}

// PreviewItemView is one pull request: its preview (App) or why it has none.
type PreviewItemView struct {
	Number int32  `json:"number"`
	Title  string `json:"title"`
	App    string `json:"app,omitempty"`
	Reason string `json:"reason,omitempty"`
	Text   string `json:"text,omitempty"`
}

// PreviewsBody is the preview settings an app is given.
type PreviewsBody struct {
	Enabled bool  `json:"enabled"`
	Max     int32 `json:"max" doc:"1 to 10"`
	Forks   bool  `json:"forks,omitempty"`
}

type previewsOutput struct{ Body PreviewsView }

type previewSettingsInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
	Body  PreviewsBody
}

// PreviewOf names the app a preview follows and its pull request.
type PreviewOf struct {
	Name       string `json:"name"`
	PullNumber int32  `json:"pullNumber"`
}

// sourceView is the source part of an app's view; nil for an app from the
// home folder.
func (s *Workspaces) sourceView(ctx context.Context, app *model.App) *SourceView {
	src := app.Source
	if src == nil {
		return nil
	}
	_, _, has, err := s.db.GitTokens().Pending(ctx, app.ID)
	if err != nil {
		slog.Error("read the repository token state", "workspace", app.Name, "err", err)
	}
	tokenSecret, err := s.db.Secrets().GitTokenSecret(ctx, app.ID)
	if err != nil {
		slog.Error("read the repository token secret", "workspace", app.Name, "err", err)
	}
	hook, err := s.db.Previews().HasHook(ctx, app.ID)
	if err != nil {
		slog.Error("read the webhook state", "workspace", app.Name, "err", err)
	}
	owner, repo, github := model.GitHubRepo(src.URL)
	v := &SourceView{URL: src.URL, Ref: src.Ref, Path: src.Path, Commit: app.Status.Commit, HasToken: has || tokenSecret != "", TokenSecret: tokenSecret, PreviewsAvailable: s.previews && github, WebhookConfigured: hook}
	if github && app.Status.Commit != "" {
		v.WebURL = fmt.Sprintf("https://github.com/%s/%s/commit/%s", owner, repo, app.Status.Commit)
	}
	return v
}

// previewOf is the preview part of an app's view; nil for an ordinary app.
func (s *Workspaces) previewOf(ctx context.Context, app *model.App) *PreviewOf {
	if app.ParentID == "" {
		return nil
	}
	out := &PreviewOf{PullNumber: app.PullNumber}
	switch parent, err := s.db.Apps().Get(ctx, app.ParentID); {
	case err == nil:
		out.Name = parent.Name
	case !errors.Is(err, store.ErrNotFound):
		slog.Error("read the app a preview follows", "workspace", app.Name, "err", err)
	}
	return out
}

// checkRepoFit refuses a source the template does not take and a missing one
// it needs.
func checkRepoFit(t *model.Template, src *model.Source) error {
	switch {
	case src == nil && t.Spec.FromRepo == model.FromRepoRequired:
		return problem.FieldError("source", reasons.MsgTemplateNeedsRepo)
	case src != nil && t.Spec.FromRepo != model.FromRepoOptional && t.Spec.FromRepo != model.FromRepoRequired:
		return problem.FieldError("source", reasons.MsgTemplateNoRepo)
	}
	return nil
}

// authorizedEdit is authorized for a change of the app's spec: a preview is
// managed by the app it follows, so it answers 409.
func (s *Workspaces) authorizedEdit(ctx context.Context, in *nameInput, act authz.Action) (*model.App, error) {
	app, err := s.authorized(ctx, in, act)
	if err != nil {
		return nil, err
	}
	if app.ParentID != "" {
		return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgPreviewManaged))
	}
	return app, nil
}

type tokenInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
	Body  struct {
		Secret string `json:"secret" minLength:"1" maxLength:"100" doc:"The name of one of your secrets that holds the repository's access token"`
	}
}

// webhookOutput is the one answer that carries a webhook secret.
type webhookOutput struct {
	Body struct {
		URL    string `json:"url" doc:"Where the code host delivers"`
		Secret string `json:"secret" doc:"Signs deliveries. Shown once."`
		Notice string `json:"notice"`
	}
}

// hostOf is the code host that serves the app's repository; nil when none.
func (s *Workspaces) hostOf(app *model.App) codehost.Host {
	if app.Source == nil {
		return nil
	}
	return s.hostFor(app.Source.URL)
}

// hostFor is the code host that serves the repository at repoURL; nil when none.
func (s *Workspaces) hostFor(repoURL string) codehost.Host {
	if s.hosts == nil {
		return nil
	}
	for _, h := range s.hosts.All() {
		if h.Owns(repoURL) {
			return h
		}
	}
	return nil
}

// previewOptions is what the New app form needs to offer previews.
type previewOptions struct {
	Body struct {
		// Available is whether the platform has previews (for GitHub repositories).
		Available  bool  `json:"available"`
		Max        int32 `json:"max" doc:"How many previews a new app starts with"`
		MaxAllowed int32 `json:"maxAllowed" doc:"The most previews one app may ask for"`
		// ForksWarning is the sentence to show next to the forks switch.
		ForksWarning string `json:"forksWarning"`
	}
}

type newWebhookInput struct {
	Body struct {
		URL string `json:"url" maxLength:"2048" doc:"The repository the app will run from"`
	}
}

// newWebhookOutput is the one answer that carries the secret of a webhook
// for an app not yet created; the create request hands both back.
type newWebhookOutput struct {
	Body struct {
		ID     string `json:"id" doc:"Hand back in the create request's source.webhook"`
		URL    string `json:"url" doc:"Where the code host delivers, once the app exists"`
		Secret string `json:"secret" doc:"Signs deliveries. Shown once."`
		Notice string `json:"notice"`
	}
}

// repoExtras checks the previews and webhook a create request sets up with
// its repository src and returns the previews for the spec and the webhook to
// store with the app.
func (s *Workspaces) repoExtras(ctx context.Context, src *SourceBody) (*model.Previews, store.RepoSecrets, error) {
	var out store.RepoSecrets
	if src == nil {
		return nil, out, nil
	}
	url := strings.TrimSpace(src.URL)
	var previews *model.Previews
	if p := src.Previews; p != nil {
		if _, _, github := model.GitHubRepo(url); !github {
			return nil, out, problem.FieldError("source.previews", reasons.MsgPreviewsNeedGitHub)
		}
		if p.Max < 1 || p.Max > s.previewsCap {
			return nil, out, problem.FieldError("source.previews.max", reasons.MsgPreviewsTooMany, s.previewsCap)
		}
		previews = &model.Previews{Enabled: p.Enabled, Max: p.Max, Forks: p.Forks}
	}
	if h := src.Webhook; h != nil {
		if s.hostFor(url) == nil {
			return nil, out, problem.FieldError("source.webhook", reasons.MsgPreviewsNeedGitHub)
		}
		if !hookIDPattern.MatchString(h.ID) || !hookSecretPattern.MatchString(h.Secret) {
			return nil, out, problem.FieldError("source.webhook", reasons.MsgWebhookUnknown)
		}
		switch _, _, err := s.db.Previews().ByHook(ctx, h.ID); {
		case err == nil:
			return nil, out, problem.FieldError("source.webhook", reasons.MsgWebhookUnknown)
		case !errors.Is(err, store.ErrNotFound):
			return nil, out, problem.Internal("read the webhook", err)
		}
		out.HookID, out.HookSecret = h.ID, h.Secret
	}
	return previews, out, nil
}

// hookURL is the address a code host delivers to: absolute when the platform
// knows its UI host, else the path alone.
func (s *Workspaces) hookURL(hostID, hookID string) string {
	u := url.URL{Path: "/hooks/" + hostID + "/" + hookID}
	if s.urls.UIHost != "" {
		u.Scheme, u.Host = s.urls.URLScheme(), s.urls.UIHost
	}
	return u.String()
}

func (s *Workspaces) registerSource(api huma.API) {
	errs := []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity}
	huma.Register(api, huma.Operation{
		OperationID: "setWorkspaceSourceToken", Method: http.MethodPut, Path: "/api/workspaces/{name}/source/token",
		Summary:       "Take the access token of the app's private repository from one of your secrets (the app restarts)",
		DefaultStatus: http.StatusNoContent, Tags: []string{"workspaces"}, Errors: append(errs, http.StatusForbidden),
	}, func(ctx context.Context, in *tokenInput) (*struct{}, error) {
		app, err := s.ownSourceApp(ctx, &nameInput{Name: in.Name, Owner: in.Owner})
		if err != nil {
			return nil, err
		}
		if app.Source == nil {
			return nil, problem.FieldError("source", reasons.MsgTemplateNoRepo)
		}
		u := authn.User(ctx)
		if _, err := checkGitTokenSecret(ctx, s.db, u, s.rolesOf(u), app, in.Body.Secret, "secret"); err != nil {
			return nil, err
		}
		if err := s.db.Secrets().SetGitToken(ctx, authn.Actor(ctx), *app, in.Body.Secret); err != nil {
			return nil, envvars.SaveError(err, errNotFound, errNotFound)
		}
		return nil, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "deleteWorkspaceSourceToken", Method: http.MethodDelete, Path: "/api/workspaces/{name}/source/token",
		Summary:       "Clone the app's repository without an access token (the app restarts)",
		DefaultStatus: http.StatusNoContent, Tags: []string{"workspaces"}, Errors: append(errs[:3], http.StatusForbidden),
	}, func(ctx context.Context, in *nameInput) (*struct{}, error) {
		app, err := s.ownSourceApp(ctx, in)
		if err != nil {
			return nil, err
		}
		// An app with no token is already as asked.
		if err := s.db.Secrets().SetGitToken(ctx, authn.Actor(ctx), *app, ""); err != nil {
			return nil, envvars.SaveError(err, errNotFound, errNotFound)
		}
		return nil, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "getWorkspacePreviews", Method: http.MethodGet, Path: "/api/workspaces/{name}/previews",
		Summary:     "The app's preview settings and what the last look at its pull requests found",
		Description: "Each pull request is listed with its preview or the reason it has none.",
		Tags:        []string{"workspaces"}, Errors: errs[:3],
	}, func(ctx context.Context, in *nameInput) (*previewsOutput, error) {
		app, err := s.authorizedEdit(ctx, in, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		if app.Source == nil {
			return nil, problem.FieldError("source", reasons.MsgPreviewsNeedGitHub)
		}
		return s.previewsOutput(ctx, app)
	})
	huma.Register(api, huma.Operation{
		OperationID: "setWorkspacePreviews", Method: http.MethodPut, Path: "/api/workspaces/{name}/previews",
		Summary:     "Turn pull request previews on or off and set how many (GitHub repositories only)",
		Description: "One preview app per open pull request, up to max. Turning them off keeps max and forks for next time.",
		Tags:        []string{"workspaces"}, Errors: errs,
	}, func(ctx context.Context, in *previewSettingsInput) (*previewsOutput, error) {
		app, err := s.authorizedEdit(ctx, &nameInput{Name: in.Name, Owner: in.Owner}, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		if app.Source == nil {
			return nil, problem.FieldError("source", reasons.MsgPreviewsNeedGitHub)
		}
		if _, _, github := model.GitHubRepo(app.Source.URL); !github {
			return nil, problem.FieldError("source", reasons.MsgPreviewsNeedGitHub)
		}
		if b := in.Body; b.Max < 1 || b.Max > s.previewsCap {
			return nil, problem.FieldError("max", reasons.MsgPreviewsTooMany, s.previewsCap)
		}
		if in.Body.Enabled && (app.Source.Previews == nil || !app.Source.Previews.Enabled) {
			// Previews of the repository's own branches clone with its token.
			u := authn.User(ctx)
			if err := envvars.MayRewrite(ctx, s.db, u, s.rolesOf(u), authz.Object{App: app}); err != nil {
				return nil, err
			}
		}
		set := &model.Previews{Enabled: in.Body.Enabled, Max: in.Body.Max, Forks: in.Body.Forks}
		if app, err = s.updateApp(ctx, app, func(a *model.App) { a.Source.Previews = set }); err != nil {
			return nil, err
		}
		return s.previewsOutput(ctx, app)
	})
	huma.Register(api, huma.Operation{
		OperationID: "setWorkspacePreviewsWebhook", Method: http.MethodPost, Path: "/api/workspaces/{name}/previews/webhook",
		Summary: "Make the webhook address and secret that start a preview sync at once (shown once; replaces any earlier pair)",
		Description: "Add the address as a webhook of the repository, content type application/json, " +
			"with the pull request event. Without it previews still follow pull requests, at the poll interval.",
		Tags: []string{"workspaces"}, Errors: errs,
	}, func(ctx context.Context, in *nameInput) (*webhookOutput, error) {
		app, err := s.authorizedEdit(ctx, in, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		host := s.hostOf(app)
		if host == nil {
			return nil, problem.FieldError("source", reasons.MsgPreviewsNeedGitHub)
		}
		id, secret, err := s.db.Previews().SetHook(ctx, authn.Actor(ctx), app.ID)
		if err != nil {
			return nil, tokenStoreError("make the webhook", err)
		}
		out := &webhookOutput{}
		out.Body.URL = s.hookURL(host.ID(), id)
		out.Body.Secret = secret
		out.Body.Notice = reasons.Msg(reasons.MsgWebhookSecretOnce)
		return out, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "getPreviewOptions", Method: http.MethodGet, Path: "/api/previews",
		Summary: "Whether new apps can have pull request previews, and their limits",
		Tags:    []string{"workspaces"}, Errors: errs[:1],
	}, func(context.Context, *struct{}) (*previewOptions, error) {
		out := &previewOptions{}
		out.Body.Available, out.Body.MaxAllowed = s.previews, s.previewsCap
		out.Body.Max = min(model.DefaultPreviewMax, s.previewsCap)
		out.Body.ForksWarning = reasons.Msg(reasons.MsgPreviewForksWarning)
		return out, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "newPreviewsWebhook", Method: http.MethodPost, Path: "/api/previews/webhook",
		Summary: "Make a webhook address and secret for an app not yet created (shown once)",
		Description: "Nothing is stored: create the app with both in source.webhook, and the address answers from then on. " +
			"Add it as a webhook of the repository, content type application/json, with the pull request event.",
		Tags: []string{"workspaces"}, Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *newWebhookInput) (*newWebhookOutput, error) {
		if err := s.mayCreate(authn.User(ctx)); err != nil {
			return nil, err
		}
		host := s.hostFor(strings.TrimSpace(in.Body.URL))
		if host == nil {
			return nil, problem.FieldError("url", reasons.MsgPreviewsNeedGitHub)
		}
		id, secret, err := store.NewHook()
		if err != nil {
			return nil, problem.Internal("make the webhook", err)
		}
		out := &newWebhookOutput{}
		out.Body.ID, out.Body.URL, out.Body.Secret = id, s.hookURL(host.ID(), id), secret
		out.Body.Notice = reasons.Msg(reasons.MsgWebhookSecretOnce)
		return out, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "redeployWorkspace", Method: http.MethodPost, Path: "/api/workspaces/{name}/redeploy",
		Summary:     "Pull the newest commit (or home file) and restart the app",
		Description: "The pod clones again at every start, so a restart is a redeploy.",
		Tags:        []string{"workspaces"}, Errors: errs,
	}, func(ctx context.Context, in *nameInput) (*workspaceOutput, error) {
		app, err := s.authorized(ctx, in, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		t, err := s.db.Templates().Get(ctx, app.Template)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil, problem.FieldError("template", reasons.MsgTemplateUnavailable)
		case err != nil:
			return nil, problem.Internal("read template", err)
		}
		if !redeployable(app, &t) {
			return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgNothingToRedeploy))
		}
		at := s.now()
		if app, err = s.updateApp(ctx, app, func(a *model.App) { a.RestartRequestedAt = at }); err != nil {
			return nil, err
		}
		return s.output(ctx, app)
	})
}

// redeployable is whether a restart redeploys the app: it clones its
// repository, or copies one file of the home at start.
func redeployable(app *model.App, t *model.Template) bool {
	return app.Source != nil || (t != nil && t.Spec.Build != nil && app.File != "")
}

// previewsOutput is the preview settings of app and its last sync result.
func (s *Workspaces) previewsOutput(ctx context.Context, app *model.App) (*previewsOutput, error) {
	set := model.Previews{Max: model.DefaultPreviewMax}
	if p := app.Source.Previews; p != nil {
		set = *p
	}
	v := PreviewsView{Enabled: set.Enabled, Max: min(set.Max, s.previewsCap), MaxAllowed: s.previewsCap, Forks: set.Forks, ForksWarning: reasons.Msg(reasons.MsgPreviewForksWarning), Items: []PreviewItemView{}}
	sync, at, err := s.db.Previews().Sync(ctx, app.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return nil, problem.Internal("read the preview sync", err)
	default:
		if !at.IsZero() {
			v.SyncedAt = &at
		}
		if sync.Reason != "" {
			r, _ := reasons.Lookup(reasons.Code(sync.Reason))
			v.Reason, v.Title, v.Todo = sync.Reason, r.Title, r.Todo
			v.Text = strings.TrimSpace(reasonWhat(sync.Reason) + " " + sync.Message)
		}
		for _, it := range sync.Items {
			v.Items = append(v.Items, PreviewItemView{Number: it.Number, Title: it.Title, App: it.App, Reason: it.Reason, Text: reasonWhat(it.Reason)})
		}
	}
	return &previewsOutput{Body: v}, nil
}

// reasonWhat is what the catalog says happened for code; empty for no code.
func reasonWhat(code string) string {
	r, _ := reasons.Lookup(reasons.Code(code))
	return r.What
}

// ownSourceApp is the app the request names, for a change of its repository
// token: only its owner, never with an app's own API token (envvars.MayChange).
func (s *Workspaces) ownSourceApp(ctx context.Context, in *nameInput) (*model.App, error) {
	if err := authn.NoAppTokens(ctx); err != nil {
		return nil, err
	}
	app, err := s.authorizedEdit(ctx, in, authz.ActManageApp)
	if err != nil {
		return nil, err
	}
	u := authn.User(ctx)
	if err := envvars.MayChange(ctx, u, s.rolesOf(u), authz.Object{App: app}, app.Owner); err != nil {
		return nil, err
	}
	return app, nil
}

// checkGitTokenSecret checks that name is one of the caller's secrets that
// authz lets them give app as its repository token, and returns the binding.
// An unknown name is a field error on field.
func checkGitTokenSecret(ctx context.Context, db *store.Store, u authz.Principal, roles []authz.Role, app *model.App, name, field string) (model.SecretBinding, error) {
	if err := authn.NoAppTokens(ctx); err != nil {
		return model.SecretBinding{}, err
	}
	sec, unknown, err := envvars.UsableSecret(ctx, db, u, roles, authz.Object{App: app}, name, field)
	if err != nil {
		return model.SecretBinding{}, err
	}
	if unknown != nil {
		return model.SecretBinding{}, problem.Fields(*unknown)
	}
	return model.SecretBinding{Env: model.GitTokenEnv, Secret: sec.Name}, nil
}

// tokenStoreError is a failure of the token table. Its text is not shown:
// no error that touched a token is allowed near one.
func tokenStoreError(what string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return errNotFound()
	}
	return problem.Internal(what, err)
}

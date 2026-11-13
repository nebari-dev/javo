package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/loop"
	"github.com/nebari-dev/javo/internal/controller/sweep"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/imageref"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/status"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/workload"
)

// Reconciler turns app rows into workloads an Executor runs, asks for the
// owner's home (which the storage controller turns into a claim), and writes
// what the Executor observed back to the row's status columns. It never
// writes spec columns.
type Reconciler struct {
	// Exec runs the apps' workloads and Place keeps what an app has beside
	// its workload. Required.
	Exec  executor.Executor
	Place Place
	// Store is the database the apps, templates and people are read from.
	// Required.
	Store *store.Store
	// Storage reads what an app pod mounts: the owner's home, the ready
	// folders of the owner's groups and the scratch limit. Its Cluster view
	// also gives the size a new home asks for. Required.
	Storage      podstorage.Resolver
	ReadyTimeout time.Duration
	// PublicHost is the host browsers reach the gateway at; templates read it
	// as .Host when apps share the UI host. Empty when the deployment does
	// not set one.
	PublicHost string
	// Sizes are the admin-defined app sizes; empty keeps template resources.
	Sizes sizes.Catalog
	// UIDMode is identity.uidMode; anything but config.UIDModeAllocated runs as FixedUID.
	UIDMode string
	// Exposure decides the app's URL and the base path it is told to serve under.
	Exposure exposure.Backend
	// NetworkPolicy is the latest probe result; apps whose template has no
	// login of their own start only when it reads Enforced. Required.
	NetworkPolicy netprobe.Reader
	// PolicyRecheck is how soon an app refused for that reason is looked at
	// again. Zero means one minute.
	PolicyRecheck time.Duration
	// ResyncEvery is how often every app row is looked at again regardless of
	// announcements, and every orphan swept. Zero means dbsource.DefaultResync.
	ResyncEvery time.Duration
	// Tokens stores the API tokens of apps with API access, and Secrets
	// keeps the values an app reads: its API token, its secrets, its
	// repository token and its Nebi token. Secrets is required, Tokens
	// when any app has API access.
	// APIURL is the UI's public URL, given to those apps as JAVO_URL.
	Tokens  TokenMinter
	Secrets executor.Secrets
	APIURL  string
	// APIInternalURL is the gateway inside the cluster, given as
	// JAVO_API_URL; the agents' model endpoint is reached there too.
	APIInternalURL string
	// CloneImage (git-sync) and RunnerImage (the javo image) run the init
	// containers of apps from Git. Required when any app has a source.
	CloneImage, CloneGitConfig string
	RunnerImage                podspec.Image
	// LockAgents starts the coding agents locked in with Landlock (config
	// workloads.landlock).
	LockAgents bool
	// AllowedRegistries is containerImages.allowedRegistries: an app's own
	// image is checked again when its pod is built, so tightening the list
	// stops apps saved before. Empty allows any registry.
	AllowedRegistries []string
	// PixiImage installs the environment of an app that runs in one
	// (internal/pixienv). Required when any app names an environment.
	PixiImage string
	// AgentModel is the admin's model for coding-agent apps in force
	// (config.AICfg.AgentModel of the AI settings in force, read at each
	// pod build); "" while it is off, and nil is always off. Then an agent
	// app gets its owner's own model, if they set one.
	AgentModel func(ctx context.Context) (string, error)
	// NebiURL is Nebi's address as apps reach it, set while the platform's
	// Nebi integration is on: apps whose template asks for it then get
	// their owner's Nebi token (WithNebi). Empty gives no app Nebi.
	NebiURL string
	// SecretKeys opens the values of people's secrets, which apps get
	// through a bundle of their own (in Secrets). Nil when the controller
	// has no key-encryption key: an app that gets secrets then fails as
	// SecretsUnreadable rather than start without them.
	SecretKeys *secrets.Keys
	// Host is set when the executor runs apps as plain processes of this
	// machine: it says what the app of a Ref is planned with there (the
	// process executor's Host). Nil plans every app for a container.
	Host func(workload.Ref) workload.Host
	// Access decides whether an app's owner may still use its template, by
	// the rule the API applies when the app is made; an app whose owner may
	// not stops (TemplateNotAllowed). Required.
	Access Access
}

// Place is what an app has where it runs for as long as its row exists,
// whether or not its workload runs: on Kubernetes the Service the gateway
// reaches it by, which also owns the bundles that can be made again.
type Place interface {
	// Hold makes sure the app has its place and is reached there on port.
	// It comes before any of the app's bundles is written.
	Hold(ctx context.Context, ref workload.Ref, port int32) error
	// Tidy removes what earlier versions kept for the app under names no
	// longer used. No workload of the app runs when it is called. An API
	// token found there is handed to revoke first, by its id.
	Tidy(ctx context.Context, ref workload.Ref, revoke func(ctx context.Context, tokenID string) error) error
	// Release gives the place up. One that does not exist is not an error.
	Release(ctx context.Context, ref workload.Ref) error
}

// Access decides whether owner may use template t. The jobs controller's
// PeopleAccess, from the owner's person row, is the one the platform uses.
type Access interface {
	MayUse(ctx context.Context, owner string, t *model.Template) (bool, error)
}

// RBAC for the controller (make rbac renders config/rbac/controller; the
// Helm chart's Roles must grant the same rules, test/helm checks it). The
// manager's cache needs list and watch on every type it reads. Pods,
// Services, claims, events, namespaces and nodes are the Kubernetes
// executor's (internal/executor/kube); its rules are kept here, where make
// rbac looks.
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=list;watch
// Nodes, read only: their allocatable CPU and memory tell a too-big size
// from a full cluster.
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// API tokens of apps with API access: the Secret in the app's namespace is
// read, created, updated and deleted (with the app, or when its access
// becomes None; never listed or watched, so no Secret is cached); in the
// gateway's namespace the controller only creates token records (the chart's
// Role there grants create alone).
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update;delete

// unmanagedRetry is how often an app the executor does not serve yet (on
// Kubernetes, one in a namespace not labelled as managed) is looked at
// again.
const unmanagedRetry = 10 * time.Second

// pass is one reconcile of an app: what is known of it so far.
type pass struct {
	app *model.App
	now time.Time
	// have is the app's workload; nil when it has none, or none any more.
	have *workload.State
	// obs is what the status is derived from and url the address written
	// with it.
	obs status.Observed
	url string

	// tpl is the app's template; nil for a paused app, which needs none.
	tpl *model.Template
	// gitHolder and gitGen name the repository token the clone reads and
	// gitUnreadable is why it could not be opened (syncGitToken).
	gitHolder, gitGen, gitUnreadable string
	// blocked is set when no workload may start because the app has no
	// login of its own and network policy is not known to be enforced.
	blocked bool
	// layout is what the workload mounts, as which uid, and layoutFP its
	// fingerprint; empty when the owner's uid is not known now.
	layout   podstorage.Layout
	layoutFP string
}

// verdict is what one step of a pass decided.
type verdict struct {
	// stop is set when the app's workload must not run (any longer).
	stop bool
	// done is set when the status is settled without looking at the
	// workload; the steps after it are skipped. after is when to look
	// again.
	done  bool
	after time.Duration
}

// Reconcile makes the app's workload match one app row (the request names
// its id) and records derived status. The steps decide; the workload is
// stopped and the status written here and nowhere else.
func (r *Reconciler) Reconcile(ctx context.Context, id string) (time.Duration, error) {
	app, err := r.Store.Apps().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		// Gone: what it left, if anything, is the sweep's.
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !app.DeletedAt.IsZero() {
		return 0, r.finalize(ctx, &app)
	}
	ref := RefOf(&app)
	have, err := r.Exec.Observe(ctx, ref)
	if errors.Is(err, executor.ErrUnmanaged) {
		log.FromContext(ctx).Info("ignoring app in a namespace the controller does not manage", "namespace", app.Namespace)
		return unmanagedRetry, nil
	}
	if err != nil {
		return 0, err
	}
	appURL := r.Exposure.URLFor(app.Owner, app.Name, app.ObjectName())
	p := &pass{app: &app, now: time.Now(), have: have, url: appURL.String()}
	p.obs = status.Observed{Now: p.now, ReadyTimeout: r.ReadyTimeout}

	var v verdict
	for _, step := range []func(context.Context, *pass) (verdict, error){r.admit, r.house, r.run} {
		if v, err = step(ctx, p); err != nil {
			return 0, err
		}
		if v.stop && p.have != nil {
			if err := r.Exec.Stop(ctx, ref); err != nil {
				return 0, err
			}
			p.have = nil
		}
		if v.done {
			break
		}
	}

	// The commit is the one the current workload reported, so a replaced
	// workload's commit never lingers; an app held after a restore keeps
	// the one it last ran.
	commit := ""
	if !v.done && p.have != nil {
		p.obs.Workload, commit = p.have, p.have.Commit
	}
	if app.Held(p.now) {
		commit = app.Status.Commit
	}
	phase, conds, evidence := status.Derive(&app, p.obs)
	if err := r.writeStatus(ctx, &app, phase, p.url, status.WithEvidence(conds, evidence), commit); err != nil {
		return 0, err
	}
	switch {
	case v.done:
		return v.after, nil
	case p.blocked:
		return r.policyRecheck(), nil
	}
	return r.readyRecheck(p.have, phase, p.now), nil
}

// admit decides whether the app may run at all: not while it is held or
// paused, and not without a template its owner may use.
func (r *Reconciler) admit(ctx context.Context, p *pass) (verdict, error) {
	app := p.app
	// The token hand-off needs no template, so it runs before any state
	// below (paused, template missing, home conflict) can end the pass and
	// leave a pasted token in the row.
	var err error
	if p.gitHolder, p.gitGen, p.gitUnreadable, err = r.syncGitToken(ctx, app, p.have); err != nil {
		return verdict{}, err
	}
	// A restored app waits for an admin: no workload, and no home claim,
	// which the volume restore may still be bringing back.
	if app.Held(p.now) {
		v := verdict{stop: true, done: true}
		if !app.ReleaseAt.IsZero() {
			v.after = app.ReleaseAt.Sub(p.now)
		}
		return v, nil
	}
	if app.Paused {
		return verdict{stop: true}, nil
	}
	t, err := r.Store.Templates().Get(ctx, app.Template)
	switch {
	case errors.Is(err, store.ErrNotFound):
		p.obs.TemplateMissing, p.url = true, ""
		return verdict{done: true}, nil
	case err != nil:
		return verdict{}, err
	}
	// Admin templates are stored as written; an empty field means its default.
	t.Spec.ApplyDefaults()
	if t.Spec.JobOnly {
		// The template's command is a placeholder; a workload would exit
		// and crash-loop.
		p.obs.TemplateError, p.url = "the template only runs jobs, so it cannot start an app", ""
		return verdict{done: true}, nil
	}
	switch ok, err := r.Access.MayUse(ctx, app.Owner, &t); {
	case err != nil:
		return verdict{}, err
	case !ok:
		p.obs.TemplateNotAllowed, p.url = true, ""
		return verdict{stop: true, done: true}, nil
	}
	p.tpl = &t
	p.obs.AppLogin, p.obs.Builds = t.Spec.AppLogin, t.Spec.Build != nil
	// withEnvironment adds the install step for either (internal/pixienv).
	p.obs.InstallsEnv = t.Spec.Environment != "" || app.Environment != nil
	if t.Spec.AppLogin == "Disabled" {
		res, err := r.NetworkPolicy.Get(ctx)
		if err != nil {
			return verdict{}, err
		}
		p.obs.NetworkPolicy = res.State
	}
	return verdict{}, nil
}

// house decides whether the app has what a workload needs before it is
// built: its owner's home, ready to mount, and the uid it runs as.
func (r *Reconciler) house(ctx context.Context, p *pass) (verdict, error) {
	app := p.app
	if p.have == nil {
		// No workload reads what earlier versions kept for the app any more.
		if err := r.Place.Tidy(ctx, RefOf(app), r.revokeOwn(app)); err != nil {
			return verdict{}, err
		}
	}
	home, err := r.ensureHome(ctx, app)
	if err != nil {
		return verdict{}, err
	}
	if c := meta.FindStatusCondition(home.Status.Conditions, model.ConditionReady); c != nil && c.Reason == string(reasons.HomeClaimForeign) {
		p.obs.HomeConflict = true
		return verdict{done: true}, nil
	}
	migrating := meta.IsStatusConditionTrue(home.Status.Conditions, model.ConditionMigrating)
	if p.tpl != nil && (migrating || meta.IsStatusConditionTrue(home.Status.Conditions, model.ConditionRestoring)) {
		// The copy (or the restore) owns the files until it ends: no app may
		// write to them meanwhile. The home's change brings the app back afterwards.
		p.obs.HomeMigrating, p.obs.HomeRestoring = migrating, !migrating
		return verdict{stop: true, done: true}, nil
	}
	// Like the owner's uid below, the home only matters when a workload is
	// about to be built: one that already exists keeps its status while its
	// home is being prepared again.
	if p.tpl != nil && p.have == nil && !home.Status.Prepared {
		p.obs.HomeReason, p.obs.HomeEvidence = homeBlock(&home)
		return verdict{done: true}, nil
	}
	p.blocked = p.have == nil && status.LoginlessBlocked(p.obs.AppLogin, p.obs.NetworkPolicy)
	// The layout the workload should have: what it mounts, as which uid. A
	// workload that already exists keeps its status whatever happens to the
	// person, so without their uid it is kept (layoutFP stays empty); one
	// about to be built waits for it.
	if p.tpl != nil && !p.blocked {
		uid, ok, err := identity.PodUIDOf(ctx, r.Store.People(store.IDRange{}), r.UIDMode, app.Owner)
		switch {
		case err != nil:
			return verdict{}, err
		case ok:
			if p.layout, err = r.layout(ctx, app, uid); err != nil {
				return verdict{}, err
			}
			p.layoutFP = r.layoutFingerprint(app, p.tpl, p.layout)
		case p.have == nil:
			p.obs.IdentityPending = true
			return verdict{done: true}, nil
		}
	}
	return verdict{}, nil
}

// run brings the app's workload in line with its row: the values it reads
// are written, a workload built from something else is stopped (the next
// pass, once it is gone, starts the new one), and an app with none gets
// one.
func (r *Reconciler) run(ctx context.Context, p *pass) (verdict, error) {
	app, tpl, ref := p.app, p.tpl, RefOf(p.app)
	if tpl == nil {
		return verdict{}, nil
	}
	port, err := r.portOf(ctx, ref, AppPortOf(app, tpl))
	if err != nil {
		return verdict{}, err
	}
	if err := r.Place.Hold(ctx, ref, port); err != nil {
		return verdict{}, err
	}
	access, hasToken := model.APIAccessOf(app, tpl), false
	if scope := token.ScopeFor(access); scope != "" {
		if _, err := r.ensureAPIToken(ctx, app, scope); err != nil {
			return verdict{}, err
		}
		hasToken = true
	} else if p.have == nil || p.have.Fingerprint.APIAccess != model.APIAccessNone {
		// Only an app that had access, or whose workload is not built yet,
		// can have a token left; others make no call.
		if err := r.removeAPIToken(ctx, app); err != nil {
			return verdict{}, err
		}
	}
	if r.NebiURL != "" && model.NebiTokenFor(app, tpl) {
		if err := r.syncNebiToken(ctx, app); err != nil {
			return verdict{}, err
		}
	}
	agent, err := r.agentModelOf(ctx, app, tpl)
	if err != nil {
		return verdict{}, err
	}
	appSecrets, err := r.secretsOf(ctx, app, agent)
	if err != nil {
		return verdict{}, err
	}
	built := r.fingerprint(app, access, p.gitGen, appSecrets.Fingerprint, p.layoutFP)
	if p.have != nil {
		// Stale: built before the names of what it reads changed, after
		// which what was kept under the old ones goes (Place.Tidy).
		return verdict{stop: p.have.Stale || built.Outdated(p.have.Fingerprint)}, nil
	}
	if p.blocked {
		return verdict{}, nil
	}
	size, ok := r.appSize(app)
	unreadable, err := r.writeAppSecrets(ctx, app, appSecrets)
	if err != nil {
		return verdict{}, err
	}
	if unreadable == "" {
		unreadable = p.gitUnreadable
	}
	if !ok {
		p.obs.SizeUnknown = app.Size
		return verdict{}, nil
	}
	if unreadable != "" {
		p.obs.SecretsUnreadable = unreadable
		return verdict{}, nil
	}
	in := Inputs{
		App: app, Template: tpl, Layout: p.layout, Size: size, Fingerprint: built,
		Images:    Images{Clone: r.CloneImage, Runner: r.RunnerImage, Pixi: r.PixiImage, GitConfig: r.CloneGitConfig, AllowedRegistries: r.AllowedRegistries, LockAgents: r.LockAgents},
		APIAccess: access, HasToken: hasToken, APIURL: r.APIURL, APIInternalURL: r.APIInternalURL,
		GitHolder: p.gitHolder, GitGen: p.gitGen, Secrets: appSecrets, Agent: agent,
		Port: port,
	}
	if r.Host != nil {
		host := r.Host(ref)
		in.Host = &host
	}
	in.BaseURL, in.PublicHost = r.platform().address(app)
	if r.NebiURL != "" && model.NebiTokenFor(app, tpl) {
		in.NebiURL = r.NebiURL
	}
	if in.EnvFiles, err = r.environmentFiles(ctx, app); err != nil {
		return verdict{}, err
	}
	spec, planErr := Plan(in)
	var notAllowed *imageref.NotAllowedError
	var noEnvironment *EnvironmentError
	switch {
	case errors.As(planErr, &notAllowed):
		p.obs.ImageNotAllowed = reasons.Msg(reasons.MsgImageRegistryNotAllowed, notAllowed.Registry, strings.Join(r.AllowedRegistries, ", "))
	case errors.As(planErr, &noEnvironment):
		p.obs.EnvironmentProblem = noEnvironment.Error()
	case planErr != nil:
		p.obs.TemplateError = planErr.Error()
	default:
		var unsupported *executor.UnsupportedError
		switch err := r.Exec.Start(ctx, spec); {
		case errors.As(err, &unsupported):
			// The plan has to change before a start can work.
			p.obs.TemplateError = unsupported.Error()
			return verdict{}, nil
		case err != nil:
			return verdict{}, err
		}
		if p.have, err = r.Exec.Observe(ctx, ref); err != nil {
			return verdict{}, err
		}
	}
	return verdict{}, nil
}

// portOf is the port the app is told to serve on: the one its executor
// reaches it at when asked for port, the app's own. Kubernetes and Docker
// answer with port itself; an executor that runs apps as processes of
// one machine picks a free port for each.
func (r *Reconciler) portOf(ctx context.Context, ref workload.Ref, port int32) (int32, error) {
	addr, err := r.Exec.Addr(ctx, ref, port)
	if err != nil {
		return 0, err
	}
	_, text, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("the executor's address of %s: %w", ref.Name, err)
	}
	n, err := strconv.ParseInt(text, 10, 32)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("the executor's address of %s has no port: %q", ref.Name, addr)
	}
	return int32(n), nil
}

// remove ends the app's workload and gives up its place: all an app has
// where it runs, but for its bundles.
func (r *Reconciler) remove(ctx context.Context, ref workload.Ref) error {
	if err := r.Exec.Stop(ctx, ref); err != nil {
		return err
	}
	return r.Place.Release(ctx, ref)
}

// finalize removes what an app marked deleted left where it ran: its
// workload, its place and its bundles, never the owner's home. Once that is
// done it closes the app's open usage interval and removes the row. A
// failure leaves the row, so the next reconcile tries again.
func (r *Reconciler) finalize(ctx context.Context, app *model.App) error {
	ref := RefOf(app)
	if err := r.remove(ctx, ref); err != nil {
		return err
	}
	for _, use := range secretUses {
		if err := r.Secrets.Delete(ctx, ref, SecretName(app.ObjectName(), use)); err != nil {
			return err
		}
	}
	if err := r.Place.Tidy(ctx, ref, r.revokeOwn(app)); err != nil {
		return err
	}
	if err := r.Store.Usage().Stop(ctx, app.ID, time.Now()); err != nil {
		return err
	}
	err := r.Store.Apps().Purge(ctx, model.Actor{System: model.SystemController}, app.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

// platform is what apps' runs take from the install (Platform).
func (r *Reconciler) platform() Platform {
	return Platform{Exposure: r.Exposure, PublicHost: r.PublicHost, PixiImage: r.PixiImage, LockAgents: r.LockAgents}
}

// layoutFingerprint is LayoutFingerprint of app as this install builds it.
// A coding agent that starts locked in (LockAgents) runs another command
// than the same app not locked in, which no other part of the fingerprint
// covers: it has another fingerprint, so turning the lock on or off
// replaces the running agents, once, and no other app. With the lock off
// it is LayoutFingerprint itself.
func (r *Reconciler) layoutFingerprint(app *model.App, t *model.Template, layout podstorage.Layout) string {
	fp := LayoutFingerprint(app, t, layout)
	if !locksAgent(t, r.LockAgents) {
		return fp
	}
	sum := sha256.Sum256([]byte(fp + " landlock"))
	return hex.EncodeToString(sum[:8])
}

// fingerprint is what a pod built for app now would be marked with: its
// size, its command, image, port and Git source, its latest restart
// request, its API access, its repository token (gitGen, the generation of
// the paste), its secrets (their fingerprint) and its layout (layoutFP,
// LayoutFingerprint; empty when it cannot be known now).
func (r *Reconciler) fingerprint(app *model.App, access, gitGen, secretsFP, layoutFP string) workload.Fingerprint {
	size, _ := r.appSize(app)
	return workload.Fingerprint{
		Size: size.Key(), SizeUnpicked: app.Size == "" && app.Resources == nil,
		Run: RunFingerprint(app), Restart: RestartMark(app),
		APIAccess: access, GitToken: gitGen, Secrets: secretsFP, Layout: layoutFP,
	}
}

// appSize is what the app asks to run at: its custom amount, or its named
// size ("" the default). ok is false for a size that is not configured.
func (r *Reconciler) appSize(app *model.App) (sizes.Size, bool) {
	return r.Sizes.For(app.Size, app.Resources)
}

// ensureHome returns the owner's home, asking for it if it does not exist. A
// new home asks for the configured default size, so a later change of the
// default does not resize it. The storage controller makes the claim; this
// reconciler never does.
func (r *Reconciler) ensureHome(ctx context.Context, app *model.App) (model.Home, error) {
	return r.Storage.EnsureHome(ctx, r.Store.Homes(), app.Namespace, app.Owner)
}

// homeBlock is why a home cannot be mounted yet: the reason its Ready
// condition carries, or HomePreparing before the storage controller has
// looked at it.
func homeBlock(home *model.Home) (reasons.Code, string) {
	c := meta.FindStatusCondition(home.Status.Conditions, model.ConditionReady)
	if c == nil || c.Status == metav1.ConditionTrue {
		return reasons.HomePreparing, ""
	}
	return reasons.Code(c.Reason), c.Message
}

// layout is the storage layout of app's pod running as uid: its owner's home,
// the ready folders of the owner's groups and the scratch limit.
func (r *Reconciler) layout(ctx context.Context, app *model.App, uid int64) (podstorage.Layout, error) {
	in, err := r.Storage.Input(ctx, app.Namespace, app.Owner)
	if err != nil {
		return podstorage.Layout{}, err
	}
	in.UID, in.GID = uid, podspec.FixedGID
	return podstorage.Build(app.Owner, in), nil
}

func (r *Reconciler) policyRecheck() time.Duration {
	if r.PolicyRecheck > 0 {
		return r.PolicyRecheck
	}
	return time.Minute
}

// readyRecheck schedules a reconcile for when a workload that runs but does
// not answer yet passes ReadyTimeout, so AppNotAnswering appears without any
// new event.
func (r *Reconciler) readyRecheck(have *workload.State, phase string, now time.Time) time.Duration {
	if have == nil || phase != "Starting" || !have.Running || r.ReadyTimeout <= 0 {
		return 0
	}
	since := now
	if u := have.Unanswered; u != nil && !u.Since.IsZero() {
		since = u.Since
	}
	d := r.ReadyTimeout - now.Sub(since) + time.Second
	if d < time.Second {
		d = time.Second
	}
	return d
}

// writeStatus updates status only when it differs. Conditions that keep their
// status keep their original transition time, so repeated reconciles are no-ops.
// The write names the spec version this reconcile read and touches only status
// columns, so a spec edit made meanwhile is kept and reconciled next. A
// commit is the one the current pod's clone reported, "" when there is no pod
// or it has not reported yet, so a replaced pod's commit never lingers.
func (r *Reconciler) writeStatus(ctx context.Context, app *model.App, phase, url string, conds []metav1.Condition, commit string) error {
	next := app.Status
	next.Phase, next.URL, next.ObservedVersion, next.Commit = phase, url, app.Version, commit
	next.Conditions = append([]metav1.Condition(nil), app.Status.Conditions...)
	for _, c := range conds {
		meta.SetStatusCondition(&next.Conditions, c)
	}
	if equality.Semantic.DeepEqual(app.Status, next) {
		return nil
	}
	// Set after the comparison: the size is not stored, so it must not make an
	// otherwise equal status look changed. A custom amount is recorded by its
	// key ("custom:2500m,6442450944").
	size, _ := r.appSize(app)
	next.Size = size.Key()
	err := r.Store.Apps().SetStatus(ctx, app.ID, app.Version, next)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

// Setup wires the controller on d: app rows (and the templates and people
// they depend on) come from the database through feed; changes to their
// workloads come from the executor. It also adds the orphan sweep. The
// caller runs feed.
func (r *Reconciler) Setup(d loop.Driver, feed *dbsource.Feed) error {
	if r.Exec == nil || r.Place == nil || r.Secrets == nil {
		return errors.New("workspace.Reconciler: Exec, Place and Secrets are required")
	}
	if r.Access == nil {
		return errors.New("workspace.Reconciler: Access is required")
	}
	if r.Storage.Homes == nil || r.Storage.Folders == nil {
		return errors.New("workspace.Reconciler: Storage is required")
	}
	if err := d.Add(&sweep.Runner{What: "apps", Sweep: r.Sweep, Every: r.ResyncEvery, Changes: feed.Subscribe()}); err != nil {
		return err
	}
	return d.Reconcile("app", r,
		dbsource.For(feed.Subscribe(), "apps", r.ResyncEvery, r.allApps),
		dbsource.Fan(feed.Subscribe(), "templates", r.appsOfTemplate),
		dbsource.Fan(feed.Subscribe(), "people", r.appsOfPerson),
		dbsource.Fan(feed.Subscribe(), store.TableHomes, r.appsOfHome),
		r.workloads())
}

// workloads feeds the controller the app of every workload the executor
// saw change.
func (r *Reconciler) workloads() loop.Source {
	return loop.SourceFunc(func(ctx context.Context, enqueue func(id string)) error {
		go func() {
			err := r.Exec.Watch(ctx, func(ref workload.Ref) {
				// A workload that lacks the row label names no row.
				if ref.ID != "" {
					enqueue(ref.ID)
				}
			})
			if err != nil && ctx.Err() == nil {
				log.FromContext(ctx).Error(err, "watching the apps' workloads")
			}
		}()
		return nil
	})
}

func appID(a *model.App) string { return a.ID }

// allApps lists every app row, also those marked deleted: they still have
// objects to remove.
func (r *Reconciler) allApps(ctx context.Context) ([]string, error) {
	apps, err := r.Store.Apps().ListAll(ctx)
	return dbsource.IDs(apps, appID, nil), err
}

// appsOfTemplate is every app that runs the template, which just changed.
func (r *Reconciler) appsOfTemplate(ctx context.Context, name string) ([]string, error) {
	apps, err := r.Store.Apps().ListAll(ctx)
	return dbsource.IDs(apps, appID, func(a *model.App) bool { return a.Template == name }), err
}

// appsOfHome is the apps of the home's owner in the home's namespace: a home
// that becomes usable (or stops being) is news for them.
func (r *Reconciler) appsOfHome(ctx context.Context, id string) ([]string, error) {
	home, err := r.Store.Homes().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	apps, err := r.Store.Apps().List(ctx, home.Spec.Owner, true)
	return dbsource.IDs(apps, appID, func(a *model.App) bool { return a.Namespace == home.Namespace }), err
}

// appsOfPerson is the apps of the person whose row just changed: a new uid

// releases the ones waiting for it.
func (r *Reconciler) appsOfPerson(ctx context.Context, id string) ([]string, error) {
	person, err := r.Store.People(store.IDRange{}).ByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	apps, err := r.Store.Apps().List(ctx, person.Name, false)
	return dbsource.IDs(apps, appID, nil), err
}

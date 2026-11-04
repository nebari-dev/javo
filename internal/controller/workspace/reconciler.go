package workspace

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/namespace"
	"github.com/nebari-dev/javo/internal/controller/sweep"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/status"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
)

// Reconciler turns app rows into a Pod and a Service, asks for the owner's
// home (which the storage controller turns into a claim), and writes what it
// observed back to the row's status columns. It never writes spec columns.
type Reconciler struct {
	client.Client
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
	// Machines reads the cluster's nodes: a pod that fits none of them
	// fails as SizeTooBig instead of waiting. Required.
	Machines capacity.Source
	// UIDMode is identity.uidMode; anything but config.UIDModeAllocated runs as FixedUID.
	UIDMode string
	// Exposure decides the app's URL and the base path it is told to serve under.
	Exposure exposure.Backend
	// ManagedNamespacesOnly builds apps only in namespaces labelled
	// javo.nebari.dev/managed=true. Set it wherever the controller sees more
	// than the one workspace namespace: an unlabelled namespace has no Pod
	// Security labels and no gateway-only NetworkPolicy.
	ManagedNamespacesOnly bool
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
	// reads those apps' token Secrets live (Secrets are never cached).
	// APIURL is the UI's public URL, given to those apps as JAVO_URL.
	// Required when any app has API access.
	// Secrets also reads the repository token Secrets of apps from Git.
	Tokens  TokenMinter
	Secrets client.Reader
	APIURL  string
	// CloneImage (git-sync) and RunnerImage (the javo image) run the init
	// containers of apps from Git. Required when any app has a source.
	CloneImage, CloneGitConfig string
	RunnerImage                podspec.Image
	// PixiImage installs the environment of an app that runs in one
	// (internal/pixienv). Required when any app names an environment.
	PixiImage string
	// NebiURL is Nebi's address as apps reach it, set while the platform's
	// Nebi integration is on: apps whose template asks for it then get
	// their owner's Nebi token (WithNebi). Empty gives no app Nebi.
	NebiURL string
	// SecretKeys opens the values of people's secrets, which apps get
	// through a per-app Secret (Secrets reads it). Nil when the controller
	// has no key-encryption key: an app that gets secrets then fails as
	// SecretsUnreadable rather than start without them.
	SecretKeys *secrets.Keys
	// Access decides whether an app's owner may still use its template, by
	// the rule the API applies when the app is made; an app whose owner may
	// not stops (TemplateNotAllowed). Required.
	Access Access
}

// Access decides whether owner may use template t. The jobs controller's
// PeopleAccess, from the owner's person row, is the one the platform uses.
type Access interface {
	MayUse(ctx context.Context, owner string, t *model.Template) (bool, error)
}

// RBAC for the controller (make rbac renders config/rbac/controller; the
// Helm chart's Roles must grant the same rules, test/helm checks it). The
// manager's cache needs list and watch on every type it reads.
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

// unmanagedRetry is how often an app in a not-yet-managed namespace is
// looked at again.
const unmanagedRetry = 10 * time.Second

const indexEventName = "involvedObject.name"

// Reconcile makes the cluster match one app row (the request names its id)
// and records derived status.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	app, err := r.Store.Apps().Get(ctx, req.Name)
	if errors.Is(err, store.ErrNotFound) {
		// Gone: its objects, if any are left, are the sweep's.
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if !app.DeletedAt.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, &app)
	}
	if r.ManagedNamespacesOnly {
		if managed, err := namespace.IsManaged(ctx, r.Client, app.Namespace); err != nil {
			return ctrl.Result{}, err
		} else if !managed {
			log.FromContext(ctx).Info("ignoring app in a namespace the controller does not manage", "namespace", app.Namespace)
			// The namespace may gain the label later, or the cache may lag.
			return ctrl.Result{RequeueAfter: unmanagedRetry}, nil
		}
	}
	owner, short := app.Owner, app.Name
	appURL := r.Exposure.URLFor(owner, short, app.ObjectName())
	url := appURL.String()
	now := time.Now()
	obs := status.Observed{Now: now, ReadyTimeout: r.ReadyTimeout}

	pod, err := r.appPod(ctx, &app)
	if err != nil {
		return ctrl.Result{}, err
	}
	// The token hand-off needs no template, so it runs before any state
	// below (paused, template missing, home conflict) can return early and
	// leave a pasted token in the row.
	gitSecret, gitGen, gitUnreadable, err := r.syncGitToken(ctx, &app, pod)
	if err != nil {
		return ctrl.Result{}, err
	}

	// A restored app waits for an admin: no Pod, and no home claim, which
	// the volume restore may still be bringing back.
	if app.Held(now) {
		if pod != nil {
			if err := r.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
		}
		var res ctrl.Result
		if !app.ReleaseAt.IsZero() {
			res.RequeueAfter = app.ReleaseAt.Sub(now)
		}
		phase, conds, _ := status.Derive(&app, obs)
		return res, r.writeStatus(ctx, &app, phase, url, conds, app.Status.Commit)
	}

	var tpl *model.Template
	if app.Paused {
		if pod != nil {
			if err := r.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
			pod = nil
		}
	} else {
		t, err := r.Store.Templates().Get(ctx, app.Template)
		switch {
		case errors.Is(err, store.ErrNotFound):
			obs.TemplateMissing = true
			phase, conds, _ := status.Derive(&app, obs)
			return ctrl.Result{}, r.writeStatus(ctx, &app, phase, "", conds, "")
		case err != nil:
			return ctrl.Result{}, err
		}
		// Admin templates are stored as written; an empty field means its default.
		t.Spec.ApplyDefaults()
		if t.Spec.JobOnly {
			// The template's command is a placeholder; a pod would exit and
			// crash-loop.
			obs.TemplateError = "the template only runs jobs, so it cannot start an app"
			phase, conds, ev := status.Derive(&app, obs)
			return ctrl.Result{}, r.writeStatus(ctx, &app, phase, "", status.WithEvidence(conds, ev), "")
		}
		switch ok, err := r.Access.MayUse(ctx, app.Owner, &t); {
		case err != nil:
			return ctrl.Result{}, err
		case !ok:
			if pod != nil {
				if err := r.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
					return ctrl.Result{}, err
				}
			}
			obs.TemplateNotAllowed = true
			phase, conds, _ := status.Derive(&app, obs)
			return ctrl.Result{}, r.writeStatus(ctx, &app, phase, "", conds, "")
		}
		tpl = &t
		obs.AppLogin, obs.Builds = t.Spec.AppLogin, t.Spec.Build != nil
		// withEnvironment adds the install step for either (internal/pixienv).
		obs.InstallsEnv = t.Spec.Environment != "" || app.Environment != nil
		if t.Spec.AppLogin == "Disabled" {
			res, err := r.NetworkPolicy.Get(ctx)
			if err != nil {
				return ctrl.Result{}, err
			}
			obs.NetworkPolicy = res.State
		}
	}

	home, err := r.ensureHome(ctx, &app)
	if err != nil {
		return ctrl.Result{}, err
	}
	if c := meta.FindStatusCondition(home.Status.Conditions, model.ConditionReady); c != nil && c.Reason == string(reasons.HomeClaimForeign) {
		obs.HomeConflict = true
		phase, conds, _ := status.Derive(&app, obs)
		return ctrl.Result{}, r.writeStatus(ctx, &app, phase, url, conds, "")
	}
	migrating := meta.IsStatusConditionTrue(home.Status.Conditions, model.ConditionMigrating)
	if tpl != nil && (migrating || meta.IsStatusConditionTrue(home.Status.Conditions, model.ConditionRestoring)) {
		// The copy (or the restore) owns the files until it ends: no app may
		// write to them meanwhile. The home's change brings the app back afterwards.
		if pod != nil {
			if err := r.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
		}
		obs.HomeMigrating, obs.HomeRestoring = migrating, !migrating
		phase, conds, _ := status.Derive(&app, obs)
		return ctrl.Result{}, r.writeStatus(ctx, &app, phase, url, conds, "")
	}
	claim, err := r.homeClaim(ctx, &home)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Like the owner's uid below, the home only matters when a pod is about to
	// be built: a pod that already exists keeps its status while its home is
	// being prepared again.
	if tpl != nil && pod == nil && !home.Status.Prepared {
		obs.HomeReason, obs.HomeEvidence = homeBlock(&home)
		phase, conds, evidence := status.Derive(&app, obs)
		return ctrl.Result{}, r.writeStatus(ctx, &app, phase, url, status.WithEvidence(conds, evidence), "")
	}
	blocked := pod == nil && status.LoginlessBlocked(obs.AppLogin, obs.NetworkPolicy)
	// The layout the pod should have: what it mounts, as which uid. A pod
	// that already exists keeps its status whatever happens to the person,
	// so without their uid it is kept (layoutFP stays empty); a pod about to
	// be built waits for it.
	var layout podstorage.Layout
	var layoutFP string
	if tpl != nil && !blocked {
		uid, ok, err := identity.PodUIDOf(ctx, r.Store.People(store.IDRange{}), r.UIDMode, app.Owner)
		switch {
		case err != nil:
			return ctrl.Result{}, err
		case ok:
			if layout, err = r.layout(ctx, &app, uid); err != nil {
				return ctrl.Result{}, err
			}
			layoutFP = LayoutFingerprint(&app, tpl, layout)
		case pod == nil:
			obs.IdentityPending = true
			phase, conds, _ := status.Derive(&app, obs)
			return ctrl.Result{}, r.writeStatus(ctx, &app, phase, url, conds, "")
		}
	}
	var tokenSecret, access string
	if tpl != nil {
		if err := r.ensureService(ctx, &app, tpl); err != nil {
			return ctrl.Result{}, err
		}
		access = model.APIAccessOf(&app, tpl)
		if scope := token.ScopeFor(access); scope != "" {
			if tokenSecret, err = r.ensureAPIToken(ctx, &app, scope); err != nil {
				return ctrl.Result{}, err
			}
		} else if pod == nil || podAPIAccess(pod) != model.APIAccessNone {
			// Only an app that had access, or whose pod is not built yet,
			// can have a Secret left; others make no call.
			if err := r.removeAPIToken(ctx, &app); err != nil {
				return ctrl.Result{}, err
			}
		}
		if r.NebiURL != "" && model.NebiTokenFor(&app, tpl) {
			if err := r.syncNebiToken(ctx, &app); err != nil {
				return ctrl.Result{}, err
			}
		}
		appSecrets, err := r.secretsOf(ctx, &app)
		if err != nil {
			return ctrl.Result{}, err
		}
		if pod != nil && r.podOutdated(&app, pod, access, gitGen, appSecrets.Fingerprint, layoutFP) {
			// The next reconcile, once the old pod is gone, builds the new one.
			if err := r.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
			phase, conds, _ := status.Derive(&app, obs)
			return ctrl.Result{}, r.writeStatus(ctx, &app, phase, url, conds, "")
		}
		if pod == nil && !blocked {
			size, ok := r.appSize(&app)
			secretsName := AppSecretsName(app.ObjectName())
			unreadable, err := r.writeAppSecrets(ctx, &app, appSecrets)
			if err != nil {
				return ctrl.Result{}, err
			}
			if unreadable == "" {
				unreadable = gitUnreadable
			}
			base, host := r.platform().address(&app)
			if !ok {
				obs.SizeUnknown = app.Size
			} else if unreadable != "" {
				obs.SecretsUnreadable = unreadable
			} else if want, buildErr := BuildPod(&app, tpl, base, host, layout, size, Images{Clone: r.CloneImage, Runner: r.RunnerImage, Pixi: r.PixiImage, GitConfig: r.CloneGitConfig}); buildErr != nil {
				obs.TemplateError = buildErr.Error()
			} else {
				if want.Annotations == nil {
					want.Annotations = map[string]string{}
				}
				want.Annotations[model.AnnotationLayout] = layoutFP
				WithAPIToken(want, tpl, access, tokenSecret, r.APIURL)
				if r.NebiURL != "" && model.NebiTokenFor(&app, tpl) {
					WithNebi(want, app.ObjectName(), r.NebiURL, r.RunnerImage)
				}
				WithGitToken(want, &app, gitSecret, gitGen)
				WithEnv(want, app.Env)
				WithAppSecrets(want, secretsName, appSecrets)
				problem, err := r.withEnvironment(ctx, &app, tpl, want)
				if err != nil {
					return ctrl.Result{}, err
				}
				if problem != "" {
					obs.EnvironmentProblem = problem
				} else if pod, err = r.createPod(ctx, want); err != nil {
					return ctrl.Result{}, err
				}
			}
		}
	}

	if pod != nil {
		if obs.Events, err = r.podEvents(ctx, &app, pod); err != nil {
			return ctrl.Result{}, err
		}
		if obs.Machines, err = r.Machines.Cluster(ctx); err != nil {
			return ctrl.Result{}, err
		}
		size, _ := r.appSize(&app)
		obs.SizeMayWait = size.AllowUnschedulable
	}
	obs.Pod, obs.Claim = pod, claim
	phase, conds, evidence := status.Derive(&app, obs)
	if err := r.writeStatus(ctx, &app, phase, url, status.WithEvidence(conds, evidence), commitOf(pod)); err != nil {
		return ctrl.Result{}, err
	}
	if blocked {
		return ctrl.Result{RequeueAfter: r.policyRecheck()}, nil
	}
	return ctrl.Result{RequeueAfter: r.readyRecheck(pod, phase, now)}, nil
}

// appPod returns the app's Pod, or nil. A Pod of that name that carries
// another app's id (or none) is not the app's and is never touched.
func (r *Reconciler) appPod(ctx context.Context, app *model.App) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	err := r.Get(ctx, client.ObjectKey{Namespace: app.Namespace, Name: app.ObjectName()}, pod)
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, err
	case pod.Labels[model.LabelApp] != app.ID:
		return nil, fmt.Errorf("pod %s/%s exists and does not belong to app %s", pod.Namespace, pod.Name, app.ID)
	}
	return pod, nil
}

// finalize removes what an app marked deleted left in the cluster: its Pod,
// Service and token Secrets, never the owner's home. Once that is done it
// closes the app's open usage interval and removes the row. A failure leaves
// the row, so the next reconcile tries again.
func (r *Reconciler) finalize(ctx context.Context, app *model.App) error {
	key := client.ObjectKey{Namespace: app.Namespace, Name: app.ObjectName()}
	for _, obj := range []client.Object{&corev1.Pod{}, &corev1.Service{}} {
		if err := r.Get(ctx, key, obj); apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			return err
		}
		// Another app's object of the same name is not this app's to delete.
		if obj.GetLabels()[model.LabelApp] != app.ID {
			continue
		}
		if err := r.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	for _, name := range []string{APITokenSecretName(app.ObjectName()), GitTokenSecretName(app.ObjectName()), AppSecretsName(app.ObjectName()), NebiTokenSecretName(app.ObjectName())} {
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: app.Namespace, Name: name}}
		if err := r.Delete(ctx, secret); client.IgnoreNotFound(err) != nil {
			return err
		}
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
	return Platform{Exposure: r.Exposure, PublicHost: r.PublicHost, PixiImage: r.PixiImage}
}

// podOutdated reports whether pod was built with another size, command,
// image, port, Git source, API access, repository token (gitGen, the
// generation of the paste), secrets (their fingerprint) or layout (layoutFP,
// LayoutFingerprint; empty when it cannot be known now) than app asks for,
// or before the app's latest restart request. A pod without the run
// annotation (built before these fields, or for an app that names none)
// matches an app that names none; one without the layout annotation is
// replaced, so group folders and read-only homes hold for every pod.
func (r *Reconciler) podOutdated(app *model.App, pod *corev1.Pod, access, gitGen, secretsFP, layoutFP string) bool {
	return r.sizeChanged(app, pod) || pod.Annotations[model.AnnotationRun] != RunFingerprint(app) ||
		pod.Annotations[model.AnnotationRestart] != RestartMark(app) || podAPIAccess(pod) != access ||
		pod.Annotations[model.AnnotationGitToken] != gitGen || pod.Annotations[model.AnnotationSecrets] != secretsFP ||
		(layoutFP != "" && pod.Annotations[model.AnnotationLayout] != layoutFP)
}

// sizeChanged reports whether pod was built with another size or custom
// amount than app asks for. A pod without a size annotation (built before
// sizes existed) of an app that never picked a size counts as unchanged, so
// upgrading does not restart every app.
func (r *Reconciler) sizeChanged(app *model.App, pod *corev1.Pod) bool {
	have, annotated := pod.Annotations[model.AnnotationSize]
	if !annotated && app.Size == "" && app.Resources == nil {
		return false
	}
	want, _ := r.appSize(app)
	return have != want.Key()
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

// homeClaim returns the claim a home records, or nil while it has none.
func (r *Reconciler) homeClaim(ctx context.Context, home *model.Home) (*corev1.PersistentVolumeClaim, error) {
	if home.Status.ClaimName == "" {
		return nil, nil
	}
	claim := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, client.ObjectKey{Namespace: home.Namespace, Name: home.Status.ClaimName}, claim)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	return claim, err
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

func (r *Reconciler) ensureService(ctx context.Context, app *model.App, tpl *model.Template) error {
	want := BuildService(app, tpl)
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: want.Name, Namespace: want.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = want.Labels
		svc.Spec.Selector = want.Spec.Selector
		ports := make([]corev1.ServicePort, len(want.Spec.Ports))
		for i, p := range want.Spec.Ports {
			p.Protocol = corev1.ProtocolTCP
			ports[i] = p
		}
		svc.Spec.Ports = ports
		return nil
	})
	return err
}

func (r *Reconciler) createPod(ctx context.Context, pod *corev1.Pod) (*corev1.Pod, error) {
	err := r.Create(ctx, pod)
	if apierrors.IsAlreadyExists(err) {
		// The cache lagged behind a create from an earlier reconcile.
		got := &corev1.Pod{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(pod), got); err != nil {
			return nil, err
		}
		return got, nil
	}
	return pod, err
}

// podEvents returns the events about this exact Pod. Matching on UID keeps a
// deleted Pod's events from describing the Pod that replaced it.
func (r *Reconciler) podEvents(ctx context.Context, app *model.App, pod *corev1.Pod) ([]corev1.Event, error) {
	var list corev1.EventList
	if err := r.List(ctx, &list, client.InNamespace(app.Namespace), client.MatchingFields{indexEventName: pod.Name}); err != nil {
		return nil, err
	}
	var out []corev1.Event
	for _, e := range list.Items {
		if e.InvolvedObject.Kind == "Pod" && e.InvolvedObject.UID == pod.UID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (r *Reconciler) policyRecheck() time.Duration {
	if r.PolicyRecheck > 0 {
		return r.PolicyRecheck
	}
	return time.Minute
}

// readyRecheck schedules a reconcile for when a running-but-not-ready pod
// passes ReadyTimeout, so AppNotAnswering appears without any new event.
func (r *Reconciler) readyRecheck(pod *corev1.Pod, phase string, now time.Time) time.Duration {
	if pod == nil || phase != "Starting" || pod.Status.Phase != corev1.PodRunning || r.ReadyTimeout <= 0 {
		return 0
	}
	since := now
	if pod.Status.StartTime != nil {
		since = pod.Status.StartTime.Time
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if run := cs.State.Running; run != nil {
			since = run.StartedAt.Time
			break
		}
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

// SetupWithManager wires the controller: app rows (and the templates and
// people they depend on) come from the database through feed; Pods, Services
// and Events about them come from the cluster and map back to their app by
// the id label. It also adds the orphan sweep. The caller runs feed.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	if r.Machines == nil {
		return errors.New("workspace.Reconciler: Machines is required")
	}
	if r.Access == nil {
		return errors.New("workspace.Reconciler: Access is required")
	}
	if r.Storage.Homes == nil || r.Storage.Folders == nil {
		return errors.New("workspace.Reconciler: Storage is required")
	}
	byAppLabel := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		return dbsource.Request(o.GetLabels()[model.LabelApp])
	})
	podEvents := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		e, ok := o.(*corev1.Event)
		if !ok || e.InvolvedObject.Kind != "Pod" {
			return nil
		}
		pod := &corev1.Pod{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: e.Namespace, Name: e.InvolvedObject.Name}, pod); err != nil {
			return nil
		}
		return dbsource.Request(pod.Labels[model.LabelApp])
	})
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Event{}, indexEventName, func(o client.Object) []string {
		return []string{o.(*corev1.Event).InvolvedObject.Name}
	}); err != nil {
		return err
	}
	if err := mgr.Add(&sweep.Runner{What: "apps", Sweep: r.Sweep, Every: r.ResyncEvery, Changes: feed.Subscribe()}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).Named("app").
		WatchesRawSource(dbsource.For(feed.Subscribe(), "apps", r.ResyncEvery, r.allApps)).
		WatchesRawSource(dbsource.Fan(feed.Subscribe(), "templates", r.appsOfTemplate)).
		WatchesRawSource(dbsource.Fan(feed.Subscribe(), "people", r.appsOfPerson)).
		WatchesRawSource(dbsource.Fan(feed.Subscribe(), store.TableHomes, r.appsOfHome)).
		Watches(&corev1.Pod{}, byAppLabel).
		Watches(&corev1.Service{}, byAppLabel).
		Watches(&corev1.Event{}, podEvents, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
			e, ok := o.(*corev1.Event)
			return ok && e.InvolvedObject.Kind == "Pod"
		}))).
		Complete(r)
}

func appID(a *model.App) string { return a.ID }

// allApps lists every app row, also those marked deleted: they still have
// objects to remove.
func (r *Reconciler) allApps(ctx context.Context) ([]reconcile.Request, error) {
	apps, err := r.Store.Apps().ListAll(ctx)
	return dbsource.Requests(apps, appID, nil), err
}

// appsOfTemplate is every app that runs the template, which just changed.
func (r *Reconciler) appsOfTemplate(ctx context.Context, name string) ([]reconcile.Request, error) {
	apps, err := r.Store.Apps().ListAll(ctx)
	return dbsource.Requests(apps, appID, func(a *model.App) bool { return a.Template == name }), err
}

// appsOfHome is the apps of the home's owner in the home's namespace: a home
// that becomes usable (or stops being) is news for them.
func (r *Reconciler) appsOfHome(ctx context.Context, id string) ([]reconcile.Request, error) {
	home, err := r.Store.Homes().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	apps, err := r.Store.Apps().List(ctx, home.Spec.Owner, true)
	return dbsource.Requests(apps, appID, func(a *model.App) bool { return a.Namespace == home.Namespace }), err
}

// appsOfPerson is the apps of the person whose row just changed: a new uid

// releases the ones waiting for it.
func (r *Reconciler) appsOfPerson(ctx context.Context, id string) ([]reconcile.Request, error) {
	person, err := r.Store.People(store.IDRange{}).ByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	apps, err := r.Store.Apps().List(ctx, person.Name, false)
	return dbsource.Requests(apps, appID, nil), err
}

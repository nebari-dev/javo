package servercmd

import (
	"bytes"
	"context"
	"fmt"
	"github.com/nebari-dev/javo/internal/buildinfo"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/nebari-dev/javo/internal/activity"
	"github.com/nebari-dev/javo/internal/ai/aisettings"
	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/cli/wiring"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/features/authlocal"
	"github.com/nebari-dev/javo/internal/gateway"
	"github.com/nebari-dev/javo/internal/gateway/admin"
	"github.com/nebari-dev/javo/internal/gateway/apps"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/branding"
	"github.com/nebari-dev/javo/internal/gateway/proxy"
	"github.com/nebari-dev/javo/internal/plans"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/templates"
)

// RBAC for the gateway (make rbac renders config/rbac/gateway; the Helm
// chart's Roles must grant the same rules, test/helm checks it). Apps,
// templates, sharing and jobs are rows of the database: the gateway writes
// nothing about them to Kubernetes.
// Accounts, sessions and API tokens are rows of the database. The only
// Secrets are the gateway's sessions key and the secrets' fingerprint key in
// the release namespace, read by name and made on the first start (create
// cannot be narrowed by name; nothing changes or deletes a Secret).
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get,resourceNames=javo-sessions-key;javo-secret-fingerprint-key;javo-backup-password
// +kubebuilder:rbac:groups="",resources=secrets,verbs=create
// File picker pods: created, read, reaped and released; their events and
// the owner's home claim are read, and so are the pods of runs and apps.
// Each file pod's password is a Secret the gateway creates (create only, by
// the marker above; it never reads one there) and the pod owns.
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;create;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=list
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get
// App logs: the API reads the app container log (and the claim and pod
// events above) of the caller's own app.
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// Namespaces: when apps live in per-person namespaces the gateway finds them by
// the label the controller puts on each.
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list
// Nodes, read only, into a cache trimmed to their allocatable CPU and
// memory: they mark the sizes no machine can hold.
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

// gatewayOptions are the gateway's own flags; the ones it shares with the
// controller are in commonFlags.
type gatewayOptions struct {
	listen, uiDir, filesImage, templatesDir string
	// internalHost is the gateway's Service host, for pods calling the API.
	internalHost string
	// sessionsKeySecret names the Secret in the release namespace with the
	// key the sessions' provider tokens are sealed with; sessionsKeyCreate
	// makes it.
	sessionsKeySecret string
	sessionsKeyCreate bool
	filesIdleAfter    time.Duration
	trustedProxies    []string
	proxySecretFile   string
	secure            bool
	// backups are the server's own SQLite backups, set by javo server; the
	// gateway alone (Postgres) has none of its own.
	backups store.BackupPlan
	// carried is set by `javo local` and nil otherwise.
	carried *carried
}

// carried is what the gateway is given in place of flags and folders by a
// binary that carries its files and serves one person on their own
// machine (`javo local`). It is the one thing the gateway does
// differently there, and every field's zero value is what `javo server`
// does.
type carried struct {
	// listener is the loopback port already listened on, in place of
	// --listen.
	listener net.Listener
	// ui is the built UI in place of --ui-dir, and templates the built-in
	// template files in place of --templates-dir.
	ui, templates fs.FS
	// link signs the one account in through a link.
	link *authlocal.Link
	// ready is called once, when everything is set up, with the handler
	// about to be served, and returns the one to serve.
	ready func(http.Handler) http.Handler
}

func (o *gatewayOptions) register(f *pflag.FlagSet) {
	f.StringVar(&o.listen, "listen", ":8080", "address to listen on")
	f.StringVar(&o.uiDir, "ui-dir", "/ui", "directory with the built UI")
	f.StringVar(&o.internalHost, "internal-host", "", "the gateway's Service host inside the cluster (such as javo-gateway.javo-system.svc): /api/ answers there as on the UI host, for pods whose public address is out of reach")
	f.StringVar(&o.filesImage, "files-image", defaultFilesImage, "rclone image for file picker pods, pinned by digest")
	f.DurationVar(&o.filesIdleAfter, "files-idle-after", 10*time.Minute,
		"how long a user's file picker pod is kept after their last request from the UI")
	f.StringVar(&o.templatesDir, "templates-dir", "", "directory of built-in template files (*.yaml) to install into the database at start")
	f.StringSliceVar(&o.trustedProxies, "trusted-proxies", nil,
		"CIDRs of reverse proxies whose X-Forwarded-For is believed for the client address (login rate limits)")
	f.StringVar(&o.proxySecretFile, "trusted-proxy-secret-file", "",
		"file holding the secret trusted proxies send in "+gateway.ProxySecretHeader+"; set, X-Forwarded-For is believed only on requests that carry it")
	f.BoolVar(&o.secure, "secure-cookies", false, "mark the session cookie Secure even when urls.scheme is http (it always is with https)")
	f.StringVar(&o.sessionsKeySecret, "sessions-key-secret", "", "Secret in the release namespace whose key \"key\" holds the 32-byte key that seals each session's identity provider tokens in the database, read at start; empty: a key made at start, so a restart signs everyone out")
	f.BoolVar(&o.sessionsKeyCreate, "sessions-key-create", false, "make the --sessions-key-secret Secret, immutable with a new random key, when it does not exist; never changed afterwards")
}

func NewGateway() *cobra.Command {
	var common commonFlags
	var opts gatewayOptions
	var runnerImage string
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Serve the API, the UI and the app proxy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			platform, err := common.load(ctx)
			if err != nil {
				return err
			}
			db, err := common.openDB(ctx, platform)
			if err != nil {
				return err
			}
			defer db.Close()
			if err := needsCluster(platform, "javo gateway"); err != nil {
				return err
			}
			return opts.run(ctx, &common, platform, db, nil)
		},
	}
	common.register(cmd.Flags())
	opts.register(cmd.Flags())
	cmd.Flags().StringVar(&runnerImage, "runner-image", "", "unused: the controller builds job pods")
	_ = cmd.Flags().MarkDeprecated("runner-image", "the gateway no longer renders jobs")
	return cmd
}

// run serves on db until ctx ends, for apps that run on host, or on the
// cluster when host is nil.
func (o *gatewayOptions) run(ctx context.Context, common *commonFlags, platform config.Platform, db *store.Store, host *wiring.Host) error {
	trusted, err := parseTrustedProxies(o.trustedProxies)
	if err != nil {
		return err
	}
	appSizes, err := sizes.Load(common.sizesFile)
	if err != nil {
		return err
	}
	look, err := branding.Load(platform.Branding)
	if err != nil {
		return err
	}
	if buildinfo.Local && platform.MCP.Enabled {
		// Refused before anything is started; the endpoint itself is in a
		// file that build leaves out.
		return buildinfo.LeftOut("the MCP endpoint (mcp.enabled)")
	}
	pl := wiring.Placement(platform)
	on, err := o.parts(ctx, common, platform, db, pl, host)
	if err != nil {
		return err
	}
	var own carried
	if o.carried != nil {
		own = *o.carried
	}
	builtin := own.templates
	if builtin == nil && o.templatesDir != "" {
		builtin = os.DirFS(o.templatesDir)
	}
	if builtin != nil {
		if err := templates.Sync(ctx, db, builtin); err != nil {
			return fmt.Errorf("install built-in templates: %w", err)
		}
	}
	bkt, err := wiring.Bucket(platform, db, o.backups)
	if err != nil {
		return err
	}
	sessionsKey := on.sessionsKey
	sessions := &session.DBStore{Sessions: db.Sessions(), Key: sessionsKey}
	// Webhook secrets are sealed with the same key (each bound to its hook),
	// so a copy of the database cannot sign deliveries.
	db.SetHookKey(sessionsKey)
	tokens := &token.DBStore{Tokens: db.Tokens()}
	go sessions.RunJanitor(ctx, platform.Sessions.SweepEvery.Duration)
	go tokens.RunJanitor(ctx, platform.Sessions.SweepEvery.Duration, apps.AppTokenWanted(db))
	go authn.RunDeviceJanitor(ctx, db, platform.Sessions.SweepEvery.Duration)
	var outageGrace time.Duration
	if oidc := platform.Auth.OIDC; oidc != nil {
		outageGrace = oidc.OutageGrace.Duration
	}
	fp := on.fingerprinter
	aiLive := wiring.AISettings(platform.AI, db, sessionsKey)
	aiSvc, err := wiring.AIService(ctx, platform.AI, aiLive, db, fp)
	if err != nil {
		return err
	}
	proposals := &plans.Service{DB: db, TTL: platform.Plans.TTL.Duration}
	go proposals.RunJanitor(ctx, time.Minute)
	go apps.TrustJanitor{DB: db, Policy: apps.TrustPolicy{Config: platform.Agents.TrustedAllowed(), DB: db}, Now: time.Now}.Run(ctx, time.Minute)
	exp := wiring.Exposure(platform)
	workloads := on.apps
	providers := wiring.AuthProviders(platform, &local.DBUsers{Accounts: db.LocalAccounts()}, own.link)
	people := on.people
	revalidator := authn.NewRevalidator(providers, sessions, people, outageGrace, nil)
	// One keeper for the API (create, start) and the app proxy, so they
	// share its throttle.
	nebi := wiring.NebiTokens(platform, db)
	srv := gateway.NewServer(gateway.Config{
		Cluster:           on.cluster,
		Store:             db,
		Tokens:            tokens,
		API:               platform.API,
		Exposure:          exp,
		PublicHost:        common.publicHost,
		PixiImage:         platform.Environments.PixiImage,
		LockAgents:        wiring.LockAgents(platform),
		Providers:         providers,
		Revalidator:       revalidator,
		People:            people,
		Roles:             platform.Roles,
		URLs:              platform.URLs,
		Sharing:           platform.Sharing,
		StaleAfter:        platform.Identity.StaleAfter.Duration,
		Sessions:          sessions,
		Placement:         pl,
		SecureCookies:     o.secure,
		TrustedProxies:    trusted,
		UIDir:             o.uiDir,
		UI:                own.ui,
		Branding:          look,
		Homes:             on.homes,
		Usage:             on.usage,
		Groups:            ids.PersonGroups{People: db.People(store.IDRange{})},
		Storage:           platform.Storage.Spec(),
		Executor:          workloads,
		Runs:              on.runs,
		PreviewsEnabled:   platform.Source.Previews.Enabled,
		Thumbnails:        platform.Thumbnails.Enabled,
		PreviewsMaxPerApp: platform.Source.Previews.MaxPerApp,
		CodeHosts:         wiring.CodeHosts(platform),
		Sizes:             appSizes,
		Machines:          on.machines,
		Engines:           wiring.Engines(),
		ContainerImages:   platform.ContainerImages,
		IdleStop:          platform.IdleStop,
		NetworkPolicy:     on.policy,
		Ready:             db.Ready,
		Backups: admin.BackupConfig{
			Engine: common.database.Engine, Dir: o.backups.Dir, Schedule: o.backups.Schedule, Keep: o.backups.Keep, Bucket: bkt,
		},
		Diagnose:        wiring.DiagnoseEngine(),
		Plans:           proposals,
		AI:              aiSvc,
		AgentModel:      wiring.AgentModel(ctx, platform.AI, aiLive),
		AISettings:      aiLive,
		InternalHost:    on.internalHost,
		Agent:           apps.AgentConfig{Model: agentModelOf(aiLive), Template: platform.AI.Agent.Template},
		AIFeatures:      platform.AI.Features,
		AIIncludeOwners: platform.AI.Admin.IncludeOwners,
		MCP:             platform.MCP,
		TrustedAgents:   platform.Agents.TrustedAllowed(),
		Environments:    wiring.EnvironmentSource(platform),
		NebiTokens:      nebi,
	})
	// Use of each app is kept in memory and written every
	// activity.FlushEvery, never per request; the controller reads the
	// latest any replica wrote.
	used := activity.NewTracker(nil)
	go used.Run(ctx, db.Apps(), activity.FlushEvery)
	srv.Apps(proxy.New(proxy.Config{
		Store: db, Sessions: sessions, Activity: used,
		Exposure: exp, Roles: platform.Roles, URLs: platform.URLs, Sharing: platform.Sharing, Revalidator: revalidator,
		NebiTokens: nebi, SecureCookies: o.secure,
		Resolve: proxy.ResolveAt(exp, workloads.Addr),
		// Every replica has the sessions key, so any of them reads the
		// route cookie another one set.
		Seal: sessionsKey,
	}))
	var handler http.Handler = srv
	if o.proxySecretFile != "" {
		secret, err := readProxySecret(o.proxySecretFile)
		if err != nil {
			return err
		}
		handler = gateway.RequireProxySecret(secret, srv)
	}
	if own.ready != nil {
		handler = own.ready(handler)
	}
	return serve(ctx, o.listen, own.listener, handler)
}

// minProxySecret is the shortest proxy secret accepted: it must not be
// guessable by the traffic it keeps out.
const minProxySecret = 32

// readProxySecret reads the trusted proxies' secret, without surrounding
// white space.
func readProxySecret(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--trusted-proxy-secret-file: %w", err)
	}
	b = bytes.TrimSpace(b)
	if len(b) < minProxySecret {
		return nil, fmt.Errorf("--trusted-proxy-secret-file: the secret has %d characters, want at least %d", len(b), minProxySecret)
	}
	return b, nil
}

// parseTrustedProxies parses CIDRs such as 10.42.0.1/32.
func parseTrustedProxies(in []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(in))
	for _, s := range in {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("--trusted-proxies: %q is not a CIDR such as 10.42.0.1/32", s)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// defaultFilesImage is rclone 1.75.2, pinned by digest. Its known advisories
// and the mitigations that make them acceptable are in DEPENDENCIES.md.
const defaultFilesImage = "rclone/rclone:1.75.2@sha256:2687085f718d3c628f7fdfb77c52a1d344332aed543110bb88088f2c70d43eb5"

// serve runs h on addr, or on l when it is already listened on, until ctx
// ends, then drains for up to 10s.
func serve(ctx context.Context, addr string, l net.Listener, h http.Handler) error {
	hs := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	errc := make(chan error, 1)
	go func() {
		if l != nil {
			errc <- hs.Serve(l)
			return
		}
		errc <- hs.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return hs.Shutdown(shutdown)
}

// uidRange is the range new people get a uid from: the configured one when
// uids are allocated, none when every pod runs as one fixed uid.
func uidRange(c config.IdentityCfg) store.IDRange {
	if c.UIDMode != config.UIDModeAllocated {
		return store.IDRange{}
	}
	return store.IDRange{First: c.UIDRange.First, Last: c.UIDRange.Last}
}

// agentModelOf is the agent model of the AI settings in force; nil (AI off)
// is always off.
func agentModelOf(l *aisettings.Live) func(context.Context) string {
	if l == nil {
		return nil
	}
	return l.AgentModel
}

package servercmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/tink-crypto/tink-go/v2/tink"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/nebari-dev/javo/internal/activity"
	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/cli/wiring"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/secretskey"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway"
	"github.com/nebari-dev/javo/internal/gateway/admin"
	"github.com/nebari-dev/javo/internal/gateway/apps"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/branding"
	"github.com/nebari-dev/javo/internal/gateway/namespaces"
	"github.com/nebari-dev/javo/internal/gateway/podlogs"
	"github.com/nebari-dev/javo/internal/gateway/proxy"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/plans"
	"github.com/nebari-dev/javo/internal/secrets"
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
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get,resourceNames=javo-sessions-key;javo-secret-fingerprint-key
// +kubebuilder:rbac:groups="",resources=secrets,verbs=create
// File picker pods: created, read, reaped and released; their events and
// the owner's home claim are read, and so are the pods of runs and apps.
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
	// sessionsKeySecret names the Secret in the release namespace with the
	// key the sessions' provider tokens are sealed with; sessionsKeyCreate
	// makes it.
	sessionsKeySecret string
	sessionsKeyCreate bool
	filesIdleAfter    time.Duration
	trustedProxies    []string
	secure            bool
	// backups are the server's own SQLite backups, set by javo server; the
	// gateway alone (Postgres) has none of its own.
	backups store.BackupPlan
}

func (o *gatewayOptions) register(f *pflag.FlagSet) {
	f.StringVar(&o.listen, "listen", ":8080", "address to listen on")
	f.StringVar(&o.uiDir, "ui-dir", "/ui", "directory with the built UI")
	f.StringVar(&o.filesImage, "files-image", defaultFilesImage, "rclone image for file picker pods, pinned by digest")
	f.DurationVar(&o.filesIdleAfter, "files-idle-after", 10*time.Minute,
		"how long a user's file picker pod is kept after their last request from the UI")
	f.StringVar(&o.templatesDir, "templates-dir", "", "directory of built-in template files (*.yaml) to install into the database at start")
	f.StringSliceVar(&o.trustedProxies, "trusted-proxies", nil,
		"CIDRs of reverse proxies whose X-Forwarded-For is believed for the client address (login rate limits)")
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
			platform, err := common.loadConfig()
			if err != nil {
				return err
			}
			db, err := common.database.Open(ctx)
			if err != nil {
				return err
			}
			defer db.Close()
			return opts.run(ctx, &common, platform, db)
		},
	}
	common.register(cmd.Flags())
	opts.register(cmd.Flags())
	cmd.Flags().StringVar(&runnerImage, "runner-image", "", "unused: the controller builds job pods")
	_ = cmd.Flags().MarkDeprecated("runner-image", "the gateway no longer renders jobs")
	return cmd
}

// run serves on db until ctx ends.
func (o *gatewayOptions) run(ctx context.Context, common *commonFlags, platform config.Platform, db *store.Store) error {
	wsNamespace, namespace := common.workspaceNamespace, common.namespace
	if namespace == "" {
		return errors.New("--namespace is required (or set POD_NAMESPACE)")
	}
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
	restCfg, err := ctrlconfig.GetConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return err
	}
	scheme, err := gatewayScheme()
	if err != nil {
		return err
	}
	c, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	machines, err := capacity.Watch(ctx, restCfg)
	if err != nil {
		return fmt.Errorf("watch nodes: %w", err)
	}
	if o.templatesDir != "" {
		if err := templates.Sync(ctx, db, os.DirFS(o.templatesDir)); err != nil {
			return fmt.Errorf("install built-in templates: %w", err)
		}
	}
	bkt, err := wiring.Bucket(platform, db, o.backups)
	if err != nil {
		return err
	}
	sessionsKey, err := o.sessionsKeyOf(ctx, c, namespace)
	if err != nil {
		return err
	}
	sessions := &session.DBStore{Sessions: db.Sessions(), Key: sessionsKey}
	tokens := &token.DBStore{Tokens: db.Tokens()}
	pl := wiring.Placement(platform)
	// c reads straight from the API server (no cache), as files.Pods needs.
	pods, err := files.NewPods(files.PodsConfig{
		Client: c, Namespace: wsNamespace, Image: o.filesImage, IdleAfter: o.filesIdleAfter,
		Storage: wiring.StorageResolver(db, platform),
		ReapIn:  namespaces.New(pl, c).All,
		// One namespace per person: sweep with one cluster-wide call.
		ReapAcrossNamespaces: len(pl.Desired()) == 0,
	})
	if err != nil {
		return err
	}
	go pods.Run(ctx)
	go sessions.RunJanitor(ctx, platform.Sessions.SweepEvery.Duration)
	go tokens.RunJanitor(ctx, platform.Sessions.SweepEvery.Duration, apps.AppTokenWanted(db))
	var outageGrace time.Duration
	if oidc := platform.Auth.OIDC; oidc != nil {
		outageGrace = oidc.OutageGrace.Duration
	}
	fp, err := common.fingerprinter(ctx, c)
	if err != nil {
		return err
	}
	aiSvc, err := wiring.AIService(ctx, platform.AI, db, fp)
	if err != nil {
		return err
	}
	proposals := &plans.Service{DB: db, TTL: platform.Plans.TTL.Duration}
	go proposals.RunJanitor(ctx, time.Minute)
	exp := wiring.Exposure(platform)
	providers := wiring.AuthProviders(platform, &local.DBUsers{Accounts: db.LocalAccounts()})
	people := db.People(uidRange(platform.Identity))
	revalidator := authn.NewRevalidator(providers, sessions, people, outageGrace, nil)
	lister := files.NewLister(pods, files.Rclone{})
	// One keeper for the API (create, start) and the app proxy, so they
	// share its throttle.
	nebi := wiring.NebiTokens(platform, db)
	srv := gateway.NewServer(gateway.Config{
		Client:            c,
		Store:             db,
		Tokens:            tokens,
		API:               platform.API,
		Exposure:          exp,
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
		Branding:          look,
		Homes:             lister,
		Usage:             lister,
		Groups:            ids.PersonGroups{People: db.People(store.IDRange{})},
		Storage:           platform.Storage.Spec(),
		Logs:              podlogs.Clientset{Clientset: clientset},
		PreviewsEnabled:   platform.Source.Previews.Enabled,
		Thumbnails:        platform.Thumbnails.Enabled,
		PreviewsMaxPerApp: platform.Source.Previews.MaxPerApp,
		CodeHosts:         wiring.CodeHosts(platform),
		Sizes:             appSizes,
		Machines:          machines,
		Engines:           wiring.Engines(),
		ContainerImages:   platform.ContainerImages,
		IdleStop:          platform.IdleStop,
		NetworkPolicy:     &netprobe.Store{Facts: db.Facts()},
		Ready:             db.Ready,
		Backups: admin.BackupConfig{
			Engine: common.database.Engine, Dir: o.backups.Dir, Schedule: o.backups.Schedule, Keep: o.backups.Keep, Bucket: bkt,
		},
		Diagnose:        wiring.DiagnoseEngine(),
		Plans:           proposals,
		AI:              aiSvc,
		AIFeatures:      platform.AI.Features,
		AIIncludeOwners: platform.AI.Admin.IncludeOwners,
		MCP:             platform.MCP,
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
		Exposure: exp, Roles: platform.Roles, URLs: platform.URLs, Revalidator: revalidator,
		NebiTokens: nebi, SecureCookies: o.secure,
	}))
	return serve(ctx, o.listen, srv)
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

func gatewayScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	return scheme, corev1.AddToScheme(scheme)
}

// serve runs h on addr until ctx ends, then drains for up to 10s.
func serve(ctx context.Context, addr string, h http.Handler) error {
	hs := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
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

// sessionsKeyOf reads the --sessions-key-secret Secret through c (making it
// first with --sessions-key-create), or makes a key that lives as long as
// the process when there is none: the provider's tokens are never written
// in clear either way.
func (o *gatewayOptions) sessionsKeyOf(ctx context.Context, c client.Client, namespace string) (tink.AEAD, error) {
	if o.sessionsKeySecret == "" {
		slog.Warn("no --sessions-key-secret: sessions are sealed with a key made now, so a restart signs everyone out")
		return secrets.NewKey()
	}
	b, err := secretskey.Secret{
		Client: c, Namespace: namespace, Name: o.sessionsKeySecret, Key: keySecretKey,
		Component: "sessions-key", Create: o.sessionsKeyCreate,
	}.Bytes(ctx)
	if err != nil {
		return nil, err
	}
	k, err := secrets.KEKFromBytes(b)
	if err != nil {
		return nil, fmt.Errorf("the sessions key in Secret %s/%s: %w", namespace, o.sessionsKeySecret, err)
	}
	return k, nil
}

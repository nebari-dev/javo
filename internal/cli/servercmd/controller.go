package servercmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	ctrlmanager "sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/cli/wiring"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/backupclaim"
	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/idle"
	"github.com/nebari-dev/javo/internal/controller/jobs"
	"github.com/nebari-dev/javo/internal/controller/manager"
	"github.com/nebari-dev/javo/internal/controller/namespace"
	"github.com/nebari-dev/javo/internal/controller/preview"
	"github.com/nebari-dev/javo/internal/controller/secretskey"
	"github.com/nebari-dev/javo/internal/controller/storage"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/features/placementperteam"
	"github.com/nebari-dev/javo/internal/features/placementperuser"
	"github.com/nebari-dev/javo/internal/features/placementshared"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/store"
)

// netprobeFlags are the controller's settings for the NetworkPolicy probe.
type netprobeFlags struct {
	controlURL string
	every      time.Duration
	timeout    time.Duration
}

// controllerOptions are the controller's own flags; the ones it shares with
// the gateway are in commonFlags.
type controllerOptions struct {
	gatewayNS    string
	gatewayPods  map[string]string
	networkPol   bool
	egressDeny   []string
	egressAllow  []string
	egressAllowC []string
	egressAllowN []string
	appBindings  []string
	metricsAddr  string
	probeAddr    string
	readyTimeout time.Duration
	runnerImage  string
	// pullSecrets name the Secrets the runner image is pulled with; the
	// namespace reconciler copies them from the release namespace.
	pullSecrets []string
	probe       netprobeFlags
	notif       notificationFlags
	// secretsKey is the key-encryption key file of people's secrets.
	secretsKey string
	// secretsKeySecret names the Secret in the release namespace holding
	// that key instead; secretsKeyCreate makes it when it does not exist.
	secretsKeySecret string
	secretsKeyCreate bool
	// backupClaim is the Postgres backup claim the controller makes in the
	// release namespace; empty makes none.
	backupClaim backupClaimFlags
}

type backupClaimFlags struct {
	name         string
	size         string
	storageClass string
}

func (o *controllerOptions) register(f *pflag.FlagSet) {
	f.StringVar(&o.gatewayNS, "gateway-namespace", os.Getenv("POD_NAMESPACE"), "namespace of the gateway pods, the only ones allowed to reach apps")
	f.StringToStringVar(&o.gatewayPods, "gateway-pod-labels", nil, "labels of the gateway pods (key=value,...)")
	f.BoolVar(&o.networkPol, "network-policy", true, "keep the default-deny NetworkPolicies in every workspace namespace")
	f.StringSliceVar(&o.egressDeny, "egress-deny-cidrs", []string{"169.254.169.254/32", "fd00:ec2::254/128", "100.100.100.200/32", "168.63.129.16/32"},
		"address ranges app and job pods may not reach, besides the private ranges; empty denies only those")
	f.StringSliceVar(&o.egressAllow, "egress-allow-cidrs", nil,
		"if set, the only address ranges app and job pods may reach instead of the public internet, besides DNS, the gateway, their own namespace and the allowed cluster ranges and namespaces")
	f.StringSliceVar(&o.egressAllowC, "egress-allow-cluster-cidrs", nil,
		"private address ranges app and job pods may reach anyway (less --egress-deny-cidrs)")
	f.StringSliceVar(&o.egressAllowN, "egress-allow-namespaces", nil,
		"namespaces whose pods app and job pods may reach")
	f.StringSliceVar(&o.appBindings, "app-role-binding", nil,
		"bind a ClusterRole to a service account in every workspace namespace (clusterrole=namespace/serviceaccount, repeatable)")
	f.StringVar(&o.metricsAddr, "metrics-bind-address", ":8081", "metrics endpoint address")
	f.StringVar(&o.probeAddr, "health-probe-bind-address", ":8082", "health probe address")
	f.DurationVar(&o.readyTimeout, "ready-timeout", 3*time.Minute, "how long a running app may stay unready before it is reported as not answering")
	f.StringVar(&o.runnerImage, "runner-image", "", "image whose /javo binary job pods copy in to run the job runner (the chart's own image); required to run the controller")
	f.StringSliceVar(&o.pullSecrets, "image-pull-secret", nil, "Secret in the release namespace the runner image is pulled with; copied into every namespace the controller runs pods in (repeatable)")
	f.StringVar(&o.notif.namespace, "notifications-namespace", "", "namespace holding the failure-notice ConfigMap and Secret; empty turns failure notices off")
	f.StringVar(&o.notif.configMap, "notifications-config-map", "javo-notifications", "ConfigMap with notifications-engine services, templates, triggers and subscriptions")
	f.StringVar(&o.notif.secret, "notifications-secret", "javo-notifications", "Secret with the service credentials the ConfigMap refers to as $key")
	f.StringVar(&o.probe.controlURL, "netprobe-control-url", "", "URL of the gateway's health endpoint; the NetworkPolicy probe's dialer must reach it; required to run the controller")
	f.StringVar(&o.secretsKey, "secrets-key-file", "", "file holding the 32-byte key-encryption key of people's secrets; empty (and no --secrets-key-secret): apps that get secrets do not start")
	f.StringVar(&o.secretsKeySecret, "secrets-key-secret", "", "Secret in the release namespace whose key kek holds the 32-byte key-encryption key of people's secrets, read at start")
	f.BoolVar(&o.secretsKeyCreate, "secrets-key-create", false, "make the --secrets-key-secret Secret, immutable with a new random key, when it does not exist; never changed afterwards")
	f.DurationVar(&o.probe.every, "netprobe-interval", 6*time.Hour, "how often the NetworkPolicy enforcement probe runs (it also runs at start)")
	f.StringVar(&o.backupClaim.name, "backup-claim", "", "make this claim in the release namespace, if missing, for the Postgres backup CronJob's dumps; never changed or deleted after; empty makes none")
	f.StringVar(&o.backupClaim.size, "backup-claim-size", "5Gi", "size of the --backup-claim claim")
	f.StringVar(&o.backupClaim.storageClass, "backup-claim-storage-class", "", "storage class of the --backup-claim claim; empty uses the cluster default")
	f.DurationVar(&o.probe.timeout, "netprobe-timeout", 2*time.Minute, "how long one probe may take before its result is Unknown")
}

func NewController() *cobra.Command {
	var common commonFlags
	var opts controllerOptions
	cmd := &cobra.Command{
		Use:   "controller",
		Short: "Run the workspace controller",
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx := ctrl.SetupSignalHandler()
			platform, err := common.loadConfig()
			if err != nil {
				return err
			}
			db, err := common.openDB(ctx, platform)
			if err != nil {
				return err
			}
			defer db.Close()
			return opts.run(ctx, &common, platform, db)
		},
	}
	common.register(cmd.Flags())
	opts.register(cmd.Flags())
	return cmd
}

// run runs the controller on db until ctx ends.
func (o *controllerOptions) run(ctx context.Context, common *commonFlags, platform config.Platform, db *store.Store) error {
	wsNamespace, releaseNS := common.workspaceNamespace, common.namespace
	if o.runnerImage == "" || o.probe.controlURL == "" {
		return errors.New("the controller needs --runner-image and --netprobe-control-url")
	}
	if o.networkPol && (o.gatewayNS == "" || len(o.gatewayPods) == 0) {
		return errors.New("--network-policy needs --gateway-namespace (or POD_NAMESPACE) and --gateway-pod-labels")
	}
	if releaseNS == "" {
		return errors.New("--namespace is required (or set POD_NAMESPACE)")
	}
	appSizes, err := sizes.Load(common.sizesFile)
	if err != nil {
		return err
	}
	bindings, err := parseAppRoleBindings(o.appBindings)
	if err != nil {
		return err
	}
	egress, err := namespace.ParseEgress(o.egressDeny, o.egressAllow, o.egressAllowC)
	if err != nil {
		return err
	}
	egress.AllowNamespaces = o.egressAllowN
	// Pods get the name servers this pod has (kubelet's --cluster-dns).
	if egress.NameServers, err = namespace.ReadNameServers("/etc/resolv.conf"); err != nil {
		return err
	}
	backupClaimSize, err := resource.ParseQuantity(o.backupClaim.size)
	if err != nil {
		return fmt.Errorf("--backup-claim-size: %w", err)
	}
	ctrl.SetLogger(zap.New())
	keys, err := o.secretKeys(ctx, db, releaseNS)
	if err != nil {
		return err
	}
	var fp *secrets.Fingerprinter
	if common.fingerprintKeySecret != "" {
		c, err := startClient()
		if err != nil {
			return err
		}
		if fp, err = common.fingerprinter(ctx, c); err != nil {
			return err
		}
	}
	if keys != nil && fp != nil {
		go keys.RunFingerprints(ctx, fp, fingerprintEvery)
	}
	// Every reconciler that reads the database hears about its changes
	// through one copy of this feed each.
	if err := db.StartFeed(ctx); err != nil {
		return fmt.Errorf("listen for database changes: %w", err)
	}
	feed := dbsource.NewFeed(db.Changes())
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	// Shared mode watches the one namespace; the others watch them all.
	shared := platform.Namespaces.Mode == placementshared.ID
	var watched []string
	if shared {
		watched = []string{wsNamespace}
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), manager.Options(scheme, watched, o.metricsAddr, o.probeAddr))
	if err != nil {
		return err
	}
	probeNS, extra := netprobeNamespace(shared, wsNamespace, releaseNS)
	if err := (&namespace.Reconciler{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Store: db, Placement: wiring.Placement(platform),
		Gateway:       namespace.GatewayPods{Namespace: o.gatewayNS, Labels: o.gatewayPods},
		NetworkPolicy: o.networkPol, Egress: egress, Bindings: bindings, Extra: extra,
		PullSecrets: namespace.PullSecrets{Namespace: releaseNS, Names: o.pullSecrets},
	}).SetupWithManager(mgr, feed); err != nil {
		return err
	}
	// The probe and its result live outside the namespaces the manager's
	// cache may cover, so they use a client without a cache.
	direct, err := client.New(mgr.GetConfig(), client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	if o.backupClaim.name != "" {
		if err := mgr.Add(backupclaim.Claim{
			Client: direct, Namespace: releaseNS, Name: o.backupClaim.name,
			Size: backupClaimSize, StorageClass: o.backupClaim.storageClass,
		}); err != nil {
			return err
		}
	}
	policy := &netprobe.Store{Facts: db.Facts()}
	if err := mgr.Add(&netprobe.Loop{
		Prober: &netprobe.Prober{
			Client: direct, AppsNamespace: probeNS,
			Image: o.platformImage(), ControlURL: o.probe.controlURL, Timeout: o.probe.timeout,
		},
		Store: policy, Every: o.probe.every,
	}); err != nil {
		return err
	}
	if err := (&workspace.Reconciler{
		Client: mgr.GetClient(), Store: db, Storage: wiring.StorageResolver(db, platform),
		ReadyTimeout: o.readyTimeout, PublicHost: common.publicHost, Sizes: appSizes,
		Machines: capacity.Nodes{Reader: mgr.GetClient()},
		UIDMode:  platform.Identity.UIDMode, Exposure: wiring.Exposure(platform),
		ManagedNamespacesOnly: !shared, NetworkPolicy: policy,
		Tokens: &token.DBStore{Tokens: db.Tokens()}, Secrets: mgr.GetAPIReader(),
		APIURL:     publicURL(platform.URLs, common.publicHost),
		CloneImage: platform.Source.CloneImage, CloneGitConfig: platform.Source.GitConfig, RunnerImage: o.platformImage(),
		PixiImage:  platform.Environments.PixiImage,
		NebiURL:    wiring.NebiURL(platform),
		SecretKeys: keys,
		Access:     wiring.JobAccess(db.People(store.IDRange{}), platform.Roles),
	}).SetupWithManager(mgr, feed); err != nil {
		return err
	}
	// Always run: with the platform switch off it removes the previews that
	// already exist instead of leaving untrusted PR code running.
	pv := platform.Source.Previews
	if err := (&preview.Reconciler{
		Store: db, Hosts: wiring.CodeHosts(platform).All(), Secrets: mgr.GetAPIReader(),
		PollInterval: pv.PollInterval.Duration, MaxPerApp: pv.MaxPerApp, Now: time.Now, Enabled: pv.Enabled,
	}).SetupWithManager(mgr, feed); err != nil {
		return err
	}
	cronJobEvents, err := manager.CronJobEvents(mgr.GetConfig(), scheme, watched)
	if err != nil {
		return err
	}
	if err := mgr.Add(cronJobEvents); err != nil {
		return err
	}
	if err := (&jobs.Reconciler{
		Client: mgr.GetClient(), Store: db, Reader: mgr.GetAPIReader(), RunnerImage: o.platformImage(), PixiImage: platform.Environments.PixiImage,
		Access: wiring.JobAccess(db.People(store.IDRange{}), platform.Roles), Now: time.Now,
		UIDMode: platform.Identity.UIDMode, ManagedNamespacesOnly: !shared,
		CronJobEvents: cronJobEvents, Storage: wiring.StorageResolver(db, platform),
		Secrets: direct, SecretKeys: keys, Sizes: appSizes,
		Tokens: &token.DBStore{Tokens: db.Tokens()}, APIURL: publicURL(platform.URLs, common.publicHost),
		APIInternalURL: gatewayBase(o.probe.controlURL),
	}).SetupWithManager(mgr, feed); err != nil {
		return err
	}
	if err := o.setupStorage(mgr, feed, db, platform, probeNS, watched); err != nil {
		return err
	}
	// Leader only (the manager runs runnables on the leader by default), so
	// replicas never stop the same app twice.
	if err := mgr.Add(&idle.Checker{Apps: db.Apps()}); err != nil {
		return err
	}
	if o.notif.enabled() {
		if err := addNotifications(mgr, db, o.notif); err != nil {
			return err
		}
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	// The feed has every subscriber now. It runs on the leader, like the
	// reconcilers it feeds.
	if err := mgr.Add(ctrlmanager.RunnableFunc(func(ctx context.Context) error { feed.Run(ctx); return nil })); err != nil {
		return err
	}
	return mgr.Start(ctx)
}

// setupStorage adds the storage reconcilers: the cluster's findings, the
// opt-in probe (its claim and Jobs live in probeNS, the namespace the
// NetworkPolicy probe uses: an apps namespace, never the release one), homes,
// shared folders, scheduled moves and, in per-team mode, team quotas. Helper
// Jobs run the controller's own image (`javo storage prepare|probe-step`).
func (o *controllerOptions) setupStorage(mgr ctrl.Manager, feed *dbsource.Feed, db *store.Store, platform config.Platform, probeNS string, watched []string) error {
	r := storage.Rows{Store: db, Config: platform.Storage.Spec()}
	resolver := wiring.StorageResolver(db, platform)
	uids := ids.People{People: db.People(store.IDRange{}), Mode: platform.Identity.UIDMode}
	gids := ids.AllocatedGroups{Groups: db.Groups(store.IDRange{First: platform.Identity.GIDRange.First, Last: platform.Identity.GIDRange.Last})}
	reader := mgr.GetAPIReader()
	if err := (&storage.ClusterReconciler{Client: mgr.GetClient(), Reader: reader, Rows: r}).SetupWithManager(mgr, feed); err != nil {
		return err
	}
	if err := (&storage.ProbeReconciler{Client: mgr.GetClient(), Reader: reader, Rows: r, Namespace: probeNS, Image: o.platformImage()}).SetupWithManager(mgr, feed); err != nil {
		return err
	}
	if err := (&storage.HomeReconciler{
		Client: mgr.GetClient(), Reader: reader, Rows: r, Backends: wiring.StorageBackendsByMode(), UIDs: uids, Image: o.platformImage(),
	}).SetupWithManager(mgr, feed); err != nil {
		return err
	}
	if err := (&storage.MigrationReconciler{
		Client: mgr.GetClient(), Reader: reader, Rows: r, Backends: wiring.StorageBackendsByMode(), UIDs: uids, Storage: resolver,
		Image: o.platformImage(),
	}).SetupWithManager(mgr, feed); err != nil {
		return err
	}
	if hb, err := wiring.HomeBackups(platform); err != nil {
		return err
	} else if hb != nil {
		br := storage.BackupReconciler{
			Client: mgr.GetClient(), Reader: reader, Rows: r, UIDs: uids, Storage: resolver, Image: o.platformImage(), Backups: hb,
			People: storage.PeopleLabels{People: db.People(store.IDRange{})},
		}
		// Every home volume is backed up, even one found without a row.
		if err := (storage.VolumeFinder{Reader: reader, Store: db, Namespaces: watched}).SetupWithManager(mgr, feed); err != nil {
			return err
		}
		if err := (&br).SetupWithManager(mgr, feed); err != nil {
			return err
		}
		if err := (&storage.FolderBackupReconciler{BackupReconciler: br}).SetupWithManager(mgr, feed); err != nil {
			return err
		}
	}
	if err := (&storage.SharedFolderReconciler{
		Client: mgr.GetClient(), Reader: reader, Rows: r, Backends: wiring.StorageBackendsByMode(), GIDs: gids, Image: o.platformImage(),
		PerUserNamespaces: platform.Namespaces.Mode == placementperuser.ID,
	}).SetupWithManager(mgr, feed); err != nil {
		return err
	}
	if teams := teamNamespaces(platform.Namespaces); len(teams) > 0 {
		return (&storage.QuotaReconciler{Client: mgr.GetClient(), Rows: r, Teams: teams}).SetupWithManager(mgr, feed)
	}
	return nil
}

// fingerprintEvery is how often the controller looks for secret values with
// no fingerprint yet; until one has its fingerprint the gateway sends no log
// line of its owner's apps to a model.
const fingerprintEvery = 15 * time.Second

// secretKeys loads the key-encryption key and makes the data keyset when the
// database has none. A key that does not open the stored keyset is logged
// and kept: apps that get secrets then fail as SecretsUnreadable, the others
// run, and the keyset is never replaced (that would lose every value).
func (o *controllerOptions) secretKeys(ctx context.Context, db *store.Store, releaseNS string) (*secrets.Keys, error) {
	kek, err := o.loadKEK(ctx, releaseNS)
	if err != nil || kek == nil {
		return nil, err
	}
	keys := &secrets.Keys{Store: db, KEK: kek}
	if err := keys.Ensure(ctx); err != nil {
		ctrl.Log.Error(err, "the secrets key does not open the stored secrets keyset; apps that get secrets will not start")
	}
	return keys, nil
}

// startClient reads straight from the API server, for the keys read once at
// start, before the manager's cache exists.
func startClient() (client.Client, error) {
	return client.New(ctrl.GetConfigOrDie(), client.Options{})
}

// loadKEK reads the key-encryption key from --secrets-key-file or the
// --secrets-key-secret Secret (made first with --secrets-key-create); nil
// when neither is set.
func (o *controllerOptions) loadKEK(ctx context.Context, releaseNS string) (secrets.KEK, error) {
	switch {
	case o.secretsKey != "" && o.secretsKeySecret != "":
		return nil, errors.New("--secrets-key-file and --secrets-key-secret are both set; use one")
	case o.secretsKey != "":
		return secrets.KEKFromFile(o.secretsKey)
	case o.secretsKeySecret != "":
		c, err := startClient()
		if err != nil {
			return nil, err
		}
		return secretskey.Secret{Client: c, Namespace: releaseNS, Name: o.secretsKeySecret, Create: o.secretsKeyCreate}.Load(ctx)
	}
	ctrl.Log.Info("no --secrets-key-file or --secrets-key-secret: apps that get secrets will not start")
	return nil, nil
}

// teamNamespaces lists each configured team's namespace with its storage limit.
// Only per-team mode has team namespaces; other modes list none, so no quota
// is made there even when teams are configured.
func teamNamespaces(cfg config.Namespaces) []storage.TeamNamespace {
	if cfg.Mode != placementperteam.ID {
		return nil
	}
	p := placementperteam.New(cfg)
	out := make([]storage.TeamNamespace, 0, len(cfg.Teams))
	for _, t := range cfg.Teams {
		out = append(out, storage.TeamNamespace{Namespace: p.TeamNamespace(t), Limit: t.StorageLimit})
	}
	return out
}

// sizesFileUsage documents --sizes-file, which the controller and the
// gateway share.
const sizesFileUsage = "YAML file with the app sizes users pick from ({default, sizes: [{name, displayName, cpu, memory}]}); empty offers none and keeps template resources"

// parseAppRoleBindings parses clusterrole=namespace/serviceaccount values.
func parseAppRoleBindings(in []string) ([]namespace.Binding, error) {
	out := make([]namespace.Binding, 0, len(in))
	for _, v := range in {
		role, sa, ok := strings.Cut(v, "=")
		ns, name, ok2 := strings.Cut(sa, "/")
		if !ok || !ok2 || role == "" || ns == "" || name == "" {
			return nil, fmt.Errorf("--app-role-binding: %q is not clusterrole=namespace/serviceaccount", v)
		}
		out = append(out, namespace.Binding{ClusterRole: role, ServiceAccount: types.NamespacedName{Namespace: ns, Name: name}})
	}
	return out, nil
}

// publicURL is the address people reach the UI and its API at: urls.uiHost,
// else --public-host, with urls.scheme; empty when neither is set.
func publicURL(urls config.URLs, publicHost string) string {
	host := urls.UIHost
	if host == "" {
		host = publicHost
	}
	if host == "" {
		return ""
	}
	return urls.URLScheme() + "://" + host
}

// gatewayBase is the gateway's address inside the cluster: the scheme and
// host of its health endpoint (--netprobe-control-url).
func gatewayBase(controlURL string) string {
	u, err := url.Parse(controlURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// netprobeNamespace is where the NetworkPolicy probe runs its pods: the
// shared namespace, or in the other modes a namespace of its own, kept by the
// namespace reconciler like an app namespace (label, policies, bindings) and
// returned in extra for it.
func netprobeNamespace(shared bool, wsNamespace, releaseNS string) (probeNS string, extra []string) {
	if shared {
		return wsNamespace, nil
	}
	probeNS = naming.NamespaceName(releaseNS, "netprobe")
	return probeNS, []string{probeNS}
}

// platformImage is the runner image and the Secrets it is pulled with.
func (o *controllerOptions) platformImage() podspec.Image {
	return podspec.Image{Ref: o.runnerImage, PullSecrets: o.pullSecrets}
}

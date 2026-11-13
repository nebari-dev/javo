//go:build !local

package servercmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlmanager "sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/nebari-dev/javo/internal/capacity/nodes"
	"github.com/nebari-dev/javo/internal/cli/wiring"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/backupclaim"
	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/idle"
	"github.com/nebari-dev/javo/internal/controller/loop/managed"
	"github.com/nebari-dev/javo/internal/controller/manager"
	"github.com/nebari-dev/javo/internal/controller/namespace"
	"github.com/nebari-dev/javo/internal/controller/preview"
	"github.com/nebari-dev/javo/internal/controller/secretskey"
	"github.com/nebari-dev/javo/internal/controller/storage"
	"github.com/nebari-dev/javo/internal/executor/kube"
	"github.com/nebari-dev/javo/internal/features/placementperteam"
	"github.com/nebari-dev/javo/internal/features/placementperuser"
	"github.com/nebari-dev/javo/internal/features/placementshared"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/netprobe/kubeprobe"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/store"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// runOnCluster runs the controller on the cluster the kubeconfig names:
// a controller-runtime manager with its cache, leader election, metrics
// and health listeners, and every reconciler a cluster has.
func (o *controllerOptions) runOnCluster(ctx context.Context, common *commonFlags, platform config.Platform, db *store.Store) error {
	wsNamespace, releaseNS := common.workspaceNamespace, common.namespace
	if o.runnerImage == "" || o.probe.controlURL == "" {
		return errors.New("the controller needs --runner-image and --netprobe-control-url")
	}
	if o.networkPol && (o.gatewayNS == "" || len(o.gatewayPods) == 0 || o.gatewayPort <= 0) {
		return errors.New("--network-policy needs --gateway-namespace (or POD_NAMESPACE), --gateway-pod-labels and --gateway-port")
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
		Gateway:       namespace.GatewayPods{Namespace: o.gatewayNS, Labels: o.gatewayPods, Port: o.gatewayPort},
		NetworkPolicy: o.networkPol, Egress: egress, Bindings: bindings, Extra: extra,
		PullSecrets: namespace.PullSecrets{Namespace: releaseNS, Names: o.pullSecrets},
		// Config validation allows these only in per-user and per-team mode.
		Limits: namespace.Limits{Quota: platform.Limits.Namespace.Quota, ContainerDefaults: platform.Limits.Namespace.ContainerDefaults,
			ContainerDefaultRequests: platform.Limits.Namespace.ContainerDefaultRequests},
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
		Prober: &kubeprobe.Prober{
			Client: direct, AppsNamespace: probeNS,
			Image: o.platformImage(), ControlURL: o.probe.controlURL, Timeout: o.probe.timeout,
		},
		Store: policy, Every: o.probe.every,
	}); err != nil {
		return err
	}
	appStorage := wiring.StorageResolver(db, platform)
	// Apps run as Pods: the executor is the only part of the app controller
	// that talks to the cluster.
	apps := &kube.Executor{
		Sizes: appSizes, Machines: nodes.Source{Reader: mgr.GetClient()}, HomeClaim: appStorage.HomeClaim,
		ManagedOnly: !shared, Isolation: platform.Workloads.Isolation(),
	}
	if err := apps.SetupWithManager(mgr); err != nil {
		return err
	}
	appRec := o.appReconciler(common, platform, db, appSizes)
	appRec.Exec, appRec.Place, appRec.Storage, appRec.NetworkPolicy = apps, apps, appStorage, policy
	appRec.Secrets = kube.Secrets{Reader: mgr.GetAPIReader(), Client: mgr.GetClient()}
	appRec.APIInternalURL, appRec.SecretKeys = gatewayBase(o.probe.controlURL), keys
	if err := appRec.Setup(managed.Driver(mgr), feed); err != nil {
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
	// Runs are Jobs of the same cluster the apps run on, with the same
	// isolation. The controller fires the schedules itself.
	runs := apps.Runs()
	jobRec := o.jobReconciler(common, platform, db, appSizes)
	jobRec.Exec, jobRec.Legacy = runs, runs
	jobRec.Secrets = kube.Secrets{Reader: mgr.GetAPIReader(), Client: mgr.GetClient(), Runs: true}
	jobRec.APIInternalURL, jobRec.SecretKeys = gatewayBase(o.probe.controlURL), keys
	if err := jobRec.Setup(managed.Driver(mgr), feed); err != nil {
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

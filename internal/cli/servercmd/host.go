package servercmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/spf13/pflag"
	"github.com/tink-crypto/tink-go/v2/tink"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	ctrlmanager "sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/cli/wiring"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/idle"
	"github.com/nebari-dev/javo/internal/controller/loop"
	"github.com/nebari-dev/javo/internal/controller/namespace"
	"github.com/nebari-dev/javo/internal/egressproxy"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/kube"
	"github.com/nebari-dev/javo/internal/ext/placement"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/namespaces"
	"github.com/nebari-dev/javo/internal/gateway/podlogs"
	gwstorage "github.com/nebari-dev/javo/internal/gateway/storage"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
)

// clusterFlags are the flags of `javo server` that only mean something on
// Kubernetes: its namespaces, policies, Secrets and the manager's own
// listeners. Given with another executor they are refused, not ignored.
var clusterFlags = []string{
	"namespace", "gateway-namespace", "gateway-pod-labels", "gateway-port", "network-policy",
	"egress-allow-namespaces",
	"app-role-binding", "image-pull-secret",
	"notifications-namespace", "notifications-config-map", "notifications-secret",
	"netprobe-control-url",
	"secrets-key-file", "secrets-key-secret", "secrets-key-create",
	"sessions-key-secret", "sessions-key-create",
	"secret-fingerprint-key-secret", "secret-fingerprint-key-create",
	"backup-password-secret", "backup-password-create",
	"backup-claim", "backup-claim-size", "backup-claim-storage-class",
}

// containerFlags are the flags a Docker host reads as a cluster does:
// what apps may reach (through the forward proxy there), the isolation
// probe's pace and the file helpers. Where apps are processes there is
// none of the three, and they are refused.
var containerFlags = []string{
	"egress-deny-cidrs", "egress-allow-cidrs", "egress-allow-cluster-cidrs",
	"netprobe-interval", "netprobe-timeout",
	"files-image", "files-idle-after",
}

// listenerFlags are the listeners the controller-runtime manager serves
// besides the gateway's port. Without a cluster there is no manager and
// nothing but the gateway's port is listened on, and on Docker the port
// of the apps' forward proxy (docker.proxyPort).
var listenerFlags = []string{"metrics-bind-address", "health-probe-bind-address"}

// refuseClusterFlags is an error for each flag given that the executor
// (one that is not Kubernetes) has no use for. A second listener is
// refused whatever its address: on Docker, apps reach every port of the
// container Javo runs in.
func refuseClusterFlags(f *pflag.FlagSet, executor string) error {
	var errs []error
	for _, name := range clusterFlags {
		if fl := f.Lookup(name); fl != nil && fl.Changed {
			errs = append(errs, fmt.Errorf("--%s is for executor kubernetes and means nothing with executor %s", name, executor))
		}
	}
	for _, name := range containerFlags {
		if fl := f.Lookup(name); fl != nil && fl.Changed && executor != config.ExecutorDocker {
			errs = append(errs, fmt.Errorf("--%s is for executor kubernetes or docker and means nothing with executor %s", name, executor))
		}
	}
	for _, name := range listenerFlags {
		if fl := f.Lookup(name); fl != nil && fl.Changed && fl.Value.String() != "0" {
			errs = append(errs, fmt.Errorf("--%s %s: with executor %s Javo listens on --listen only, which answers /healthz and /readyz; another listener is one more port its apps could reach", name, fl.Value, executor))
		}
	}
	return errors.Join(errs...)
}

// needsCluster refuses a command that runs one component alone when apps
// do not run on Kubernetes: the gateway and the controller then share one
// executor, in one process.
func needsCluster(platform config.Platform, what string) error {
	if platform.OnKubernetes() {
		return nil
	}
	return fmt.Errorf("%s needs executor kubernetes: with executor %s the gateway and the controller run in one process, as javo server", what, platform.Executor)
}

// errNoCluster is what the server, the controller and the gateway say
// when they are asked to run on a cluster in a binary built with the tag
// local. Each asks before the first thing only a cluster needs, so that
// binary links no client of one.
var errNoCluster = wiring.LeftOut("executor kubernetes")

// hostOf builds where apps run for a config whose executor is not
// Kubernetes, or nil when it is. flags are the command's, for the ones
// only a cluster gives meaning to.
func hostOf(ctx context.Context, flags *pflag.FlagSet, common *commonFlags, gw *gatewayOptions, ctl *controllerOptions, platform config.Platform, db *store.Store) (*wiring.Host, error) {
	if platform.OnKubernetes() {
		if wiring.LocalBuild {
			return nil, errNoCluster
		}
		return nil, nil
	}
	if err := refuseClusterFlags(flags, platform.Executor); err != nil {
		return nil, err
	}
	egress, err := namespace.ParseEgress(ctl.egressDeny, ctl.egressAllow, ctl.egressAllowC)
	if err != nil {
		return nil, err
	}
	return wiring.NewHost(ctx, platform, wiring.HostInputs{
		DB: db, Namespace: common.workspaceNamespace, Listen: gw.listen, RunnerImage: ctl.runnerImage,
		FilesImage: gw.filesImage, FilesIdleAfter: gw.filesIdleAfter,
		Egress:     egressproxy.Rules{Deny: egress.Deny, Allow: egress.Allow, AllowCluster: egress.AllowCluster},
		ProbeEvery: ctl.probe.every, ProbeTimeout: ctl.probe.timeout,
	})
}

// gatewayParts is what the gateway takes from where apps run.
type gatewayParts struct {
	// client reads the cluster; nil where there is none.
	client   client.Client
	machines capacity.Source
	// apps and runs say where a workload listens and what it printed.
	apps, runs executor.Executor
	policy     netprobe.Reader
	// homes and usage read people's files.
	homes gwstorage.Homes
	usage gwstorage.HomeUsages
	// sessionsKey and fingerprinter are Javo's own keys.
	sessionsKey   tink.AEAD
	fingerprinter *secrets.Fingerprinter
	people        authn.People
	// internalHost is the host apps call the API at.
	internalHost string
}

// parts is what the gateway takes from host, or from the cluster when
// host is nil.
func (o *gatewayOptions) parts(ctx context.Context, common *commonFlags, platform config.Platform, db *store.Store, pl placement.Placement, host *wiring.Host) (gatewayParts, error) {
	people := db.People(uidRange(platform.Identity))
	if host == nil {
		if wiring.LocalBuild {
			return gatewayParts{}, errNoCluster
		}
		on, err := o.clusterParts(ctx, common, platform, db, pl)
		on.people = people
		return on, err
	}
	// What the host serves beside the gateway ends with it.
	host.Serve(ctx)
	on := gatewayParts{
		machines: host.Machines(), apps: host.Apps, runs: host.Runs, policy: host.NetworkPolicy(),
		sessionsKey: host.SessionsKey, fingerprinter: host.Fingerprinter, people: host.People(people),
		internalHost: o.internalHost,
	}
	on.homes, on.usage = host.Files, host.Files
	if u, err := url.Parse(host.APIURL); err == nil && on.internalHost == "" {
		on.internalHost = u.Host
	}
	return on, nil
}

// clusterParts is what the gateway takes from the cluster the kubeconfig
// names: a client, its nodes, the file picker pods and Javo's keys, which
// are Secrets of the release namespace.
func (o *gatewayOptions) clusterParts(ctx context.Context, common *commonFlags, platform config.Platform, db *store.Store, pl placement.Placement) (gatewayParts, error) {
	var none gatewayParts
	wsNamespace, namespace := common.workspaceNamespace, common.namespace
	if namespace == "" {
		return none, errors.New("--namespace is required (or set POD_NAMESPACE)")
	}
	restCfg, err := ctrlconfig.GetConfig()
	if err != nil {
		return none, fmt.Errorf("load kubeconfig: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return none, err
	}
	scheme, err := gatewayScheme()
	if err != nil {
		return none, err
	}
	c, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return none, err
	}
	machines, err := capacity.Watch(ctx, restCfg)
	if err != nil {
		return none, fmt.Errorf("watch nodes: %w", err)
	}
	sessionsKey, err := o.sessionsKeyOf(ctx, c, namespace)
	if err != nil {
		return none, err
	}
	// c reads straight from the API server (no cache), as files.Pods needs.
	pods, err := files.NewPods(files.PodsConfig{
		Client: c, Namespace: wsNamespace, Image: o.filesImage, IdleAfter: o.filesIdleAfter,
		// The sessions key is the one key every replica shares; a sealed
		// login is bound to its pod and Secret, so it opens nothing else.
		Seal:    sessionsKey,
		Storage: podstorage.Provisioner{Resolver: wiring.StorageResolver(db, platform), Maker: db.Homes()},
		UIDs:    ids.People{People: db.People(store.IDRange{}), Mode: platform.Identity.UIDMode},
		ReapIn:  namespaces.New(pl, c).All,
		// One namespace per person: sweep with one cluster-wide call.
		ReapAcrossNamespaces: len(pl.Desired()) == 0,
	})
	if err != nil {
		return none, err
	}
	go pods.Run(ctx)
	fp, err := common.fingerprinter(ctx, c)
	if err != nil {
		return none, err
	}
	// What runs the apps and the runs: the gateway asks it where an app
	// listens and for logs, and nothing else. It finds a run's pods
	// through c.
	workloads := &kube.Executor{Client: c, PodLogs: podlogs.Clientset{Clientset: clientset}}
	lister := files.NewLister(pods, files.Rclone{})
	return gatewayParts{
		client: c, machines: machines, apps: workloads, runs: workloads.Runs(),
		policy: &netprobe.Store{Facts: db.Facts()}, homes: lister, usage: lister,
		sessionsKey: sessionsKey, fingerprinter: fp, internalHost: o.internalHost,
	}, nil
}

// runOnHost runs the controller where there is no cluster: the app and
// job reconcilers, what the host sets up beside them (homes, the answer
// to group folders, on Docker the isolation probe) and the idle check,
// driven by the database's change feed and the executors' Watch on a
// loop.Loop. There is no manager, so no leader election (this process is
// the only controller), no metrics or health listener, and none of what
// only a cluster has: namespaces, network policy, storage classes and
// their probe, home moves and backups, the backup claim, previews and
// failure notices.
func (o *controllerOptions) runOnHost(ctx context.Context, common *commonFlags, platform config.Platform, db *store.Store, host *wiring.Host) error {
	if o.runnerImage == "" {
		return errors.New("the controller needs --runner-image")
	}
	appSizes, err := sizes.Load(common.sizesFile)
	if err != nil {
		return err
	}
	ctrl.SetLogger(zap.New())
	go host.Keys.RunFingerprints(ctx, host.Fingerprinter, fingerprintEvery)
	if err := db.StartFeed(ctx); err != nil {
		return fmt.Errorf("listen for database changes: %w", err)
	}
	feed := dbsource.NewFeed(db.Changes())
	var l loop.Loop
	appRec := o.appReconciler(common, platform, db, appSizes)
	appRec.Exec, appRec.Place, appRec.Secrets, appRec.Host = host.Apps, host.Place, host.Secrets, host.PlanFor
	appRec.Storage, appRec.NetworkPolicy = wiring.StorageResolver(db, platform), host.NetworkPolicy()
	appRec.APIInternalURL, appRec.SecretKeys = host.APIURL, host.Keys
	if err := appRec.Setup(&l, feed); err != nil {
		return err
	}
	jobRec := o.jobReconciler(common, platform, db, appSizes)
	jobRec.Exec, jobRec.Secrets, jobRec.Host = host.Runs, host.Secrets, host.PlanFor
	jobRec.APIInternalURL, jobRec.SecretKeys = host.APIURL, host.Keys
	if err := jobRec.Setup(&l, feed); err != nil {
		return err
	}
	if err := host.Setup(&l, feed); err != nil {
		return err
	}
	if err := l.Add(&idle.Checker{Apps: db.Apps()}); err != nil {
		return err
	}
	// The feed has every subscriber now.
	if err := l.Add(ctrlmanager.RunnableFunc(func(ctx context.Context) error { feed.Run(ctx); return nil })); err != nil {
		return err
	}
	return l.Start(ctx)
}

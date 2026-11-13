package servercmd

import (
	"context"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"github.com/nebari-dev/javo/internal/ai/aisettings"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/cli/wiring"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/jobs"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/sizes"
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
	gatewayPort  int32
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
	f.Int32Var(&o.gatewayPort, "gateway-port", 0, "the gateway pods' HTTP port, the only one app and job pods may connect to")
	f.BoolVar(&o.networkPol, "network-policy", true, "keep the default-deny NetworkPolicies in every workspace namespace")
	f.StringSliceVar(&o.egressDeny, "egress-deny-cidrs", []string{"169.254.169.254/32", "fd00:ec2::254/128", "fd20:ce::254/128", "100.100.100.200/32", "168.63.129.16/32"},
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
			ctx := signals.SetupSignalHandler()
			platform, err := common.load(ctx)
			if err != nil {
				return err
			}
			db, err := common.openDB(ctx, platform)
			if err != nil {
				return err
			}
			defer db.Close()
			if err := needsCluster(platform, "javo controller"); err != nil {
				return err
			}
			return opts.run(ctx, &common, platform, db, nil)
		},
	}
	common.register(cmd.Flags())
	opts.register(cmd.Flags())
	return cmd
}

// run runs the controller on db until ctx ends: on host when apps run
// there, else on the cluster.
func (o *controllerOptions) run(ctx context.Context, common *commonFlags, platform config.Platform, db *store.Store, host *wiring.Host) error {
	if host != nil {
		return o.runOnHost(ctx, common, platform, db, host)
	}
	return o.runOnCluster(ctx, common, platform, db)
}

// appReconciler is the app reconciler but for where apps run, which its
// caller fills in: Exec, Place, Secrets, Storage, NetworkPolicy,
// APIInternalURL and SecretKeys (and Host, on a machine).
func (o *controllerOptions) appReconciler(common *commonFlags, platform config.Platform, db *store.Store, appSizes sizes.Catalog) *workspace.Reconciler {
	return &workspace.Reconciler{
		Store:        db,
		ReadyTimeout: o.readyTimeout, PublicHost: common.publicHost, Sizes: appSizes,
		UIDMode: platform.Identity.UIDMode, Exposure: wiring.Exposure(platform),
		Tokens:     &token.DBStore{Tokens: db.Tokens()},
		APIURL:     publicURL(platform.URLs, common.publicHost),
		CloneImage: platform.Source.CloneImage, CloneGitConfig: platform.Source.GitConfig, RunnerImage: o.platformImage(),
		PixiImage:  platform.Environments.PixiImage,
		NebiURL:    wiring.NebiURL(platform),
		AgentModel: aisettings.AgentModelOf(db, platform.AI),
		Access:     wiring.JobAccess(db.People(store.IDRange{}), platform.Roles),
		// Checked again at each pod build, so a tightened list stops
		// apps saved before.
		AllowedRegistries: platform.ContainerImages.AllowedRegistries,
		LockAgents:        wiring.LockAgents(platform),
	}
}

// jobReconciler is the jobs reconciler but for where runs run, which its
// caller fills in: Exec, Secrets, APIInternalURL and SecretKeys.
func (o *controllerOptions) jobReconciler(common *commonFlags, platform config.Platform, db *store.Store, appSizes sizes.Catalog) *jobs.Reconciler {
	return &jobs.Reconciler{
		Store: db, RunnerImage: o.platformImage(), PixiImage: platform.Environments.PixiImage,
		Access: wiring.JobAccess(db.People(store.IDRange{}), platform.Roles), Now: time.Now,
		UIDMode: platform.Identity.UIDMode, Storage: wiring.StorageResolver(db, platform),
		Sizes:  appSizes,
		Tokens: &token.DBStore{Tokens: db.Tokens()}, APIURL: publicURL(platform.URLs, common.publicHost),
		MaxTimeout: platform.Limits.JobMaxTimeout.Duration,
	}
}

// fingerprintEvery is how often the controller looks for secret values with
// no fingerprint yet; until one has its fingerprint the gateway sends no log
// line of its owner's apps to a model.
const fingerprintEvery = 15 * time.Second

// sizesFileUsage documents --sizes-file, which the controller and the
// gateway share.
const sizesFileUsage = "YAML file with the app sizes users pick from ({default, sizes: [{name, displayName, cpu, memory}]}); empty offers none and keeps template resources"

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

// platformImage is the runner image and the Secrets it is pulled with.
func (o *controllerOptions) platformImage() podspec.Image {
	return podspec.Image{Ref: o.runnerImage, PullSecrets: o.pullSecrets}
}

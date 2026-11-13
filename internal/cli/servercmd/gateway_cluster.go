//go:build !local

package servercmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/nebari-dev/javo/internal/capacity/nodes"
	"github.com/nebari-dev/javo/internal/cli/wiring"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/secretskey"
	"github.com/nebari-dev/javo/internal/executor/kube"
	"github.com/nebari-dev/javo/internal/ext/placement"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/clusterview/kubeview"
	"github.com/nebari-dev/javo/internal/gateway/namespaces"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/tink-crypto/tink-go/v2/tink"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func gatewayScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	return scheme, corev1.AddToScheme(scheme)
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
	machines, err := nodes.Watch(ctx, restCfg)
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
		ReapIn:  namespaces.New(pl, kubeview.Of(c)).All,
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
	workloads := &kube.Executor{Client: c, PodLogs: kube.ClientsetLogs{Clientset: clientset}}
	lister := files.NewLister(pods, files.Rclone{})
	return gatewayParts{
		cluster: kubeview.Of(c), machines: machines, apps: workloads, runs: workloads.Runs(),
		policy: &netprobe.Store{Facts: db.Facts()}, homes: lister, usage: lister,
		sessionsKey: sessionsKey, fingerprinter: fp, internalHost: o.internalHost,
	}, nil
}

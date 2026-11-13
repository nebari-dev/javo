//go:build !local

package servercmd

import (
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/nebari-dev/javo/internal/jobs/notify"
	"github.com/nebari-dev/javo/internal/store"
)

// addNotifications registers the failure-notice runnable with mgr. Owners are
// not mailed yet, since no verified email address is known for them; the
// ConfigMap's own subscriptions still send.
func addNotifications(mgr ctrl.Manager, db *store.Store, f notificationFlags) error {
	kube, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		return err
	}
	n, err := notify.New(notify.Config{
		Store: db, Kube: kube, Namespace: f.namespace,
		ConfigMapName: f.configMap, SecretName: f.secret, Recipients: notify.NoRecipients{},
	})
	if err != nil {
		return err
	}
	return mgr.Add(n)
}

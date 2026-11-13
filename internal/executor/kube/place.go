package kube

import (
	"context"
	"errors"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/workload"
)

// Hold makes sure the app has its Service, named as its Pod, sending to
// port. The Service lasts as long as the app, not as its Pods: the gateway
// reaches the app by it (Addr), and it owns the app's Secrets that can be
// made again (Secrets.Put).
func (e *Executor) Hold(ctx context.Context, ref workload.Ref, port int32) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: ref.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, e.Client, svc, func() error {
		svc.Labels = map[string]string{model.LabelApp: ref.ID, model.LabelOwner: ref.Owner}
		svc.Spec.Selector = map[string]string{model.LabelApp: ref.ID}
		svc.Spec.Ports = []corev1.ServicePort{{Port: port, TargetPort: intstr.FromInt32(port), Protocol: corev1.ProtocolTCP}}
		return nil
	})
	return err
}

// Release deletes the app's Service.
func (e *Executor) Release(ctx context.Context, ref workload.Ref) error {
	return e.drop(ctx, ref, &corev1.Service{})
}

// legacyUses are the uses an app's Secrets had names for before bundle
// names ("<name>.app-<use>"): "<name>-javo-<use>". A job of the same owner
// could produce such a name, so they are only cleaned up, and only when
// labelled as the app's.
var legacyUses = []string{"api", "secrets", "git", "nebi"}

func legacySecretName(name, use string) string { return name + "-" + model.ProductName + "-" + use }

// Tidy deletes the app's Secrets under their names before bundle names;
// the caller makes sure no Pod of the app reads them any more. The id of
// the API token the old API Secret names goes to revoke first.
func (e *Executor) Tidy(ctx context.Context, ref workload.Ref, revoke func(ctx context.Context, tokenID string) error) error {
	for _, use := range legacyUses {
		sec, err := OwnedSecret(ctx, e.Reader, ref, legacySecretName(ref.Name, use))
		if errors.Is(err, executor.ErrForeign) || (err == nil && sec == nil) {
			continue
		}
		if err != nil {
			return err
		}
		if id := sec.Annotations[model.AnnotationTokenID]; use == "api" && id != "" {
			if err := revoke(ctx, id); err != nil {
				return err
			}
		}
		if err := e.Client.Delete(ctx, sec, client.Preconditions{UID: &sec.UID}); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

// readsLegacySecrets is true when pod reads a Secret under a name from
// before bundle names (its own, or its parent's repository token): it was
// built before them and must be built again.
func readsLegacySecrets(pod *corev1.Pod) bool {
	legacy := func(name string) bool {
		return slices.ContainsFunc(legacyUses, func(use string) bool { return strings.HasSuffix(name, legacySecretName("", use)) })
	}
	for _, v := range pod.Spec.Volumes {
		if v.Secret != nil && legacy(v.Secret.SecretName) {
			return true
		}
	}
	for _, cs := range [][]corev1.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for _, c := range cs {
			for _, e := range c.Env {
				if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && legacy(e.ValueFrom.SecretKeyRef.Name) {
					return true
				}
			}
			for _, f := range c.EnvFrom {
				if f.SecretRef != nil && legacy(f.SecretRef.Name) {
					return true
				}
			}
		}
	}
	return false
}

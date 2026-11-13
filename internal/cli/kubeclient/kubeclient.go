// Package kubeclient is what the admin commands do on a cluster, with the
// Kubernetes client it builds: the claims of people's homes
// (homeclaims.Claims) and the key of people's secrets kept in a Secret.
// Only a javo that runs on Kubernetes carries it.
package kubeclient

import (
	"context"
	"fmt"

	"github.com/nebari-dev/javo/internal/cli/homeclaims"
	"github.com/nebari-dev/javo/internal/controller/secretskey"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

// Default connects using in-cluster config or the local kubeconfig.
func Default() (client.Client, error) {
	cfg, err := config.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	scheme := runtime.NewScheme()
	if err := addToScheme(scheme); err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{Scheme: scheme})
}

// addToScheme registers what the commands read: core and batch objects.
func addToScheme(s *runtime.Scheme) error {
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, batchv1.AddToScheme} {
		if err := add(s); err != nil {
			return err
		}
	}
	return nil
}

// DefaultClaims is the homes' claims of the cluster Default connects to.
func DefaultClaims() (homeclaims.Claims, error) {
	c, err := Default()
	if err != nil {
		return nil, err
	}
	return Claims(c), nil
}

// Claims is the homes' claims read and changed through c.
func Claims(c client.Client) homeclaims.Claims { return claims{c} }

type claims struct{ c client.Client }

// of lists the claims labelled with owner in namespace.
func (k claims) of(ctx context.Context, namespace, owner string) ([]corev1.PersistentVolumeClaim, error) {
	var l corev1.PersistentVolumeClaimList
	err := k.c.List(ctx, &l, client.InNamespace(namespace), client.MatchingLabels{model.LabelOwner: owner})
	return l.Items, err
}

func (k claims) Any(ctx context.Context, namespace, owner string) (bool, error) {
	found, err := k.of(ctx, namespace, owner)
	return len(found) > 0, err
}

func (k claims) Forget(ctx context.Context, homes []model.Home) error {
	for _, h := range homes {
		found, err := k.of(ctx, h.Namespace, h.Spec.Owner)
		if err != nil {
			return err
		}
		for i := range found {
			claim := &found[i]
			if claim.Labels[model.LabelForgotten] == "true" {
				continue
			}
			base := claim.DeepCopy()
			claim.Labels[model.LabelForgotten] = "true"
			if err := k.c.Patch(ctx, claim, client.MergeFrom(base)); client.IgnoreNotFound(err) != nil {
				return err
			}
		}
	}
	return nil
}

func (k claims) Delete(ctx context.Context, homes []model.Home) error {
	for _, h := range homes {
		// The home's finished storage Jobs (backups, restores) keep their
		// pods for their logs; a pod that names the claim keeps it from
		// going, so they go first.
		var jobs batchv1.JobList
		if err := k.c.List(ctx, &jobs, client.InNamespace(h.Namespace), client.MatchingLabels{model.LabelOwner: h.Spec.Owner}); err != nil {
			return err
		}
		for i := range jobs.Items {
			if err := k.c.Delete(ctx, &jobs.Items[i], client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
				return err
			}
		}
		// List and delete each: the controller's account may delete claims,
		// not delete a collection of them.
		found, err := k.of(ctx, h.Namespace, h.Spec.Owner)
		if err != nil {
			return err
		}
		for i := range found {
			if err := k.c.Delete(ctx, &found[i]); client.IgnoreNotFound(err) != nil {
				return err
			}
		}
	}
	return nil
}

// KEKFromSecret reads the key-encryption key of people's secrets from the
// Secret name in namespace (never made here: rotating needs the key that
// wrapped the stored keyset).
func KEKFromSecret(ctx context.Context, namespace, name string) (secrets.KEK, error) {
	cfg, err := config.GetConfig()
	if err != nil {
		return nil, err
	}
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		return nil, err
	}
	return secretskey.Secret{Client: c, Namespace: namespace, Name: name}.Load(ctx)
}

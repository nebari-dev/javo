// Package backupclaim makes the claim the Postgres backup CronJob writes its
// dumps to, when no backup bucket is configured.
//
// The chart cannot render that claim itself: its only consumer is the
// nightly CronJob, so on WaitForFirstConsumer storage (k3s local-path, kind)
// it stays Pending until the first backup, and `helm install --wait` would
// wait for it to bind until it times out. The controller makes it instead,
// outside the release, and never changes or deletes it afterwards.
package backupclaim

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The controller's Role in the release namespace grants these.
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=create

// Claim is the backup claim to make. It runs once as a manager Runnable.
type Claim struct {
	Client    client.Client
	Namespace string
	Name      string
	Size      resource.Quantity
	// StorageClass empty uses the cluster's default class.
	StorageClass string
}

// Ensure creates the claim when it does not exist. An existing claim (made by
// an earlier chart, or by an admin) holds backups and is left as it is. It has
// no owner, so removing the controller or the release never deletes backups.
func (c Claim) Ensure(ctx context.Context) error {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: c.Namespace, Name: c.Name},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: c.Size}},
		},
	}
	if c.StorageClass != "" {
		pvc.Spec.StorageClassName = &c.StorageClass
	}
	if err := c.Client.Create(ctx, pvc); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create backup claim %s/%s: %w", c.Namespace, c.Name, err)
	}
	return nil
}

// Start makes the claim once; the manager runs it on the leader.
func (c Claim) Start(ctx context.Context) error { return c.Ensure(ctx) }

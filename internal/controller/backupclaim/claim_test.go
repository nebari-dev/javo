package backupclaim

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEnsureCreatesTheClaimWhenAbsent(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	claim := Claim{Client: c, Namespace: "javo-system", Name: "javo-backups", Size: resource.MustParse("5Gi"), StorageClass: "fast"}
	if err := claim.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "javo-system", Name: "javo-backups"}, &pvc); err != nil {
		t.Fatal(err)
	}
	if got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(resource.MustParse("5Gi")) != 0 {
		t.Errorf("size %s, want 5Gi", got.String())
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "fast" {
		t.Errorf("storage class %v, want fast", pvc.Spec.StorageClassName)
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("access modes %v", pvc.Spec.AccessModes)
	}
	// No owner: deleting the controller or the release never deletes backups.
	if len(pvc.OwnerReferences) != 0 {
		t.Errorf("owner references %v", pvc.OwnerReferences)
	}
}

func TestEnsureWithoutStorageClassUsesTheClusterDefault(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	if err := (Claim{Client: c, Namespace: "ns", Name: "javo-backups", Size: resource.MustParse("1Gi")}).Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "javo-backups"}, &pvc); err != nil {
		t.Fatal(err)
	}
	if pvc.Spec.StorageClassName != nil {
		t.Errorf("storage class %q, want unset", *pvc.Spec.StorageClassName)
	}
}

// A claim that exists (made by an earlier release of the chart, or by an
// admin) holds backups: it is left exactly as it is.
func TestEnsureLeavesAnExistingClaimAlone(t *testing.T) {
	existing := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "javo-backups", Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm"}},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")}},
		},
	}
	c := fake.NewClientBuilder().WithObjects(existing).Build()
	if err := (Claim{Client: c, Namespace: "ns", Name: "javo-backups", Size: resource.MustParse("5Gi")}).Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "javo-backups"}, &pvc); err != nil {
		t.Fatal(err)
	}
	if got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(resource.MustParse("20Gi")) != 0 {
		t.Errorf("size changed to %s", got.String())
	}
	if pvc.Labels["app.kubernetes.io/managed-by"] != "Helm" || len(pvc.Labels) != 1 {
		t.Errorf("labels changed: %v", pvc.Labels)
	}
}

// Package storage is the extension point for how a person's home and a
// group's shared folder are stored: one disk per user, or one shared drive.
// Implementations live in internal/features/storage and are registered in
// internal/cli/wiring.
package storage

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Backend decides the shape of the claims behind homes and group folders.
// Which backend is in use is chosen elsewhere (the cluster inventory and the
// admin's setting); a backend holds no detection logic.
type Backend interface {
	ID() string // "storage.per-user", "storage.shared"
	Mode() model.HomeMode
	// Reasons lists the catalog codes the backend can cause.
	Reasons() []reasons.Code
	// HomeClaim is the claim for h on storage class class. It has no owner
	// reference, so deleting an app or the Home object keeps the files.
	HomeClaim(h *model.Home, class string, size resource.Quantity) *corev1.PersistentVolumeClaim
	// FolderClaim is the claim for f. A refusal is an *UnavailableError.
	FolderClaim(f *model.SharedFolder, class string, size resource.Quantity, singleNode bool) (*corev1.PersistentVolumeClaim, error)
	// CoLocate is true when all of a user's pods must run on one node.
	CoLocate(singleNode bool) bool
}

// UnavailableError is a refusal the person or admin can act on; Code is the
// catalog reason to show.
type UnavailableError struct{ Code reasons.Code }

func (e *UnavailableError) Error() string { return string(e.Code) }

// NewClaim is the claim both backends build; they differ only in access
// mode. labels and annotations are copied.
func NewClaim(name, namespace, class string, size resource.Quantity, mode corev1.PersistentVolumeAccessMode, labels, annotations map[string]string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels, Annotations: annotations},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{mode},
			StorageClassName: ptr.To(class),
			VolumeMode:       ptr.To(corev1.PersistentVolumeFilesystem),
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: size}},
		},
	}
}

// HomeClaim builds h's claim with the given access mode.
func HomeClaim(h *model.Home, class string, size resource.Quantity, mode corev1.PersistentVolumeAccessMode) *corev1.PersistentVolumeClaim {
	return NewClaim(naming.HomeClaimName(h.Spec.Owner), h.Namespace, class, size, mode,
		map[string]string{model.LabelOwner: h.Spec.Owner},
		map[string]string{model.AnnotationOwnerID: h.Spec.Owner})
}

// FolderClaim builds f's claim with the given access mode.
func FolderClaim(f *model.SharedFolder, class string, size resource.Quantity, mode corev1.PersistentVolumeAccessMode) *corev1.PersistentVolumeClaim {
	return NewClaim(naming.FolderClaimName(f.Name), f.Namespace, class, size, mode,
		map[string]string{model.LabelGroupFolder: f.Name},
		map[string]string{model.AnnotationGroup: f.Spec.Group})
}

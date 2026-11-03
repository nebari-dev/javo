// Package shared keeps homes and group folders on a shared drive
// (ReadWriteMany), so a user's pods can run on any node.
package shared

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/ext/storage"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// ID is the backend's registry key.
const ID = "storage.shared"

// Backend is the shared drive backend.
type Backend struct{}

var _ storage.Backend = Backend{}

// New returns the shared backend.
func New() Backend { return Backend{} }

func (Backend) ID() string              { return ID }
func (Backend) Mode() model.HomeMode    { return model.HomeModeShared }
func (Backend) Reasons() []reasons.Code { return nil }
func (Backend) CoLocate(bool) bool      { return false }

func (Backend) HomeClaim(h *model.Home, class string, size resource.Quantity) *corev1.PersistentVolumeClaim {
	return storage.HomeClaim(h, class, size, corev1.ReadWriteMany)
}

func (Backend) FolderClaim(f *model.SharedFolder, class string, size resource.Quantity, _ bool) (*corev1.PersistentVolumeClaim, error) {
	return storage.FolderClaim(f, class, size, corev1.ReadWriteMany), nil
}

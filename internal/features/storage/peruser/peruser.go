// Package peruser keeps each home on its own disk (ReadWriteOnce). A disk
// attaches to one node, so a user's pods must share a node on a multi-node
// cluster, and group folders need a single node.
package peruser

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/ext/storage"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// ID is the backend's registry key.
const ID = "storage.per-user"

// Backend is the per-user disk backend.
type Backend struct{}

var _ storage.Backend = Backend{}

// New returns the per-user backend.
func New() Backend { return Backend{} }

func (Backend) ID() string                    { return ID }
func (Backend) Mode() model.HomeMode          { return model.HomeModePerUser }
func (Backend) Reasons() []reasons.Code       { return []reasons.Code{reasons.SharedFoldersUnavailable} }
func (Backend) CoLocate(singleNode bool) bool { return !singleNode }

func (Backend) HomeClaim(h *model.Home, class string, size resource.Quantity) *corev1.PersistentVolumeClaim {
	return storage.HomeClaim(h, class, size, corev1.ReadWriteOnce)
}

// FolderClaim refuses on many nodes: a disk cannot be mounted from several.
func (Backend) FolderClaim(f *model.SharedFolder, class string, size resource.Quantity, singleNode bool) (*corev1.PersistentVolumeClaim, error) {
	if !singleNode {
		return nil, &storage.UnavailableError{Code: reasons.SharedFoldersUnavailable}
	}
	return storage.FolderClaim(f, class, size, corev1.ReadWriteOnce), nil
}

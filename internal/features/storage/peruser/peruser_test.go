package peruser_test

import (
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/ext/storage"
	"github.com/nebari-dev/javo/internal/ext/storage/conformancetest"
	"github.com/nebari-dev/javo/internal/features/storage/peruser"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestConformance(t *testing.T) { conformancetest.Run(t, peruser.New()) }

func TestHomeIsOneDiskForOneNode(t *testing.T) {
	b := peruser.New()
	if b.Mode() != model.HomeModePerUser {
		t.Fatalf("Mode = %s", b.Mode())
	}
	h := &model.Home{Spec: model.HomeSpec{Owner: "alice-1a2b3c4d"}}
	pvc := b.HomeClaim(h, "fast", resource.MustParse("10Gi"))
	if !slices.Equal(pvc.Spec.AccessModes, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}) {
		t.Fatalf("access modes = %v", pvc.Spec.AccessModes)
	}
}

func TestFolderNeedsOneNode(t *testing.T) {
	b := peruser.New()
	f := &model.SharedFolder{Name: "data-science", Spec: model.SharedFolderSpec{Group: "data-science"}}
	size := resource.MustParse("20Gi")

	_, err := b.FolderClaim(f, "fast", size, false)
	var un *storage.UnavailableError
	if !errors.As(err, &un) || un.Code != reasons.SharedFoldersUnavailable {
		t.Fatalf("multi-node error = %v, want SharedFoldersUnavailable", err)
	}

	pvc, err := b.FolderClaim(f, "fast", size, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pvc.Spec.AccessModes, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}) {
		t.Fatalf("access modes = %v", pvc.Spec.AccessModes)
	}
}

func TestCoLocateOnlyOnManyNodes(t *testing.T) {
	b := peruser.New()
	if !b.CoLocate(false) {
		t.Error("many nodes: a user's pods must share a node")
	}
	if b.CoLocate(true) {
		t.Error("one node needs no affinity")
	}
}

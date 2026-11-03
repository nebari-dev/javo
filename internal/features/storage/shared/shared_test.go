package shared_test

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/ext/storage/conformancetest"
	"github.com/nebari-dev/javo/internal/features/storage/shared"
	"github.com/nebari-dev/javo/internal/model"
)

func TestConformance(t *testing.T) { conformancetest.Run(t, shared.New()) }

func TestHomeAndFolderAreReadWriteMany(t *testing.T) {
	b := shared.New()
	if b.Mode() != model.HomeModeShared {
		t.Fatalf("Mode = %s", b.Mode())
	}
	want := []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
	size := resource.MustParse("10Gi")

	h := &model.Home{Spec: model.HomeSpec{Owner: "alice-1a2b3c4d"}}
	if got := b.HomeClaim(h, "nfs", size).Spec.AccessModes; !slices.Equal(got, want) {
		t.Fatalf("home access modes = %v", got)
	}

	f := &model.SharedFolder{Name: "data-science", Spec: model.SharedFolderSpec{Group: "data-science"}}
	for _, single := range []bool{true, false} {
		pvc, err := b.FolderClaim(f, "nfs", size, single)
		if err != nil {
			t.Fatalf("singleNode=%v: %v", single, err)
		}
		if !slices.Equal(pvc.Spec.AccessModes, want) {
			t.Fatalf("folder access modes = %v", pvc.Spec.AccessModes)
		}
	}
}

func TestNeverCoLocates(t *testing.T) {
	b := shared.New()
	if b.CoLocate(true) || b.CoLocate(false) {
		t.Fatal("shared mode must not pin pods to a node: it would block scale-down")
	}
}

// Package conformancetest is the test suite every storage.Backend must pass.
package conformancetest

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/ext/storage"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Run checks the claims b builds and the reasons it declares. The folder
// claim is built with singleNode set so that every backend can make one.
func Run(t *testing.T, b storage.Backend) {
	t.Helper()
	size := resource.MustParse("7Gi")

	t.Run("home claim", func(t *testing.T) {
		h := &model.Home{
			Namespace: "javo-apps",
			Spec:      model.HomeSpec{Owner: "alice-1a2b3c4d"},
		}
		pvc := b.HomeClaim(h, "fast", size)
		checkClaim(t, pvc, "fast", size)
		if pvc.Namespace != "javo-apps" {
			t.Errorf("namespace = %q, want the Home's", pvc.Namespace)
		}
		if got := pvc.Labels[model.LabelOwner]; got != "alice-1a2b3c4d" {
			t.Errorf("owner label = %q", got)
		}
		if got := pvc.Annotations[model.AnnotationOwnerID]; got != "alice-1a2b3c4d" {
			t.Errorf("owner-id annotation = %q", got)
		}
	})

	t.Run("folder claim", func(t *testing.T) {
		f := &model.SharedFolder{
			Name: "data-science", Namespace: "javo-apps",
			Spec: model.SharedFolderSpec{Group: "data-science"},
		}
		pvc, err := b.FolderClaim(f, "fast", size, true)
		if err != nil {
			t.Fatalf("FolderClaim on a single node: %v", err)
		}
		checkClaim(t, pvc, "fast", size)
		if got := pvc.Labels[model.LabelGroupFolder]; got != "data-science" {
			t.Errorf("group-folder label = %q", got)
		}
		if _, ok := pvc.Labels[model.LabelOwner]; ok {
			t.Error("a folder claim must not carry an owner label")
		}
	})

	t.Run("reasons exist in the catalog", func(t *testing.T) {
		for _, c := range b.Reasons() {
			if _, ok := reasons.Lookup(c); !ok {
				t.Errorf("reason %s is not in the catalog", c)
			}
		}
	})
}

func checkClaim(t *testing.T, pvc *corev1.PersistentVolumeClaim, class string, size resource.Quantity) {
	t.Helper()
	if len(pvc.OwnerReferences) != 0 {
		t.Errorf("claim has owner references %v: deleting the owner would delete the files", pvc.OwnerReferences)
	}
	if got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(size) != 0 {
		t.Errorf("requested size = %s, want %s", got.String(), size.String())
	}
	if m := pvc.Spec.VolumeMode; m == nil || *m != corev1.PersistentVolumeFilesystem {
		t.Errorf("volumeMode = %v, want Filesystem", m)
	}
	if c := pvc.Spec.StorageClassName; c == nil || *c != class {
		t.Errorf("storageClassName = %v, want %q", c, class)
	}
}

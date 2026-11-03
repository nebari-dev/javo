package inventory_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/nebari-dev/javo/internal/storage/inventory"
)

func node(name, zone string) *corev1.Node {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if zone != "" {
		n.Labels = map[string]string{"topology.kubernetes.io/zone": zone}
	}
	return n
}

// reader builds a fake client. Typed lists come from objs; unstructured lists
// (VolumeSnapshotClass) go to snapshots.
func reader(t *testing.T, snapshots func(*unstructured.UnstructuredList) error, objs ...client.Object) client.Reader {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if u, ok := list.(*unstructured.UnstructuredList); ok {
				return snapshots(u)
			}
			return c.List(ctx, list, opts...)
		},
	}).Build()
}

func TestReadSummarisesTheCluster(t *testing.T) {
	r := reader(t, func(l *unstructured.UnstructuredList) error {
		vsc := unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "snapshot.storage.k8s.io/v1", "kind": "VolumeSnapshotClass",
			"metadata": map[string]any{"name": "csi"}, "driver": "driver.longhorn.io",
		}}
		l.Items = []unstructured.Unstructured{vsc}
		return nil
	},
		node("n1", "a"), node("n2", "b"), node("n3", "a"), node("n4", ""),
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "longhorn"}, Provisioner: "driver.longhorn.io"},
		&storagev1.CSIDriver{ObjectMeta: metav1.ObjectMeta{Name: "driver.longhorn.io"}},
		&storagev1.CSINode{ObjectMeta: metav1.ObjectMeta{Name: "n1"}},
	)
	inv, err := inventory.Read(context.Background(), r, r)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Nodes != 4 {
		t.Errorf("nodes = %d, want 4", inv.Nodes)
	}
	if len(inv.Zones) != 2 || inv.Zones[0] != "a" || inv.Zones[1] != "b" {
		t.Errorf("zones = %v, want [a b]", inv.Zones)
	}
	if len(inv.Classes) != 1 || len(inv.CSINodes) != 1 {
		t.Errorf("classes = %d, csinodes = %d, want 1 and 1", len(inv.Classes), len(inv.CSINodes))
	}
	if _, ok := inv.Drivers["driver.longhorn.io"]; !ok {
		t.Errorf("drivers = %v, want driver.longhorn.io", inv.Drivers)
	}
	if !inv.SnapshotDrivers["driver.longhorn.io"] {
		t.Errorf("snapshot drivers = %v, want driver.longhorn.io", inv.SnapshotDrivers)
	}
}

func TestReadWithoutSnapshotCRDMeansNoSnapshotSupport(t *testing.T) {
	r := reader(t, func(*unstructured.UnstructuredList) error {
		return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "snapshot.storage.k8s.io", Kind: "VolumeSnapshotClass"}}
	}, node("n1", ""))
	inv, err := inventory.Read(context.Background(), r, r)
	if err != nil {
		t.Fatalf("a missing snapshot CRD must not be an error: %v", err)
	}
	if len(inv.SnapshotDrivers) != 0 || inv.Nodes != 1 {
		t.Errorf("got %+v", inv)
	}
}

// Storage kinds and nodes are read through the cached client; only the
// snapshot classes go to the API reader, so a node status update never costs
// a list against the API server.
func TestReadUsesTheCacheForTypedKindsAndTheAPIForSnapshots(t *testing.T) {
	cached := reader(t, func(*unstructured.UnstructuredList) error {
		t.Error("snapshot classes were listed through the cached client")
		return nil
	}, node("n1", "a"), &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "std"}, Provisioner: "p"})
	api := reader(t, func(l *unstructured.UnstructuredList) error {
		l.Items = []unstructured.Unstructured{{Object: map[string]any{
			"apiVersion": "snapshot.storage.k8s.io/v1", "kind": "VolumeSnapshotClass",
			"metadata": map[string]any{"name": "csi"}, "driver": "p",
		}}}
		return nil
	}) // holds no typed objects: any typed list through it would find nothing
	inv, err := inventory.Read(context.Background(), cached, api)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Nodes != 1 || len(inv.Classes) != 1 || !inv.SnapshotDrivers["p"] {
		t.Errorf("got %+v, want 1 node, 1 class read from the cache and snapshot driver p from the API", inv)
	}
}

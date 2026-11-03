// Package inventory reads what storage the cluster offers. It is read-only:
// it only lists StorageClass, CSIDriver, CSINode, Node and VolumeSnapshotClass.
package inventory

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const zoneLabel = "topology.kubernetes.io/zone"

// Inventory is what one read of the cluster found.
type Inventory struct {
	Classes  []storagev1.StorageClass
	Drivers  map[string]storagev1.CSIDriver
	CSINodes []storagev1.CSINode
	Nodes    int
	// Zones are the distinct zone labels of the nodes, sorted.
	Zones []string
	// SnapshotDrivers holds the CSI drivers that have a VolumeSnapshotClass.
	SnapshotDrivers map[string]bool
}

var snapshotClassList = schema.GroupVersionKind{Group: "snapshot.storage.k8s.io", Version: "v1", Kind: "VolumeSnapshotClassList"}

// Read lists the cluster. StorageClass, CSIDriver, CSINode and Node come from
// cached, so a reconcile costs no API round trip for them. VolumeSnapshotClass
// is listed through api as unstructured: no snapshot client module is needed,
// and the CRD may be absent, which a cache cannot start an informer for. A
// cluster without the snapshot CRDs has no snapshot support, which is not an
// error.
func Read(ctx context.Context, cached, api client.Reader) (Inventory, error) {
	r := cached
	inv := Inventory{Drivers: map[string]storagev1.CSIDriver{}, SnapshotDrivers: map[string]bool{}}

	var classes storagev1.StorageClassList
	if err := r.List(ctx, &classes); err != nil {
		return inv, fmt.Errorf("list storage classes: %w", err)
	}
	inv.Classes = classes.Items

	var drivers storagev1.CSIDriverList
	if err := r.List(ctx, &drivers); err != nil {
		return inv, fmt.Errorf("list csi drivers: %w", err)
	}
	for _, d := range drivers.Items {
		inv.Drivers[d.Name] = d
	}

	var csiNodes storagev1.CSINodeList
	if err := r.List(ctx, &csiNodes); err != nil {
		return inv, fmt.Errorf("list csi nodes: %w", err)
	}
	inv.CSINodes = csiNodes.Items

	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		return inv, fmt.Errorf("list nodes: %w", err)
	}
	inv.Nodes = len(nodes.Items)
	for _, n := range nodes.Items {
		if z := n.Labels[zoneLabel]; z != "" && !slices.Contains(inv.Zones, z) {
			inv.Zones = append(inv.Zones, z)
		}
	}
	slices.Sort(inv.Zones)

	snaps := &unstructured.UnstructuredList{}
	snaps.SetGroupVersionKind(snapshotClassList)
	if err := api.List(ctx, snaps); err != nil {
		if meta.IsNoMatchError(err) {
			return inv, nil
		}
		return inv, fmt.Errorf("list volume snapshot classes: %w", err)
	}
	for _, s := range snaps.Items {
		if d, ok, _ := unstructured.NestedString(s.Object, "driver"); ok {
			inv.SnapshotDrivers[d] = true
		}
	}
	return inv, nil
}

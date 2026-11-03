package capability

import (
	"slices"

	storagev1 "k8s.io/api/storage/v1"

	"github.com/nebari-dev/javo/internal/model"
)

const defaultClassAnnotation = "storageclass.kubernetes.io/is-default-class"

// Classify builds the capability record of one StorageClass. Precedence for
// rwxFile, posixOwnership and sizeEnforced: admin override, then the probe
// (rwxFile and posixOwnership only, and only for the class it ran on), then
// the known-driver table, then unknown.
//
// snapshotDrivers holds the CSI drivers that have a VolumeSnapshotClass (nil
// when the snapshot CRD is missing, which means no snapshot support). A class
// shows that snapshots are configured, not that they work, so the fact is
// known, never verified.
//
// The CSIDriver is accepted so the signature matches what the inventory
// reconciler lists; no fact is derived from it today.
func Classify(sc *storagev1.StorageClass, _ *storagev1.CSIDriver, csiNodes []storagev1.CSINode, overrides []model.ClassOverride, probe *model.ProbeStatus, snapshotDrivers map[string]bool) model.ClassCapability {
	out := model.ClassCapability{
		Name:        sc.Name,
		Provisioner: sc.Provisioner,
		IsDefault:   sc.Annotations[defaultClassAnnotation] == "true",
		BindingMode: string(storagev1.VolumeBindingImmediate),
		Snapshots:   model.Fact{Value: snapshotDrivers[sc.Provisioner], Provenance: model.ProvenanceKnown},
		Expansion: model.Fact{
			Value:      sc.AllowVolumeExpansion != nil && *sc.AllowVolumeExpansion,
			Provenance: model.ProvenanceKnown,
		},
	}
	if sc.VolumeBindingMode != nil {
		out.BindingMode = string(*sc.VolumeBindingMode)
	}

	entry, known := lookup(sc.Provisioner, sc.Parameters)
	table := func(v bool) model.Fact {
		if !known {
			return model.Fact{Provenance: model.ProvenanceUnknown}
		}
		return model.Fact{Value: v, Provenance: model.ProvenanceKnown}
	}
	out.RWXFile = table(entry.rwxFile)
	out.PosixOwnership = table(entry.posixOwnership)
	out.SizeEnforced = table(entry.sizeEnforced)
	out.Notes = slices.Clone(entry.notes)

	if probe != nil && probe.StorageClassName == sc.Name &&
		(probe.Result == model.ProbePassed || probe.Result == model.ProbeFailed) {
		ok := model.Fact{Value: probe.Result == model.ProbePassed, Provenance: model.ProvenanceVerified}
		out.RWXFile, out.PosixOwnership = ok, ok
	}

	for _, o := range overrides {
		if o.Name != sc.Name {
			continue
		}
		apply := func(dst *model.Fact, v *bool) {
			if v != nil {
				*dst = model.Fact{Value: *v, Provenance: model.ProvenanceOverride}
			}
		}
		apply(&out.RWXFile, o.RWXFile)
		apply(&out.PosixOwnership, o.PosixOwnership)
		apply(&out.SizeEnforced, o.SizeEnforced)
	}

	out.VolumesPerNode = volumesPerNode(sc.Provisioner, csiNodes)
	return out
}

// volumesPerNode is the smallest attach limit any node reports for the driver.
func volumesPerNode(provisioner string, nodes []storagev1.CSINode) *int32 {
	var min *int32
	for _, n := range nodes {
		for _, d := range n.Spec.Drivers {
			if d.Name != provisioner || d.Allocatable == nil || d.Allocatable.Count == nil {
				continue
			}
			if c := *d.Allocatable.Count; min == nil || c < *min {
				min = &c
			}
		}
	}
	return min
}

package capability_test

import (
	"slices"
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/storage/capability"
)

func class(provisioner string, params map[string]string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "c"},
		Provisioner: provisioner,
		Parameters:  params,
	}
}

func fact(v bool, p model.Provenance) model.Fact {
	return model.Fact{Value: v, Provenance: p}
}

func TestClassifyKnownDrivers(t *testing.T) {
	known := model.ProvenanceKnown
	rows := []struct {
		name        string
		provisioner string
		params      map[string]string
		rwx, posix  bool
		size        bool
		notes       []string
	}{
		{"local-path", "rancher.io/local-path", nil, false, true, false, nil},
		{"ebs", "ebs.csi.aws.com", nil, false, true, true, nil},
		{"ebs auto mode", "ebs.csi.eks.amazonaws.com", nil, false, true, true, nil},
		{"gce pd", "pd.csi.storage.gke.io", nil, false, true, true, nil},
		{"azure disk", "disk.csi.azure.com", nil, false, true, true, nil},
		{"hetzner", "csi.hetzner.cloud", nil, false, true, true, nil},
		{"ceph rbd", "rbd.csi.ceph.com", nil, false, true, true, nil},
		{"efs", "efs.csi.aws.com", nil, true, false, false, nil},
		{"azure files smb default", "file.csi.azure.com", nil, true, false, true, nil},
		{"azure files smb explicit", "file.csi.azure.com", map[string]string{"protocol": "smb"}, true, false, true, nil},
		{"azure files nfs", "file.csi.azure.com", map[string]string{"protocol": "nfs"}, true, true, true, nil},
		{"longhorn", "driver.longhorn.io", nil, true, true, true, []string{capability.NoteNeedsNfsClient}},
		{"filestore", "filestore.csi.storage.gke.io", nil, true, true, true, []string{capability.NoteMinimumSizeBilled}},
		{"cephfs", "cephfs.csi.ceph.com", nil, true, true, true, nil},
		{"nfs csi", "nfs.csi.k8s.io", nil, true, true, false, nil},
		{"nfs subdir", "k8s-sigs.io/nfs-subdir-external-provisioner", nil, true, true, false, nil},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got := capability.Classify(class(r.provisioner, r.params), nil, nil, nil, nil, nil)
			if got.RWXFile != fact(r.rwx, known) {
				t.Errorf("rwxFile = %+v", got.RWXFile)
			}
			if got.PosixOwnership != fact(r.posix, known) {
				t.Errorf("posixOwnership = %+v", got.PosixOwnership)
			}
			if got.SizeEnforced != fact(r.size, known) {
				t.Errorf("sizeEnforced = %+v", got.SizeEnforced)
			}
			if !slices.Equal(got.Notes, r.notes) {
				t.Errorf("notes = %v, want %v", got.Notes, r.notes)
			}
			if got.Provisioner != r.provisioner || got.Name != "c" {
				t.Errorf("identity = %q %q", got.Name, got.Provisioner)
			}
		})
	}
}

func TestClassifyUnknownProvisioner(t *testing.T) {
	got := capability.Classify(class("example.com/unknown", nil), nil, nil, nil, nil, nil)
	for name, f := range map[string]model.Fact{
		"rwxFile": got.RWXFile, "posixOwnership": got.PosixOwnership, "sizeEnforced": got.SizeEnforced,
	} {
		if f != fact(false, model.ProvenanceUnknown) {
			t.Errorf("%s = %+v, want false/unknown", name, f)
		}
	}
}

func TestClassifyOverrideBeatsTable(t *testing.T) {
	no := false
	ov := []model.ClassOverride{{Name: "c", RWXFile: &no}, {Name: "other", PosixOwnership: &no}}
	got := capability.Classify(class("driver.longhorn.io", nil), nil, nil, ov, nil, nil)
	if got.RWXFile != fact(false, model.ProvenanceOverride) {
		t.Errorf("rwxFile = %+v", got.RWXFile)
	}
	if got.PosixOwnership != fact(true, model.ProvenanceKnown) {
		t.Errorf("an override for another class leaked: %+v", got.PosixOwnership)
	}
}

func TestClassifyProbe(t *testing.T) {
	probe := func(class string, r model.ProbeResult) *model.ProbeStatus {
		return &model.ProbeStatus{StorageClassName: class, Result: r}
	}
	verified := model.ProvenanceVerified
	t.Run("passed on unknown class", func(t *testing.T) {
		got := capability.Classify(class("example.com/unknown", nil), nil, nil, nil, probe("c", model.ProbePassed), nil)
		if got.RWXFile != fact(true, verified) || got.PosixOwnership != fact(true, verified) {
			t.Errorf("got %+v %+v", got.RWXFile, got.PosixOwnership)
		}
		if got.SizeEnforced.Provenance != model.ProvenanceUnknown {
			t.Errorf("the probe does not test size limits: %+v", got.SizeEnforced)
		}
	})
	t.Run("failed drops a known rwx class", func(t *testing.T) {
		got := capability.Classify(class("driver.longhorn.io", nil), nil, nil, nil, probe("c", model.ProbeFailed), nil)
		if got.RWXFile != fact(false, verified) || got.PosixOwnership != fact(false, verified) {
			t.Errorf("got %+v %+v", got.RWXFile, got.PosixOwnership)
		}
	})
	t.Run("override beats the probe", func(t *testing.T) {
		yes := true
		ov := []model.ClassOverride{{Name: "c", RWXFile: &yes}}
		got := capability.Classify(class("driver.longhorn.io", nil), nil, nil, ov, probe("c", model.ProbeFailed), nil)
		if got.RWXFile != fact(true, model.ProvenanceOverride) {
			t.Errorf("rwxFile = %+v", got.RWXFile)
		}
		if got.PosixOwnership != fact(false, verified) {
			t.Errorf("posixOwnership = %+v", got.PosixOwnership)
		}
	})
	t.Run("probe of another class or still running is ignored", func(t *testing.T) {
		for _, p := range []*model.ProbeStatus{probe("other", model.ProbePassed), probe("c", model.ProbeRunning)} {
			got := capability.Classify(class("example.com/unknown", nil), nil, nil, nil, p, nil)
			if got.RWXFile != fact(false, model.ProvenanceUnknown) {
				t.Errorf("rwxFile = %+v", got.RWXFile)
			}
		}
	})
}

func TestClassifyClassSettings(t *testing.T) {
	expand := true
	wffc := storagev1.VolumeBindingWaitForFirstConsumer
	sc := class("ebs.csi.aws.com", nil)
	sc.AllowVolumeExpansion = &expand
	sc.VolumeBindingMode = &wffc
	sc.Annotations = map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}

	five, nine := int32(25), int32(39)
	nodes := []storagev1.CSINode{
		{Spec: storagev1.CSINodeSpec{Drivers: []storagev1.CSINodeDriver{
			{Name: "ebs.csi.aws.com", Allocatable: &storagev1.VolumeNodeResources{Count: &nine}},
			{Name: "other.csi", Allocatable: &storagev1.VolumeNodeResources{Count: &five}},
		}}},
		{Spec: storagev1.CSINodeSpec{Drivers: []storagev1.CSINodeDriver{
			{Name: "ebs.csi.aws.com", Allocatable: &storagev1.VolumeNodeResources{Count: &five}},
		}}},
		{Spec: storagev1.CSINodeSpec{Drivers: []storagev1.CSINodeDriver{{Name: "ebs.csi.aws.com"}}}},
	}
	got := capability.Classify(sc, nil, nodes, nil, nil, nil)
	if got.Expansion != fact(true, model.ProvenanceKnown) {
		t.Errorf("expansion = %+v", got.Expansion)
	}
	if got.BindingMode != "WaitForFirstConsumer" {
		t.Errorf("bindingMode = %q", got.BindingMode)
	}
	if !got.IsDefault {
		t.Error("default class annotation not read")
	}
	if got.VolumesPerNode == nil || *got.VolumesPerNode != 25 {
		t.Errorf("volumesPerNode = %v, want the smallest reported count 25", got.VolumesPerNode)
	}

	plain := capability.Classify(class("ebs.csi.aws.com", nil), nil, nil, nil, nil, nil)
	if plain.Expansion != fact(false, model.ProvenanceKnown) || plain.VolumesPerNode != nil || plain.IsDefault {
		t.Errorf("plain class: %+v", plain)
	}
	if plain.BindingMode != "Immediate" {
		t.Errorf("bindingMode with none set = %q, want Immediate (the Kubernetes default)", plain.BindingMode)
	}
}

func TestClassifySnapshots(t *testing.T) {
	known := model.ProvenanceKnown
	rows := []struct {
		name    string
		drivers map[string]bool
		want    model.Fact
	}{
		{"snapshot class for the driver", map[string]bool{"ebs.csi.aws.com": true}, fact(true, known)},
		{"snapshot class for another driver only", map[string]bool{"other.csi": true}, fact(false, known)},
		{"snapshot CRD missing", nil, fact(false, known)},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got := capability.Classify(class("ebs.csi.aws.com", nil), nil, nil, nil, nil, r.drivers)
			if got.Snapshots != r.want {
				t.Errorf("snapshots = %+v, want %+v", got.Snapshots, r.want)
			}
		})
	}
}

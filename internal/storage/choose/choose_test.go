package choose_test

import (
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/storage/capability"
	"github.com/nebari-dev/javo/internal/storage/choose"
)

func sc(name, provisioner string, isDefault bool, params map[string]string) *storagev1.StorageClass {
	c := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}, Provisioner: provisioner, Parameters: params}
	if isDefault {
		c.Annotations = map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}
	}
	return c
}

func classify(probe *model.ProbeStatus, classes ...*storagev1.StorageClass) []model.ClassCapability {
	out := make([]model.ClassCapability, 0, len(classes))
	for _, c := range classes {
		out = append(out, capability.Classify(c, nil, nil, nil, probe, nil))
	}
	return out
}

func TestChoose(t *testing.T) {
	kind := sc("standard", "rancher.io/local-path", true, nil)
	hcloud := sc("hcloud-volumes", "csi.hetzner.cloud", true, nil)
	longhorn := sc("longhorn", "driver.longhorn.io", false, nil)
	aksDisk := sc("managed-csi", "disk.csi.azure.com", true, nil)
	aksSMB := sc("azurefile-csi", "file.csi.azure.com", false, nil)
	nfs := sc("nfs", "nfs.csi.k8s.io", false, nil)

	longhornFailed := &model.ProbeStatus{StorageClassName: "longhorn", Result: model.ProbeFailed}

	rows := []struct {
		name  string
		caps  []model.ClassCapability
		nodes int
		spec  model.ClusterStorageSpec
		want  model.StorageChoice
	}{
		{"kind: one node, local-path", classify(nil, kind), 1, model.ClusterStorageSpec{},
			model.StorageChoice{Mode: model.HomeModePerUser, SingleNode: true, HomeClass: "standard", Source: "auto"}},
		{"hetzner: longhorn is shared", classify(nil, hcloud, longhorn), 3, model.ClusterStorageSpec{},
			model.StorageChoice{Mode: model.HomeModeShared, HomeClass: "longhorn", SharedClass: "longhorn", Source: "auto"}},
		{"hetzner: longhorn probe failed", classify(longhornFailed, hcloud, longhorn), 3, model.ClusterStorageSpec{},
			model.StorageChoice{Mode: model.HomeModePerUser, HomeClass: "hcloud-volumes", Source: "auto"}},
		{"aks: azure files over SMB is never shared", classify(nil, aksDisk, aksSMB), 3, model.ClusterStorageSpec{},
			model.StorageChoice{Mode: model.HomeModePerUser, HomeClass: "managed-csi", Source: "auto"}},
		{"override perUser with a shared class present", classify(nil, hcloud, longhorn), 3,
			model.ClusterStorageSpec{Home: model.HomeStorageSpec{Mode: model.HomeModePerUser}},
			model.StorageChoice{Mode: model.HomeModePerUser, HomeClass: "hcloud-volumes", SharedClass: "longhorn", Source: "override"}},
		{"override shared without an RWX class", classify(nil, hcloud), 3,
			model.ClusterStorageSpec{Home: model.HomeStorageSpec{Mode: model.HomeModeShared}},
			model.StorageChoice{Mode: model.HomeModeShared, Source: "override"}},
		{"override shared names a class that cannot share", classify(nil, hcloud, longhorn), 3,
			model.ClusterStorageSpec{Home: model.HomeStorageSpec{Mode: model.HomeModeShared, StorageClassName: "hcloud-volumes"}},
			model.StorageChoice{Mode: model.HomeModeShared, HomeClass: "hcloud-volumes", SharedClass: "longhorn", Source: "override"}},
		{"perUser class named by the admin", classify(nil, hcloud, kind), 3,
			model.ClusterStorageSpec{Home: model.HomeStorageSpec{StorageClassName: "standard"}},
			model.StorageChoice{Mode: model.HomeModePerUser, HomeClass: "standard", Source: "auto"}},
		{"admin names the shared class", classify(nil, hcloud, longhorn, nfs), 3,
			model.ClusterStorageSpec{Home: model.HomeStorageSpec{StorageClassName: "nfs"}},
			model.StorageChoice{Mode: model.HomeModeShared, HomeClass: "nfs", SharedClass: "nfs", Source: "auto"}},
		{"default class is tried first, then by name", classify(nil, longhorn, sc("a-nfs", "nfs.csi.k8s.io", false, nil), sc("z-efs", "cephfs.csi.ceph.com", true, nil)), 3, model.ClusterStorageSpec{},
			model.StorageChoice{Mode: model.HomeModeShared, HomeClass: "z-efs", SharedClass: "z-efs", Source: "auto"}},
		{"no default class leaves the home class empty", classify(nil, sc("fast", "ebs.csi.aws.com", false, nil)), 2, model.ClusterStorageSpec{},
			model.StorageChoice{Mode: model.HomeModePerUser, Source: "auto"}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if got := choose.Choose(r.caps, r.nodes, r.spec); got != r.want {
				t.Errorf("got %+v\nwant %+v", got, r.want)
			}
		})
	}
}

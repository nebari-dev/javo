// Package capability classifies StorageClasses: what each one can do for home
// and shared folders, and where each fact comes from. It is pure; it reads no
// cluster.
package capability

// Notes attached to a class. They are plain strings in ClassCapability.Notes;
// the user-facing wording lives in the reason catalog, not here.
const (
	// NoteNeedsNfsClient: every node needs an NFSv4 client for RWX volumes.
	NoteNeedsNfsClient = "NeedsNfsClient"
	// NoteMinimumSizeBilled: the backend bills a minimum size per volume.
	NoteMinimumSizeBilled = "MinimumSizeBilled"
)

// driverEntry is one row of the known-driver table. "rwxFile" means
// ReadWriteMany with volumeMode Filesystem; RWX block does not count.
type driverEntry struct {
	provisioner string
	// match narrows the entry by StorageClass parameters; nil matches all.
	match          func(params map[string]string) bool
	rwxFile        bool
	posixOwnership bool
	sizeEnforced   bool
	notes          []string
}

// azureFilesNFS reports whether an Azure Files class uses NFS. SMB is the
// default protocol.
func azureFilesNFS(params map[string]string) bool { return params["protocol"] == "nfs" }

func azureFilesSMB(params map[string]string) bool { return !azureFilesNFS(params) }

// drivers is what each known provisioner supports, from the documentation
// linked on each entry. Entries marked inferred are not stated there but
// follow from how the driver works. First match wins.
var drivers = []driverEntry{
	// https://github.com/rancher/local-path-provisioner ("capacity limit will be ignored")
	{provisioner: "rancher.io/local-path", rwxFile: false, posixOwnership: true, sizeEnforced: false},
	// https://github.com/kubernetes-sigs/aws-ebs-csi-driver/blob/master/docs/multi-attach.md
	{provisioner: "ebs.csi.aws.com", rwxFile: false, posixOwnership: true, sizeEnforced: true},
	// Same EBS volumes under EKS Auto Mode (inferred).
	// https://docs.aws.amazon.com/eks/latest/userguide/create-storage-class.html
	{provisioner: "ebs.csi.eks.amazonaws.com", rwxFile: false, posixOwnership: true, sizeEnforced: true},
	// Dynamic access points replace the client's uid/gid with the access point's.
	// https://github.com/kubernetes-sigs/aws-efs-csi-driver/blob/master/docs/parameters.md
	// https://docs.aws.amazon.com/efs/latest/ug/enforce-identity-access-points.html
	{provisioner: "efs.csi.aws.com", rwxFile: true, posixOwnership: false, sizeEnforced: false},
	// https://docs.cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/gce-pd-csi-driver
	{provisioner: "pd.csi.storage.gke.io", rwxFile: false, posixOwnership: true, sizeEnforced: true},
	// https://docs.cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/filestore-csi-driver
	// https://docs.cloud.google.com/filestore/docs/service-tiers
	{provisioner: "filestore.csi.storage.gke.io", rwxFile: true, posixOwnership: true, sizeEnforced: true,
		notes: []string{NoteMinimumSizeBilled}},
	// https://learn.microsoft.com/en-us/azure/aks/concepts-storage
	{provisioner: "disk.csi.azure.com", rwxFile: false, posixOwnership: true, sizeEnforced: true},
	// SMB fixes ownership per mount (uid=/gid= options); NFS keeps POSIX owners.
	// https://github.com/kubernetes-sigs/azurefile-csi-driver/blob/master/docs/driver-parameters.md
	{provisioner: "file.csi.azure.com", match: azureFilesNFS, rwxFile: true, posixOwnership: true, sizeEnforced: true},
	{provisioner: "file.csi.azure.com", match: azureFilesSMB, rwxFile: true, posixOwnership: false, sizeEnforced: true},
	// RWX through an NFSv4.1 share-manager pod; posix and size are inferred (ext4 behind NFS).
	// https://longhorn.io/docs/latest/nodes-and-volumes/volumes/rwx-volumes/
	{provisioner: "driver.longhorn.io", rwxFile: true, posixOwnership: true, sizeEnforced: true,
		notes: []string{NoteNeedsNfsClient}},
	// https://github.com/hetznercloud/csi-driver
	{provisioner: "csi.hetzner.cloud", rwxFile: false, posixOwnership: true, sizeEnforced: true},
	// https://github.com/ceph/ceph-csi/blob/devel/README.md
	{provisioner: "rbd.csi.ceph.com", rwxFile: false, posixOwnership: true, sizeEnforced: true},
	// max_bytes quota is set to the requested size.
	// https://github.com/ceph/ceph-csi/blob/devel/docs/cephfs/deploy.md
	{provisioner: "cephfs.csi.ceph.com", rwxFile: true, posixOwnership: true, sizeEnforced: true},
	// A subdirectory of an existing export, no quota parameter (inferred).
	// https://github.com/kubernetes-csi/csi-driver-nfs
	{provisioner: "nfs.csi.k8s.io", rwxFile: true, posixOwnership: true, sizeEnforced: false},
	// https://github.com/kubernetes-sigs/nfs-subdir-external-provisioner
	{provisioner: "k8s-sigs.io/nfs-subdir-external-provisioner", rwxFile: true, posixOwnership: true, sizeEnforced: false},
}

func lookup(provisioner string, params map[string]string) (driverEntry, bool) {
	for _, e := range drivers {
		if e.provisioner == provisioner && (e.match == nil || e.match(params)) {
			return e, true
		}
	}
	return driverEntry{}, false
}

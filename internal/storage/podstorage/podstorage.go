// Package podstorage is the one place that turns a user's Home, their group
// folders and the scratch settings into pod volumes, mounts, environment,
// annotations, affinity and supplemental groups. App pods and file pods both
// apply its Layout, so they always agree on what is mounted where.
package podstorage

import (
	"path"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
)

const (
	// HomeDir is where the user's home is mounted.
	HomeDir = "/home/user"
	// SharedRoot holds one folder per group the user belongs to.
	SharedRoot = "/shared"
	// ScratchDir is node-local space for environments and caches.
	ScratchDir = "/scratch"

	// HomeVolume, TmpVolume and ScratchVolume are the volume names.
	HomeVolume    = "home"
	TmpVolume     = "tmp"
	ScratchVolume = "scratch"

	tmpDir = "/tmp"
	// FolderSubPath is the folder's own directory inside its claim; the
	// prepare Job makes it and owns the claim root.
	FolderSubPath = "data"
	cacheSubPath  = "cache"

	// SafeToEvictAnnotation lets the cluster autoscaler drain a node whose
	// only local volumes are scratch space.
	SafeToEvictAnnotation = "cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes"
)

// Input is everything a Layout depends on.
type Input struct {
	// Home is the owner's Home; nil or without a claim name falls back to the
	// first-generation claim name.
	Home *model.Home
	// Folders are the ready folders of the owner's groups.
	Folders []model.SharedFolder
	// ScratchMax limits the scratch volume, and /tmp when TmpMax is zero;
	// zero means no limit.
	ScratchMax resource.Quantity
	// TmpMax limits /tmp; zero means ScratchMax's limit.
	TmpMax resource.Quantity
	// DiskMax is the app container's ephemeral-storage limit: all the pod
	// may write to the node's disk (scratch, /tmp, its image layer, logs).
	// Zero means no limit.
	DiskMax resource.Quantity
	// CoLocate keeps all of the owner's pods on one node (Backend.CoLocate).
	CoLocate bool
	// UID and GID are the ids the pod runs as.
	UID, GID int64
	// ReadOnly mounts the home and folders read-only (file pods).
	ReadOnly bool
}

// Layout is what to add to a pod. Apply puts it there.
type Layout struct {
	Volumes     []corev1.Volume
	Mounts      []corev1.VolumeMount
	Env         []corev1.EnvVar
	Annotations map[string]string
	Affinity    *corev1.Affinity
	// SupplementalGroups carry the gids of the group folders. No pod built
	// here sets fsGroup: on drivers with fsGroupPolicy File it would re-group
	// every file of every folder the pod mounts.
	SupplementalGroups    []int64
	RunAsUser, RunAsGroup int64
	// DiskMax is Input.DiskMax.
	DiskMax resource.Quantity
}

// Build returns the layout of owner's pod.
func Build(owner string, in Input) Layout {
	claim := naming.HomeClaimName(owner)
	if in.Home != nil && in.Home.Status.ClaimName != "" {
		claim = in.Home.Status.ClaimName
	}
	// /tmp is node disk like scratch: the kubelet evicts the pod that goes
	// over its limit, not the node's other pods. Without a /tmp limit of its
	// own it gets the scratch limit.
	scratch, tmp := &corev1.EmptyDirVolumeSource{}, &corev1.EmptyDirVolumeSource{}
	if !in.ScratchMax.IsZero() {
		scratch.SizeLimit, tmp.SizeLimit = ptr.To(in.ScratchMax), ptr.To(in.ScratchMax)
	}
	if !in.TmpMax.IsZero() {
		tmp.SizeLimit = ptr.To(in.TmpMax)
	}
	l := Layout{
		DiskMax: in.DiskMax,
		Volumes: []corev1.Volume{
			claimVolume(HomeVolume, claim, in.ReadOnly),
			{Name: TmpVolume, VolumeSource: corev1.VolumeSource{EmptyDir: tmp}},
			{Name: ScratchVolume, VolumeSource: corev1.VolumeSource{EmptyDir: scratch}},
		},
		Mounts: []corev1.VolumeMount{
			{Name: HomeVolume, MountPath: HomeDir, ReadOnly: in.ReadOnly},
			{Name: TmpVolume, MountPath: tmpDir},
			{Name: ScratchVolume, MountPath: ScratchDir},
		},
		Env:         scratchEnv(),
		Annotations: map[string]string{SafeToEvictAnnotation: ScratchVolume + "," + TmpVolume},
		RunAsUser:   in.UID,
		RunAsGroup:  in.GID,
	}
	if !in.ReadOnly {
		// Caches the tools write under ~/.cache go to scratch, not the home.
		l.Mounts = append(l.Mounts, corev1.VolumeMount{Name: ScratchVolume, MountPath: path.Join(HomeDir, ".cache"), SubPath: cacheSubPath})
	}
	for _, f := range in.Folders {
		name := GroupVolume(naming.GroupDir(f.Spec.Group))
		l.Volumes = append(l.Volumes, claimVolume(name, f.Status.ClaimName, in.ReadOnly))
		l.Mounts = append(l.Mounts, corev1.VolumeMount{
			Name: name, MountPath: path.Join(SharedRoot, naming.GroupDir(f.Spec.Group)), SubPath: FolderSubPath, ReadOnly: in.ReadOnly,
		})
		l.SupplementalGroups = append(l.SupplementalGroups, f.Status.GID)
	}
	if in.CoLocate {
		l.Affinity = &corev1.Affinity{PodAffinity: &corev1.PodAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{model.LabelOwner: owner}},
				TopologyKey:   corev1.LabelHostname,
			}},
		}}
	}
	return l
}

// GroupVolume is the name of the volume of the group folder at /shared/<dir>
// (dir is naming.GroupDir of the group).
func GroupVolume(dir string) string { return groupVolumePrefix + dir }

const groupVolumePrefix = "group-"

// IsGroupVolume reports whether name is a group folder's volume.
func IsGroupVolume(name string) bool { return strings.HasPrefix(name, groupVolumePrefix) }

// WithoutFolders is l without the group folders and their gids, for a pod
// that runs code its owner has not reviewed (a pull request's preview).
func (l Layout) WithoutFolders() Layout {
	folder := IsGroupVolume
	l.Volumes = slices.DeleteFunc(slices.Clone(l.Volumes), func(v corev1.Volume) bool { return folder(v.Name) })
	l.Mounts = slices.DeleteFunc(slices.Clone(l.Mounts), func(m corev1.VolumeMount) bool { return folder(m.Name) })
	l.SupplementalGroups = nil
	return l
}

// ReadOnlyFiles is l with the home and the group folders mounted read-only
// in the app container, for a pod that people other than its owner drive (a
// shared app): what they make it do cannot change the code the owner's other
// apps and jobs run. The volumes stay writable, so the platform's own init
// steps (an environment install) still write; caches and /tmp stay
// writable.
func (l Layout) ReadOnlyFiles() Layout {
	l.Mounts = slices.Clone(l.Mounts)
	for i, m := range l.Mounts {
		if (m.Name == HomeVolume && m.MountPath == HomeDir) || strings.HasPrefix(m.Name, groupVolumePrefix) {
			l.Mounts[i].ReadOnly = true
		}
	}
	return l
}

func claimVolume(name, claim string, readOnly bool) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim, ReadOnly: readOnly},
	}}
}

// scratchEnv points every tool cache and the conda and pixi environments at
// scratch, so a large install does not fill the home.
func scratchEnv() []corev1.EnvVar {
	c := ScratchDir + "/" + cacheSubPath
	return []corev1.EnvVar{
		{Name: "XDG_CACHE_HOME", Value: c},
		{Name: "PIP_CACHE_DIR", Value: c + "/pip"},
		{Name: "UV_CACHE_DIR", Value: c + "/uv"},
		{Name: "PIXI_CACHE_DIR", Value: c + "/pixi"},
		{Name: "npm_config_cache", Value: c + "/npm"},
		{Name: "CONDA_PKGS_DIRS", Value: ScratchDir + "/pkgs"},
		{Name: "CONDA_ENVS_PATH", Value: ScratchDir + "/envs"},
	}
}

// Apply adds the layout to pod: volumes, affinity, ids and annotations on
// the pod, mounts and environment on its container c (a pointer into
// pod.Spec.Containers). It never sets fsGroup.
func (l Layout) Apply(pod *corev1.Pod, c *corev1.Container) {
	pod.Spec.Volumes = append(pod.Spec.Volumes, l.Volumes...)
	c.VolumeMounts = append(c.VolumeMounts, l.Mounts...)
	c.Env = append(c.Env, l.Env...)
	if len(l.Annotations) > 0 && pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	for k, v := range l.Annotations {
		pod.Annotations[k] = v
	}
	pod.Spec.Affinity = l.Affinity
	if !l.DiskMax.IsZero() {
		// The kubelet evicts the pod past the limit (ScratchFull). The request
		// is an explicit zero: left out, it would default to the limit and
		// reserve that much disk on the node for every pod.
		if c.Resources.Limits == nil {
			c.Resources.Limits = corev1.ResourceList{}
		}
		if c.Resources.Requests == nil {
			c.Resources.Requests = corev1.ResourceList{}
		}
		c.Resources.Limits[corev1.ResourceEphemeralStorage] = l.DiskMax
		if _, ok := c.Resources.Requests[corev1.ResourceEphemeralStorage]; !ok {
			c.Resources.Requests[corev1.ResourceEphemeralStorage] = resource.MustParse("0")
		}
	}
	if pod.Spec.SecurityContext == nil {
		pod.Spec.SecurityContext = &corev1.PodSecurityContext{}
	}
	sc := pod.Spec.SecurityContext
	sc.RunAsUser, sc.RunAsGroup = ptr.To(l.RunAsUser), ptr.To(l.RunAsGroup)
	sc.SupplementalGroups = l.SupplementalGroups
	if len(l.SupplementalGroups) > 0 {
		sc.SupplementalGroupsPolicy = ptr.To(corev1.SupplementalGroupsPolicyStrict)
	}
}

// ScratchPaths are the places an app's files are not saved: the cache folder
// in the home and the scratch volume (environments live there).
func ScratchPaths() []string { return []string{"~/.cache", ScratchDir} }

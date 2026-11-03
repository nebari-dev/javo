// Package podspec builds the pod spec that app pods and job pods share:
// security context and the one container named app. Callers add the storage
// layout (internal/storage/podstorage), command, args, ports, probes and
// restart policy.
package podspec

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
)

const (
	// ContainerName is the name of the main container in every pod.
	ContainerName = "app"

	// HomeDir is where the user's home is mounted (podstorage owns the paths).
	HomeDir    = podstorage.HomeDir
	HomeVolume = podstorage.HomeVolume
	TmpVolume  = podstorage.TmpVolume
	// HomeScratchVolume is the emptyDir ScratchHome mounts at the home path.
	HomeScratchVolume = "home-scratch"

	// FixedGID is the group every pod's files belong to: the primary group of
	// app pods and the fsGroup of the Jobs that prepare a home.
	FixedGID int64 = 100

	// FixedUID is the uid pods run as when uids are not allocated.
	FixedUID int64 = 1000
)

// RestrictedContext is the container security context every container uses.
func RestrictedContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

// Base returns the pod spec for owner (a person's name) running image with
// resources. It carries no storage and sets no ids or fsGroup;
// podstorage.Layout.Apply adds the volumes, ids and groups.
func Base(owner, image string, resources corev1.ResourceRequirements) corev1.PodSpec {
	return corev1.PodSpec{
		AutomountServiceAccountToken: ptr.To(false),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   ptr.To(true),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{{
			Name:            ContainerName,
			Image:           image,
			Resources:       resources,
			SecurityContext: RestrictedContext(),
			// Images name other homes (the passwd entry, a baked ENV), where
			// writes would be lost with the pod; "~" is the volume. JAVO_USER
			// is the name pod-user.sh gives a uid the image does not list.
			Env: append([]corev1.EnvVar{
				{Name: "HOME", Value: HomeDir},
				{Name: "JAVO_USER", Value: naming.IdentityReadable(owner)},
			}, ColorEnv()...),
		}},
	}
}

// ColorEnv asks the programs in an app or a run to print colours as they do
// in a terminal (pip, uvicorn, pytest, rich, click and others check one of
// these); the log view shows them. They are defaults: a variable of the same
// name the app or job sets replaces them (SetEnv), so they are not reserved.
func ColorEnv() []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "FORCE_COLOR", Value: "1"},
		{Name: "CLICOLOR_FORCE", Value: "1"},
		{Name: "PY_COLORS", Value: "1"},
	}
}

// SetEnv adds vars to c, each replacing an earlier variable of the same name
// (one name defined twice is a warning from the API server).
func SetEnv(c *corev1.Container, vars ...corev1.EnvVar) {
	for _, v := range vars {
		c.Env = slices.DeleteFunc(c.Env, func(e corev1.EnvVar) bool { return e.Name == v.Name })
		c.Env = append(c.Env, v)
	}
}

// ScratchHome gives the main container an empty folder at the home path
// instead of the owner's home: nothing it writes lasts and nothing of the
// owner's is there. The claim volume stays only while an init container
// still mounts it (the copy of a single file out of the home).
func ScratchHome(spec *corev1.PodSpec) {
	for i := range spec.Containers {
		for j := range spec.Containers[i].VolumeMounts {
			if m := &spec.Containers[i].VolumeMounts[j]; m.Name == HomeVolume {
				m.Name = HomeScratchVolume
			}
		}
	}
	spec.Volumes = append(spec.Volumes, corev1.Volume{Name: HomeScratchVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
	for _, c := range spec.InitContainers {
		for _, m := range c.VolumeMounts {
			if m.Name == HomeVolume {
				return
			}
		}
	}
	kept := spec.Volumes[:0]
	for _, v := range spec.Volumes {
		if v.Name != HomeVolume {
			kept = append(kept, v)
		}
	}
	spec.Volumes = kept
}

// Image is the platform's own image and the Secrets the kubelet pulls it
// with: names of kubernetes.io/dockerconfigjson Secrets in the pod's
// namespace. A public image has none.
type Image struct {
	Ref         string
	PullSecrets []string
}

// Pull gives spec the image's pull secrets, each once, after the ones it has.
// An image without any leaves spec as it was.
func (i Image) Pull(spec *corev1.PodSpec) {
	for _, name := range i.PullSecrets {
		ref := corev1.LocalObjectReference{Name: name}
		if !slices.Contains(spec.ImagePullSecrets, ref) {
			spec.ImagePullSecrets = append(spec.ImagePullSecrets, ref)
		}
	}
}

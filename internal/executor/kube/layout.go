package kube

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/workload"
)

// LabelJobName is the label the Job controller puts on a Job's pods.
const LabelJobName = "batch.kubernetes.io/job-name"

// SafeToEvictAnnotation lets the cluster autoscaler drain a node whose only
// local volumes are scratch space.
const SafeToEvictAnnotation = "cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes"

// evictable marks the pod's scratch space as safe to lose.
func evictable(pod *corev1.Pod) {
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[SafeToEvictAnnotation] = podstorage.ScratchVolume + "," + podstorage.TmpVolume
}

// affinity keeps all of owner's pods on one node when coLocate is set.
func affinity(owner string, coLocate bool) *corev1.Affinity {
	if !coLocate {
		return nil
	}
	return &corev1.Affinity{PodAffinity: &corev1.PodAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{model.LabelOwner: owner}},
			TopologyKey:   corev1.LabelHostname,
		}},
	}}
}

// limitDisk limits all c may write to the node's disk; a zero max is no
// limit. The kubelet evicts the pod past the limit (ScratchFull). The
// request is an explicit zero: left out, it would default to the limit and
// reserve that much disk on the node for every pod.
func limitDisk(c *corev1.Container, max resource.Quantity) {
	if max.IsZero() {
		return
	}
	if c.Resources.Limits == nil {
		c.Resources.Limits = corev1.ResourceList{}
	}
	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{}
	}
	c.Resources.Limits[corev1.ResourceEphemeralStorage] = max
	if _, ok := c.Resources.Requests[corev1.ResourceEphemeralStorage]; !ok {
		c.Resources.Requests[corev1.ResourceEphemeralStorage] = resource.MustParse("0")
	}
}

// runAs makes the pod run as u.
func runAs(spec *corev1.PodSpec, u workload.User) {
	if spec.SecurityContext == nil {
		spec.SecurityContext = &corev1.PodSecurityContext{}
	}
	sc := spec.SecurityContext
	sc.RunAsUser, sc.RunAsGroup = ptr.To(u.UID), ptr.To(u.GID)
	sc.SupplementalGroups = u.Groups
	if len(u.Groups) > 0 {
		sc.SupplementalGroupsPolicy = ptr.To(corev1.SupplementalGroupsPolicyStrict)
	}
}

func volume(v workload.Volume) corev1.Volume {
	out := corev1.Volume{Name: v.Name}
	switch {
	case v.Secret != nil:
		src := &corev1.SecretVolumeSource{SecretName: v.Secret.Bundle}
		for _, k := range v.Secret.Keys {
			src.Items = append(src.Items, corev1.KeyToPath{Key: k, Path: k})
		}
		if v.Secret.Optional {
			src.Optional = ptr.To(true)
		}
		if v.Secret.WorldReadable {
			// The kubelet writes the files as root and pods have no
			// fsGroup, so the pod's own uid reads them as "others". Only
			// that uid runs in the pod.
			src.DefaultMode = ptr.To[int32](0o444)
		}
		out.Secret = src
	case v.Source != "":
		out.PersistentVolumeClaim = &corev1.PersistentVolumeClaimVolumeSource{ClaimName: v.Source, ReadOnly: v.ReadOnly}
	default:
		out.EmptyDir = &corev1.EmptyDirVolumeSource{SizeLimit: v.Limit}
	}
	return out
}

func mount(m workload.Mount) corev1.VolumeMount {
	return corev1.VolumeMount{Name: m.Volume, MountPath: m.Path, SubPath: m.SubPath, ReadOnly: m.ReadOnly}
}

func env(e workload.Env) corev1.EnvVar {
	switch {
	case e.RunID:
		// A run is named after its Job, which its pods carry as a label.
		return corev1.EnvVar{Name: e.Name, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{
			FieldPath: "metadata.labels['" + LabelJobName + "']"}}}
	case e.From == nil:
		return corev1.EnvVar{Name: e.Name, Value: e.Value}
	}
	return corev1.EnvVar{Name: e.Name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: e.From.Bundle}, Key: e.From.Key,
	}}}
}

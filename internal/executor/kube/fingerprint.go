package kube

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/workload"
)

// Mark records on pod what it was built from. What the app does not have
// is left out, so a pod built before a field existed reads the same as one
// built without it.
func Mark(pod *corev1.Pod, f workload.Fingerprint) {
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[model.AnnotationAPIAccess] = f.APIAccess
	pod.Annotations[model.AnnotationLayout] = f.Layout
	for name, value := range map[string]string{
		model.AnnotationSize:     f.Size,
		model.AnnotationRun:      f.Run,
		model.AnnotationRestart:  f.Restart,
		model.AnnotationGitToken: f.GitToken,
		model.AnnotationSecrets:  f.Secrets,
	} {
		if value != "" {
			pod.Annotations[name] = value
		}
	}
}

// FingerprintOf reads back what pod was built from.
func FingerprintOf(pod *corev1.Pod) workload.Fingerprint {
	a := pod.Annotations
	return workload.Fingerprint{
		Size:      a[model.AnnotationSize],
		Run:       a[model.AnnotationRun],
		Restart:   a[model.AnnotationRestart],
		APIAccess: apiAccess(pod),
		GitToken:  a[model.AnnotationGitToken],
		Secrets:   a[model.AnnotationSecrets],
		Layout:    a[model.AnnotationLayout],
	}
}

// apiAccess is the API access pod was built with. A pod built before the
// annotation existed had read-write access exactly when it got the token.
func apiAccess(pod *corev1.Pod) string {
	if a, ok := pod.Annotations[model.AnnotationAPIAccess]; ok {
		return a
	}
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == workload.EnvToken {
			return model.APIAccessReadWrite
		}
	}
	return model.APIAccessNone
}

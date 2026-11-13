package kube

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/workload"
)

func appPod(env ...corev1.EnvVar) *corev1.Pod {
	return &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: env}}}}
}

func TestAMarkedPodReadsBackItsFingerprint(t *testing.T) {
	want := workload.Fingerprint{Size: "medium", Run: "r1", Restart: "t1", APIAccess: model.APIAccessRead, GitToken: "3", Secrets: "s1", Layout: "l1"}
	p := appPod()
	Mark(p, want)
	if got := FingerprintOf(p); got != want {
		t.Fatalf("read back %+v, want %+v", got, want)
	}
}

// What an app does not have is not recorded, so a pod built before a field
// existed and one built without it read the same.
func TestMarkLeavesOutWhatTheAppDoesNotHave(t *testing.T) {
	p := appPod()
	Mark(p, workload.Fingerprint{APIAccess: model.APIAccessNone, Layout: "l1"})
	for _, a := range []string{model.AnnotationSize, model.AnnotationRun, model.AnnotationRestart, model.AnnotationGitToken, model.AnnotationSecrets} {
		if _, ok := p.Annotations[a]; ok {
			t.Errorf("%s is recorded", a)
		}
	}
}

// A pod built before API access was recorded had read-write access exactly
// when it got the token.
func TestAPIAccessOfAPodBuiltBeforeItWasRecorded(t *testing.T) {
	if got := FingerprintOf(appPod(corev1.EnvVar{Name: "JAVO_TOKEN"})).APIAccess; got != model.APIAccessReadWrite {
		t.Errorf("with the token: %q", got)
	}
	if got := FingerprintOf(appPod()).APIAccess; got != model.APIAccessNone {
		t.Errorf("without it: %q", got)
	}
}

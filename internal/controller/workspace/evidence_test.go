package workspace

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/status"
)

func TestEvidenceLandsInFailingConditionMessage(t *testing.T) {
	t.Parallel()
	ws, _ := fixtures()
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodRunning,
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:                 "app",
			State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
		}},
	}}
	_, conds, ev := status.Derive(ws, status.Observed{Pod: pod, Now: time.Now()})
	conds = status.WithEvidence(conds, ev)

	started := meta.FindStatusCondition(conds, model.ConditionStarted)
	if started == nil || !strings.Contains(started.Message, "OOMKilled") {
		t.Fatalf("failing condition message lacks evidence: %+v", started)
	}
	if sched := meta.FindStatusCondition(conds, model.ConditionScheduled); strings.Contains(sched.Message, "OOMKilled") {
		t.Fatalf("true condition must keep its short message: %q", sched.Message)
	}
}

func TestEvidenceIsCapped(t *testing.T) {
	t.Parallel()
	var ev []string
	for i := 0; i < 25; i++ {
		ev = append(ev, strings.Repeat("x", 500))
	}
	conds := status.WithEvidence([]metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Message: "title"}}, ev)
	lines := strings.Split(conds[0].Message, "\n")
	if len(lines) != 20 {
		t.Fatalf("lines = %d, want 20", len(lines))
	}
	for _, l := range lines {
		if len(l) != 300 || !strings.HasSuffix(l, "...") {
			t.Fatalf("line len %d, want 300 ending in ...", len(l))
		}
	}
}

func TestNoEvidenceKeepsTitle(t *testing.T) {
	t.Parallel()
	conds := status.WithEvidence([]metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Message: "title"}}, nil)
	if conds[0].Message != "title" {
		t.Fatalf("message = %q", conds[0].Message)
	}
}

// Evidence is often app output; a cut must never split a multi-byte
// character, or the status patch carries invalid UTF-8.
func TestEvidenceCutKeepsValidUTF8(t *testing.T) {
	t.Parallel()
	line := strings.Repeat("é", 400)
	conds := status.WithEvidence([]metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse}}, []string{line})
	got := conds[0].Message
	if !utf8.ValidString(got) {
		t.Fatalf("cut produced invalid UTF-8: %q", got[len(got)-8:])
	}
	if len(got) > 300 || !strings.HasSuffix(got, "...") {
		t.Fatalf("len %d, want <= 300 ending in ...", len(got))
	}
}

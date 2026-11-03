package status

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestClassifyPod(t *testing.T) {
	failedSched := func(msg string) []corev1.Event {
		return []corev1.Event{{Reason: "FailedScheduling", Message: msg}}
	}
	withInit := func(p *corev1.Pod, cs ...corev1.ContainerStatus) *corev1.Pod {
		p.Status.InitContainerStatuses = cs
		return p
	}
	terminated := func(name, reason string, exit int32, msg string) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Reason: reason, ExitCode: exit, Message: msg}}}
	}
	crashLoop := corev1.ContainerStatus{
		Name:                 "app",
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
	}
	evicted := func(reason, msg string) *corev1.Pod {
		p := pod(corev1.PodFailed, true, false)
		p.Status.Reason, p.Status.Message = reason, msg
		return p
	}
	rows := []struct {
		name         string
		pod          *corev1.Pod
		events       []corev1.Event
		wantFailing  string
		wantCode     reasons.Code
		wantOK       bool
		wantEvidence []string // required substrings, one per evidence line
	}{
		{"insufficient cpu", pod(corev1.PodPending, false, false), failedSched("0/3 nodes: Insufficient cpu"),
			model.ConditionScheduled, reasons.NoCapacity, true, []string{"Insufficient cpu"}},
		{"image pull", pod(corev1.PodPending, true, false, corev1.ContainerStatus{
			Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull"}}}), nil,
			model.ConditionImageReady, reasons.ImagePullFailed, true, []string{"ErrImagePull"}},
		{"oom", pod(corev1.PodRunning, true, false, corev1.ContainerStatus{
			Name: "app", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}}), nil,
			model.ConditionStarted, reasons.OutOfMemory, true, []string{"OOMKilled"}},
		{"evicted for scratch space", evicted("Evicted", "Pod ephemeral local storage usage exceeds the total limit of containers 20Gi."), nil,
			model.ConditionStarted, reasons.ScratchFull, true, []string{"ephemeral local storage usage exceeds the total limit of containers 20Gi"}},
		{"evicted for node disk pressure", evicted("Evicted", "The node was low on resource: ephemeral-storage. Threshold quantity: 1Gi."), nil,
			model.ConditionStarted, reasons.ScratchFull, true, []string{"ephemeral-storage"}},
		{"evicted for emptyDir size limit", evicted("Evicted", `Usage of EmptyDir volume "scratch" exceeds the limit "20Gi".`), nil,
			model.ConditionStarted, reasons.ScratchFull, true, []string{`EmptyDir volume "scratch" exceeds the limit "20Gi"`}},
		{"evicted for container ephemeral limit", evicted("Evicted", "Container app exceeded its local ephemeral storage limit \"20Gi\"."), nil,
			model.ConditionStarted, reasons.ScratchFull, true, []string{"local ephemeral storage limit"}},
		{"evicted for memory is not scratch", evicted("Evicted", "The node was low on resource: memory."), nil, "", "", false, nil},
		{"group list not enforceable on the node", evicted("SupplementalGroupsPolicyNotSupported", "SupplementalGroupsPolicy=Strict is not supported in this node"), nil,
			model.ConditionScheduled, reasons.SupplementalGroupsPolicyNotSupported, true, []string{"not supported in this node"}},
		{"healthy", pod(corev1.PodRunning, true, true), nil, "", "", false, nil},
		{"app exits non-zero", pod(corev1.PodRunning, true, false, terminated("app", "Error", 2, "")), nil,
			model.ConditionStarted, reasons.AppCrashed, true, []string{"exit code 2"}},
		{"app crash loop uses last termination", pod(corev1.PodRunning, true, false, crashLoop), nil,
			model.ConditionStarted, reasons.AppCrashed, true, []string{"exit code 1"}},
		{"init container failure quotes trimmed message",
			withInit(pod(corev1.PodPending, true, false), terminated("sample", "Error", 1, "  cp: no such file\n")), nil,
			model.ConditionStarted, reasons.SampleMissing, true, []string{"exit code 1", "cp: no such file"}},
		{"init container exit 0 is skipped",
			withInit(pod(corev1.PodRunning, true, true), terminated("sample", "Completed", 0, "")), nil,
			"", "", false, nil},
		{"volume node affinity conflict", pod(corev1.PodPending, false, false),
			failedSched("0/3 nodes: 1 node(s) had volume node affinity conflict"),
			model.ConditionHomeReady, reasons.HomeBusy, true, []string{"volume node affinity conflict"}},
		{"unbound claim", pod(corev1.PodPending, false, false),
			failedSched("pod has unbound immediate PersistentVolumeClaims"),
			model.ConditionHomeReady, reasons.HomePending, true, []string{"unbound"}},
		{"other scheduling failure is still starting", pod(corev1.PodPending, false, false),
			failedSched("0/3 nodes: 3 node(s) had untolerated taint"),
			model.ConditionScheduled, reasons.Starting, true, []string{"untolerated taint"}},
		{"failed attach", pod(corev1.PodPending, true, false),
			[]corev1.Event{evAt("FailedAttachVolume", "AttachVolume.Attach failed", now)},
			model.ConditionHomeReady, reasons.HomeBusy, true, []string{"AttachVolume.Attach failed"}},
		{"multi-attach", pod(corev1.PodPending, true, false),
			[]corev1.Event{evAt("Multi-Attach", "Volume is already used by pod", now)},
			model.ConditionHomeReady, reasons.HomeBusy, true, []string{"already used by pod"}},
		{"superseded attach error is ignored", pod(corev1.PodPending, true, false),
			[]corev1.Event{
				evAt("FailedAttachVolume", "AttachVolume.Attach failed", now.Add(-time.Minute)),
				evAt("Pulled", "image pulled", now),
			}, "", "", false, nil},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			failing, code, evidence, ok := ClassifyPod(r.pod, r.events, now)
			if ok != r.wantOK || reasons.Code(code) != r.wantCode || failing != r.wantFailing {
				t.Fatalf("failing = %q code = %q ok = %v, want %q %q %v", failing, code, ok, r.wantFailing, r.wantCode, r.wantOK)
			}
			if len(evidence) != len(r.wantEvidence) {
				t.Fatalf("evidence = %q, want %d lines", evidence, len(r.wantEvidence))
			}
			for i, sub := range r.wantEvidence {
				if !strings.Contains(evidence[i], sub) {
					t.Fatalf("evidence[%d] = %q, want substring %q", i, evidence[i], sub)
				}
			}
		})
	}
}

func TestCapMessageKeepsFirstLines(t *testing.T) {
	var lines []string
	for i := 0; i < 25; i++ {
		lines = append(lines, "x")
	}
	if got := strings.Count(CapMessage(lines, 20, 300), "\n"); got != 19 {
		t.Fatalf("newlines = %d, want 19 (20 lines)", got)
	}
}

func TestCapMessageCutsOnRuneBoundary(t *testing.T) {
	got := CapMessage([]string{strings.Repeat("é", 400)}, 20, 300)
	if !utf8.ValidString(got) || len(got) > 300 || !strings.HasSuffix(got, "...") {
		t.Fatalf("len %d valid %v", len(got), utf8.ValidString(got))
	}
}

// The kubelet refuses to create a container whose image runs as root when the
// pod says runAsNonRoot; the message texts are the kubelet's
// (pkg/kubelet/kuberuntime, verifyRunAsNonRoot), as seen on kind.
func TestClassifyPodImageRunsAsRoot(t *testing.T) {
	configErr := func(msg string) *corev1.Pod {
		return pod(corev1.PodPending, true, false, corev1.ContainerStatus{
			Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError", Message: msg}}})
	}
	for _, msg := range []string{
		`container has runAsNonRoot and image will run as root (pod: "alice-web_javo-apps(1f0c2a8e-5d7b-4c4e-9a51-0b6c2d1e3f40)", container: app)`,
		`container has runAsNonRoot and image has non-numeric user (nginx), cannot verify user is non-root (pod: "alice-web_javo-apps(1f0c2a8e-5d7b-4c4e-9a51-0b6c2d1e3f40)", container: app)`,
	} {
		failing, code, ev, ok := ClassifyPod(configErr(msg), nil, time.Now())
		if !ok || failing != model.ConditionStarted || code != string(reasons.ImageRunsAsRoot) {
			t.Fatalf("%q: classified %s/%s/%v", msg, failing, code, ok)
		}
		if len(ev) != 1 || !strings.Contains(ev[0], msg) {
			t.Fatalf("evidence = %q", ev)
		}
		if phaseFor(reasons.Code(code)) != "Failed" {
			t.Fatal("ImageRunsAsRoot must fail the app")
		}
	}
	// Another config error (a missing ConfigMap, say) is not about root.
	if _, code, _, ok := ClassifyPod(configErr(`configmap "x" not found`), nil, time.Now()); ok && code == string(reasons.ImageRunsAsRoot) {
		t.Fatal("unrelated config error read as ImageRunsAsRoot")
	}
}

func TestProbeURLReadsATCPProbe(t *testing.T) {
	p := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(9000)}}},
	}}}}
	if got := probeURL(p); got != "TCP :9000" {
		t.Fatalf("probeURL = %q", got)
	}
}

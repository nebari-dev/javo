package status

import (
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/reasons"
)

var now = time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)

func ws(paused bool) *model.App {
	return &model.App{Owner: "alice", Template: "t", Paused: paused}
}

func probe() *corev1.Probe {
	return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
		Path: "/u/alice/sales-app/", Port: intstr.FromInt32(8501)}}}
}

func pod(phase corev1.PodPhase, scheduled bool, ready bool, cs ...corev1.ContainerStatus) *corev1.Pod {
	p := &corev1.Pod{
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "app", ReadinessProbe: probe()}}},
		Status: corev1.PodStatus{Phase: phase, ContainerStatuses: cs},
	}
	if scheduled {
		p.Spec.NodeName = "node-1"
		p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue})
	}
	if ready {
		p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue})
	}
	return p
}

func ev(reason, msg string) corev1.Event { return corev1.Event{Reason: reason, Message: msg} }

func evAt(reason, msg string, t time.Time) corev1.Event {
	return corev1.Event{Reason: reason, Message: msg, LastTimestamp: metav1.NewTime(t)}
}

func running(since time.Time) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: "app", State: corev1.ContainerState{
		Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(since)}}}
}

func TestDeriveFailures(t *testing.T) {
	bound := &corev1.PersistentVolumeClaim{Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
	pending := &corev1.PersistentVolumeClaim{Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}}
	oom := corev1.ContainerStatus{Name: "app",
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}}
	crash := corev1.ContainerStatus{Name: "app",
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}}}
	pull := func(r string) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: "app", State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: r, Message: "manifest unknown"}}}
	}

	tests := []struct {
		name     string
		o        Observed
		phase    string
		condType string
		reason   string
		evidence string
	}{
		{"no capacity", Observed{Pod: pod(corev1.PodPending, false, false), Claim: bound,
			Events: []corev1.Event{ev("FailedScheduling", "0/12 nodes are available: 12 Insufficient cpu")}},
			"Starting", model.ConditionScheduled, "NoCapacity", "Insufficient cpu"},
		{"home busy attach", Observed{Pod: pod(corev1.PodPending, true, false), Claim: bound,
			Events: []corev1.Event{ev("FailedAttachVolume", "something about the disk")}},
			"Starting", model.ConditionHomeReady, "HomeBusy", "something about the disk"},
		{"home busy multi-attach reason", Observed{Pod: pod(corev1.PodPending, true, false), Claim: bound,
			Events: []corev1.Event{ev("Multi-Attach", "volume in use")}},
			"Starting", model.ConditionHomeReady, "HomeBusy", "volume in use"},
		{"home pending", Observed{Pod: pod(corev1.PodPending, false, false), Claim: pending},
			"Starting", model.ConditionHomeReady, "HomePending", "claim"},
		{"pending claim plus no room is NoCapacity", Observed{Pod: pod(corev1.PodPending, false, false), Claim: pending,
			Events: []corev1.Event{ev("FailedScheduling", "0/12 nodes are available: 12 Insufficient cpu")}},
			"Starting", model.ConditionScheduled, "NoCapacity", "Insufficient cpu"},
		{"volume node affinity conflict", Observed{Pod: pod(corev1.PodPending, false, false), Claim: bound,
			Events: []corev1.Event{ev("FailedScheduling", "1 node(s) had volume node affinity conflict")}},
			"Starting", model.ConditionHomeReady, "HomeBusy", "volume node affinity conflict"},
		{"unbound claim scheduling message", Observed{Pod: pod(corev1.PodPending, false, false), Claim: pending,
			Events: []corev1.Event{ev("FailedScheduling", "0/3 nodes are available: pod has unbound immediate PersistentVolumeClaims.")}},
			"Starting", model.ConditionHomeReady, "HomePending", "unbound immediate PersistentVolumeClaims"},
		{"other scheduling failure", Observed{Pod: pod(corev1.PodPending, false, false), Claim: bound,
			Events: []corev1.Event{ev("FailedScheduling", "node(s) had untolerated taint")}},
			"Starting", model.ConditionScheduled, "Starting", "untolerated taint"},
		{"newest attach event wins", Observed{Pod: pod(corev1.PodPending, true, false), Claim: bound,
			Events: []corev1.Event{evAt("FailedAttachVolume", "old", now.Add(-time.Hour)), evAt("FailedAttachVolume", "new", now)}},
			"Starting", model.ConditionHomeReady, "HomeBusy", "new"},
		{"image pull backoff", Observed{Pod: pod(corev1.PodPending, true, false, pull("ImagePullBackOff")), Claim: bound},
			"Failed", model.ConditionImageReady, "ImagePullFailed", "ImagePullBackOff"},
		{"err image pull", Observed{Pod: pod(corev1.PodPending, true, false, pull("ErrImagePull")), Claim: bound},
			"Failed", model.ConditionImageReady, "ImagePullFailed", "ErrImagePull"},
		{"crash loop", Observed{Pod: pod(corev1.PodRunning, true, false, crash), Claim: bound},
			"Failed", model.ConditionStarted, "AppCrashed", "exit code 1"},
		{"oom", Observed{Pod: pod(corev1.PodRunning, true, false, oom), Claim: bound},
			"Failed", model.ConditionStarted, "OutOfMemory", "OOMKilled"},
		{"not answering", Observed{Pod: pod(corev1.PodRunning, true, false, running(now.Add(-3*time.Minute))), Claim: bound,
			Now: now, ReadyTimeout: 2 * time.Minute},
			"Failed", model.ConditionReady, "AppNotAnswering", ":8501/u/alice/sales-app/"},
		{"template missing", Observed{TemplateMissing: true},
			"Failed", model.ConditionScheduled, "TemplateMissing", ""},
		{"template invalid", Observed{TemplateError: "workspace template \"t\": args[0]: bad field"},
			"Failed", model.ConditionScheduled, "TemplateInvalid", "args[0]: bad field"},
		{"secrets unreadable", Observed{SecretsUnreadable: "the secrets keyset does not open with this key-encryption key"},
			"Failed", model.ConditionScheduled, "SecretsUnreadable", "does not open"},
		{"environment unavailable", Observed{EnvironmentProblem: "environment: the files of sales are missing"},
			"Failed", model.ConditionScheduled, "EnvironmentUnavailable", "files of sales are missing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.o.Now = now
			phase, conds, evidence := Derive(ws(false), tc.o)
			if phase != tc.phase {
				t.Errorf("phase = %q, want %q", phase, tc.phase)
			}
			c := find(conds, tc.condType)
			if c == nil || c.Status != metav1.ConditionFalse || c.Reason != tc.reason {
				t.Errorf("condition %s = %+v, want False/%s", tc.condType, c, tc.reason)
			}
			if !strings.Contains(strings.Join(evidence, "\n"), tc.evidence) {
				t.Errorf("evidence %q lacks %q", evidence, tc.evidence)
			}
		})
	}
}

func TestDeriveStillStartingIsNotFailed(t *testing.T) {
	o := Observed{Pod: pod(corev1.PodRunning, true, false, running(now.Add(-10*time.Second))), Now: now, ReadyTimeout: 2 * time.Minute}
	phase, conds, _ := Derive(ws(false), o)
	if phase != "Starting" {
		t.Errorf("phase = %q, want Starting", phase)
	}
	if c := find(conds, model.ConditionReady); c == nil || c.Status != metav1.ConditionFalse || c.Reason != "Starting" {
		t.Errorf("Ready = %+v", c)
	}
}

func TestDeriveNoPodYet(t *testing.T) {
	phase, _, _ := Derive(ws(false), Observed{Now: now})
	if phase != "Pending" {
		t.Errorf("phase = %q, want Pending", phase)
	}
}

func TestDeriveReady(t *testing.T) {
	phase, conds, _ := Derive(ws(false), Observed{Pod: pod(corev1.PodRunning, true, true, running(now.Add(-time.Hour))), Now: now, ReadyTimeout: time.Minute})
	if phase != "Running" {
		t.Errorf("phase = %q", phase)
	}
	if len(conds) != 5 {
		t.Fatalf("got %d conditions", len(conds))
	}
	for _, c := range conds {
		if c.Status != metav1.ConditionTrue {
			t.Errorf("%s = %s, want True", c.Type, c.Status)
		}
	}
}

func TestDerivePaused(t *testing.T) {
	phase, conds, _ := Derive(ws(true), Observed{Pod: pod(corev1.PodRunning, true, true), Now: now})
	if phase != "Stopped" {
		t.Errorf("phase = %q", phase)
	}
	if c := find(conds, model.ConditionReady); c == nil || c.Status != metav1.ConditionFalse || c.Reason != "Stopped" {
		t.Errorf("Ready = %+v", c)
	}
}

func find(cs []metav1.Condition, t string) *metav1.Condition {
	for i := range cs {
		if cs[i].Type == t {
			return &cs[i]
		}
	}
	return nil
}

func TestDeriveSupersededAttachFailureIsNotHomeBusy(t *testing.T) {
	o := Observed{Pod: pod(corev1.PodPending, true, false), Now: now, Claim: &corev1.PersistentVolumeClaim{Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}},
		Events: []corev1.Event{
			evAt("FailedAttachVolume", "Multi-Attach error", now.Add(-5*time.Minute)),
			evAt("Pulling", "Pulling image", now.Add(-time.Minute))}}
	_, conds, _ := Derive(ws(false), o)
	if c := find(conds, model.ConditionHomeReady); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("HomeReady = %+v, want True", c)
	}
}

func TestDeriveSupersededSchedulingFailureIgnoredOnceScheduled(t *testing.T) {
	o := Observed{Pod: pod(corev1.PodPending, true, false), Now: now,
		Events: []corev1.Event{ev("FailedScheduling", "Insufficient cpu")}}
	_, conds, _ := Derive(ws(false), o)
	if c := find(conds, model.ConditionScheduled); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("Scheduled = %+v, want True", c)
	}
}

func TestDerivePausedBeatsTemplateProblems(t *testing.T) {
	phase, _, _ := Derive(ws(true), Observed{TemplateMissing: true, Now: now})
	if phase != "Stopped" {
		t.Fatalf("phase = %q, want Stopped", phase)
	}
}

// A scheduled pod still pulling its image must show the download step in
// progress, not done; once the kubelet reports Pulled the start step is next.
func TestDeriveImageStepWaitsForPulled(t *testing.T) {
	creating := corev1.ContainerStatus{Name: "app", State: corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}
	cases := []struct {
		name    string
		events  []corev1.Event
		current string
	}{
		{"pulling", []corev1.Event{ev("Pulling", `Pulling image "x"`)}, model.ConditionImageReady},
		{"no event yet", nil, model.ConditionImageReady},
		{"pulled", []corev1.Event{ev("Pulling", ""), ev("Pulled", `Successfully pulled image "x"`)}, model.ConditionStarted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, conds, _ := Derive(ws(false), Observed{Pod: pod(corev1.PodPending, true, false, creating), Events: tc.events, Now: now})
			for _, c := range conds {
				if c.Status == metav1.ConditionFalse {
					if c.Type != tc.current || c.Reason != "Starting" {
						t.Fatalf("current step %s/%s, want %s/Starting", c.Type, c.Reason, tc.current)
					}
					return
				}
			}
			t.Fatal("no step in progress")
		})
	}
}

func TestDeriveSampleCopyFailureBlamesTemplate(t *testing.T) {
	p := pod(corev1.PodPending, true, false)
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name: "sample-copy",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "Error", ExitCode: 1, Message: "cp: cannot stat '/opt/javo/samples/x'"}},
	}}
	phase, conds, evidence := Derive(ws(false), Observed{Pod: p, Now: now})
	if phase != "Failed" {
		t.Fatalf("phase = %s", phase)
	}
	var reason string
	for _, c := range conds {
		if c.Status == metav1.ConditionFalse {
			reason = c.Reason
		}
	}
	if reason != "SampleMissing" {
		t.Fatalf("reason = %q, conditions %+v", reason, conds)
	}
	if len(evidence) == 0 || !strings.Contains(strings.Join(evidence, "\n"), "cp: cannot stat '/opt/javo/samples/x'") {
		t.Fatalf("evidence = %v", evidence)
	}
}

func failingReason(conds []metav1.Condition) string {
	for _, c := range conds {
		if c.Status == metav1.ConditionFalse {
			return c.Reason
		}
	}
	return ""
}

func TestDeriveInitContainerFailures(t *testing.T) {
	crashLoop := func(name string) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: name,
			State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, Message: "cp: cannot stat 'x'"}}}
	}
	pull := corev1.ContainerStatus{Name: "sample-dir",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "not found"}}}
	podInit := corev1.ContainerStatus{Name: "app",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}}
	cases := []struct {
		name         string
		init         corev1.ContainerStatus
		reason, want string
	}{
		{"sample-copy crash loop", crashLoop("sample-copy"), "SampleMissing", "cannot stat"},
		{"sample-dir crash loop", crashLoop("sample-dir"), "SampleMissing", "sample-dir"},
		{"init image pull", pull, "ImagePullFailed", "ImagePullBackOff"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := pod(corev1.PodPending, true, false, podInit)
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{c.init}
			phase, conds, evidence := Derive(ws(false), Observed{Pod: p, Now: now})
			if phase != "Failed" || failingReason(conds) != c.reason {
				t.Fatalf("phase %s reason %q", phase, failingReason(conds))
			}
			if !strings.Contains(strings.Join(evidence, "\n"), c.want) {
				t.Fatalf("evidence = %v", evidence)
			}
		})
	}
}

func TestDeriveSampleFailureWithoutMessageHasNoEmptyEvidence(t *testing.T) {
	p := pod(corev1.PodPending, true, false)
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "sample-copy",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}}}}
	_, _, evidence := Derive(ws(false), Observed{Pod: p, Now: now})
	for _, e := range evidence {
		if strings.TrimSpace(e) == "" {
			t.Fatalf("empty evidence entry: %q", evidence)
		}
	}
}

func TestIdentityPendingBlocksHomeBeforeAnyPod(t *testing.T) {
	ws := &model.App{}
	phase, conds, _ := Derive(ws, Observed{Now: time.Now(), IdentityPending: true})
	if phase != "Pending" {
		t.Fatalf("phase = %q", phase)
	}
	for _, c := range conds {
		if c.Type == model.ConditionHomeReady && (c.Status != metav1.ConditionFalse || c.Reason != string(reasons.IdentityPending)) {
			t.Fatalf("HomeReady = %+v", c)
		}
	}
}

func TestDeriveRefusesLoginlessAppWithoutEnforcedNetworkPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		login   string
		policy  netprobe.State
		pod     *corev1.Pod
		refused bool
	}{
		{"unknown policy", "Disabled", netprobe.Unknown, nil, true},
		{"policy never probed", "Disabled", "", nil, true},
		{"not enforced", "Disabled", netprobe.NotEnforced, nil, true},
		{"enforced", "Disabled", netprobe.Enforced, nil, false},
		{"app with its own login", "Enabled", netprobe.NotEnforced, nil, false},
		{"already running keeps running", "Disabled", netprobe.Unknown, pod(corev1.PodRunning, true, true), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			phase, conds, _ := Derive(ws(false), Observed{Now: now, AppLogin: tc.login, NetworkPolicy: tc.policy, Pod: tc.pod})
			got := ""
			for _, c := range conds {
				if c.Type == model.ConditionScheduled && c.Status == metav1.ConditionFalse {
					got = c.Reason
				}
			}
			if tc.refused {
				if phase != "Failed" || got != string(reasons.NetworkPolicyNotEnforced) {
					t.Fatalf("phase %q, Scheduled reason %q; want Failed / NetworkPolicyNotEnforced", phase, got)
				}
				return
			}
			if phase == "Failed" || got == string(reasons.NetworkPolicyNotEnforced) {
				t.Fatalf("phase %q reason %q; the app should be allowed", phase, got)
			}
		})
	}
}

// bigPod asks for 8 CPU and 32Gi, like the shipped Large size.
func bigPod() *corev1.Pod {
	p := pod(corev1.PodPending, false, false)
	p.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("32Gi")}
	return p
}

var singleNode = capacity.Cluster{{CPU: resource.MustParse("8"), Memory: resource.MustParse("16365140Ki")}}

// A GPU pod on a cluster whose machines have no GPU (or not the labels the
// pod selects) can never be placed either, however much CPU they have.
func TestDeriveGPUPodWithoutGPUMachines(t *testing.T) {
	p := pod(corev1.PodPending, false, false)
	p.Spec.NodeSelector = map[string]string{"pool": "gpu"}
	p.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi"), "nvidia.com/gpu": resource.MustParse("1")}
	insufficient := []corev1.Event{ev("FailedScheduling", "0/1 nodes are available: 1 Insufficient nvidia.com/gpu.")}
	cpuOnly := capacity.Cluster{{CPU: resource.MustParse("64"), Memory: resource.MustParse("256Gi")}}
	phase, conds, evidence := Derive(ws(false), Observed{Pod: p, Events: insufficient, Machines: cpuOnly, Now: now})
	if c := find(conds, model.ConditionScheduled); phase != "Failed" || c == nil || c.Reason != string(reasons.SizeTooBig) {
		t.Fatalf("phase %q, Scheduled %+v", phase, c)
	}
	want := "needs 4 CPU, 16 GB and 1 nvidia.com/gpu on a machine labelled pool=gpu; no machine has all of that"
	if len(evidence) == 0 || evidence[0] != want {
		t.Fatalf("evidence %q, want %q", evidence, want)
	}

	gpuPool := capacity.Cluster{{CPU: resource.MustParse("8"), Memory: resource.MustParse("32Gi"),
		Devices: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}, Labels: map[string]string{"pool": "gpu"}}}
	phase, conds, _ = Derive(ws(false), Observed{Pod: p, Events: insufficient, Machines: gpuPool, Now: now})
	if c := find(conds, model.ConditionScheduled); phase != "Starting" || c.Reason != string(reasons.NoCapacity) {
		t.Fatalf("GPU machine busy: phase %q, reason %q", phase, c.Reason)
	}
}

// A pod bigger than every machine can never be placed: that is a failure the
// user fixes by picking a smaller size, not a wait for room.
func TestDeriveSizeTooBigForEveryMachine(t *testing.T) {
	insufficient := []corev1.Event{ev("FailedScheduling", "0/1 nodes are available: 1 Insufficient cpu, 1 Insufficient memory.")}
	phase, conds, evidence := Derive(ws(false), Observed{Pod: bigPod(), Events: insufficient, Machines: singleNode, Now: now})
	c := find(conds, model.ConditionScheduled)
	if phase != "Failed" || c == nil || c.Reason != string(reasons.SizeTooBig) {
		t.Fatalf("phase %q, Scheduled %+v", phase, c)
	}
	want := "needs 8 CPU and 32 GB; the largest machine has 8 CPU and 15.6 GB"
	if len(evidence) == 0 || evidence[0] != want {
		t.Fatalf("evidence %q, want %q", evidence, want)
	}

	// The same pod on a cluster with a big enough machine is only waiting for room.
	big := append(capacity.Cluster{{CPU: resource.MustParse("16"), Memory: resource.MustParse("64Gi")}}, singleNode...)
	phase, conds, _ = Derive(ws(false), Observed{Pod: bigPod(), Events: insufficient, Machines: big, Now: now})
	if c := find(conds, model.ConditionScheduled); phase != "Starting" || c.Reason != string(reasons.NoCapacity) {
		t.Fatalf("room elsewhere: phase %q, reason %q", phase, c.Reason)
	}

	// A size the admin lets wait for an autoscaler keeps waiting.
	phase, conds, _ = Derive(ws(false), Observed{Pod: bigPod(), Events: insufficient, Machines: singleNode, SizeMayWait: true, Now: now})
	if c := find(conds, model.ConditionScheduled); phase != "Starting" || c.Reason != string(reasons.NoCapacity) {
		t.Fatalf("may wait: phase %q, reason %q", phase, c.Reason)
	}

	// Unknown machines (none read) judge nothing.
	phase, _, _ = Derive(ws(false), Observed{Pod: bigPod(), Events: insufficient, Now: now})
	if phase != "Starting" {
		t.Fatalf("unknown cluster: phase %q", phase)
	}
}

// An app the idle check stopped says so, with its timeout as evidence.
func TestDeriveStoppedForBeingIdle(t *testing.T) {
	app := ws(true)
	app.StoppedIdleAfter = 60
	phase, conds, _ := Derive(app, Observed{Now: now})
	if phase != "Stopped" {
		t.Errorf("phase = %q", phase)
	}
	c := find(conds, model.ConditionReady)
	if c == nil || c.Reason != string(reasons.StoppedIdle) || !strings.Contains(c.Message, "Stopped after 60 minutes without use") {
		t.Errorf("Ready = %+v", c)
	}
}

// A home that is not ready blocks the app before any pod exists, with the
// home's own reason and what it found.
func TestHomeNotReadyBlocksHomeBeforeAnyPod(t *testing.T) {
	for code, wantPhase := range map[reasons.Code]string{
		reasons.HomePreparing:   "Pending",
		reasons.HomePending:     "Pending",
		reasons.HomeTooLarge:    "Failed",
		reasons.TeamStorageFull: "Failed",
		reasons.HomeMountFailed: "Failed",
	} {
		phase, conds, ev := Derive(&model.App{}, Observed{Now: now, HomeReason: code, HomeEvidence: "quota used"})
		if phase != wantPhase {
			t.Errorf("%s: phase = %q, want %q", code, phase, wantPhase)
		}
		for _, c := range conds {
			if c.Type == model.ConditionHomeReady && (c.Status != metav1.ConditionFalse || c.Reason != string(code)) {
				t.Errorf("%s: HomeReady = %+v", code, c)
			}
		}
		if len(ev) != 1 || ev[0] != "quota used" {
			t.Errorf("%s: evidence = %v", code, ev)
		}
	}
}

// A Home that is being copied to another mode holds its apps back: Stopped
// (the platform's doing, nothing failed) with HomeMigrating on HomeReady.
func TestHomeMigratingStopsTheAppWithoutFailingIt(t *testing.T) {
	phase, conds, _ := Derive(&model.App{}, Observed{Now: now, HomeMigrating: true})
	if phase != "Stopped" {
		t.Fatalf("phase = %q, want Stopped", phase)
	}
	for _, c := range conds {
		if c.Type == model.ConditionHomeReady && (c.Status != metav1.ConditionFalse || c.Reason != string(reasons.HomeMigrating)) {
			t.Errorf("HomeReady = %+v", c)
		}
	}
}

func TestScratchFullIsAFailureWithTheKubeletLineAsEvidence(t *testing.T) {
	p := pod(corev1.PodFailed, true, false)
	p.Status.Reason = "Evicted"
	p.Status.Message = "Pod ephemeral local storage usage exceeds the total limit of containers 20Gi."
	phase, conds, ev := Derive(&model.App{}, Observed{Now: now, Pod: p})
	if phase != "Failed" || len(ev) != 1 || !strings.Contains(ev[0], "exceeds the total limit of containers 20Gi") {
		t.Fatalf("phase %q evidence %q", phase, ev)
	}
	for _, c := range conds {
		if c.Type == model.ConditionStarted && (c.Status != metav1.ConditionFalse || c.Reason != string(reasons.ScratchFull)) {
			t.Errorf("Started = %+v", c)
		}
	}
}

// A restored app waiting for an admin says so on every step, whether it was
// running or stopped, and is never reported as starting.
func TestDeriveHeldAfterRestore(t *testing.T) {
	for _, paused := range []bool{false, true} {
		app := ws(paused)
		app.RestoreHold = "javo-20261101T020000Z"
		phase, conds, _ := Derive(app, Observed{Now: now})
		if phase != "Pending" {
			t.Errorf("paused %v: phase = %q", paused, phase)
		}
		for _, c := range conds {
			if c.Reason != string(reasons.RestoreHeld) || c.Status != metav1.ConditionFalse {
				t.Errorf("paused %v: %s = %s %s", paused, c.Type, c.Status, c.Reason)
			}
		}
	}
}

// An app a platform admin stopped says so, so its owner knows why.
func TestDeriveStoppedByAnAdmin(t *testing.T) {
	app := ws(true)
	app.StoppedByAdmin = true
	phase, conds, _ := Derive(app, Observed{Now: now})
	if phase != "Stopped" {
		t.Errorf("phase = %q", phase)
	}
	if c := find(conds, model.ConditionReady); c == nil || c.Reason != string(reasons.StoppedByAdmin) {
		t.Errorf("Ready = %+v", c)
	}
}

func initFailed(name, message string) *corev1.Pod {
	p := pod(corev1.PodPending, true, false)
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name:  name,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, Message: message}},
	}}
	return p
}

func TestDeriveSourceStepFailuresSayWhatToFix(t *testing.T) {
	gitSync := func(errText string) string {
		return `{"msg":"starting up","flags":["--password-file=/etc/javo/git/token"]}` + "\n" +
			`{"msg":"too many failures, aborting","error":"` + errText + `"}`
	}
	cases := []struct {
		name, container, message string
		reason, evidence         string
	}{
		{"missing ref", "clone", gitSync(`exit status 128: fatal: couldn't find remote ref nope`), "RefNotFound", "couldn't find remote ref nope"},
		{"missing repo", "clone", gitSync(`remote: Repository not found.`), "RepoNotFound", "Repository not found"},
		{"bad token", "clone", gitSync(`remote: Invalid username or token.`), "AuthFailed", "Invalid username or token"},
		{"unreachable", "clone", gitSync(`Could not resolve host: git.example.org`), "CloneFailed", "Could not resolve host"},
		{"file", "source-check", "file-missing: app.py", "FileNotInRepo", "file-missing: app.py"},
		{"build", "build", "npm ERR! missing script: build", "BuildFailed", "missing script"},
		{"output", "site-copy", "cp: cannot stat 'dist/.': No such file or directory", "BuildOutputMissing", "No such file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			phase, conds, evidence := Derive(ws(false), Observed{Pod: initFailed(c.container, c.message), Now: now})
			if phase != "Failed" || failingReason(conds) != c.reason {
				t.Fatalf("phase %s reason %q", phase, failingReason(conds))
			}
			if !strings.Contains(strings.Join(evidence, "\n"), c.evidence) {
				t.Fatalf("evidence = %v", evidence)
			}
		})
	}
}

func TestDeriveCloneEvidenceIsTheGitSyncErrorOnly(t *testing.T) {
	log := `{"msg":"starting up","flags":["--password-file=/etc/javo/git/token"]}` + "\n" +
		`{"msg":"note","detail":"ghp_testtoken123"}` + "\n" +
		`{"msg":"too many failures, aborting","error":"remote: Invalid username or token."}`
	_, _, evidence := Derive(ws(false), Observed{Pod: initFailed("clone", log), Now: now})
	all := strings.Join(evidence, "\n")
	if strings.Contains(all, "ghp_testtoken123") || strings.Contains(all, "password-file") {
		t.Fatalf("evidence carries more than the error: %v", evidence)
	}
}

// pixi's own error is the evidence when the environment does not install.
func TestDeriveAFailedEnvironmentInstallSaysSo(t *testing.T) {
	msg := "Error:   x the platform linux-64 is not part of the lock file"
	_, conds, evidence := Derive(ws(false), Observed{Pod: initFailed("env-install", msg), Now: now})
	if failingReason(conds) != "EnvironmentInstallFailed" || !strings.Contains(strings.Join(evidence, "\n"), "linux-64 is not part") {
		t.Fatalf("reason %q, evidence %v", failingReason(conds), evidence)
	}
}

func TestDeriveSampleCopyStillBlamesTheTemplateNextToSourceSteps(t *testing.T) {
	_, conds, _ := Derive(ws(false), Observed{Pod: initFailed("sample-copy", "cp: cannot stat 'x'"), Now: now})
	if failingReason(conds) != "SampleMissing" {
		t.Fatalf("reason = %q", failingReason(conds))
	}
}

// sitePod is the pod of a static site from Git: it clones, checks, builds and
// copies the site in init containers, with statuses as given (the others not
// started yet).
func sitePod(phase corev1.PodPhase, init ...corev1.ContainerStatus) *corev1.Pod {
	p := pod(phase, true, false)
	for _, n := range []string{"clone", "source-check", "build", "site-copy", "site-config"} {
		p.Spec.InitContainers = append(p.Spec.InitContainers, corev1.Container{Name: n})
	}
	p.Status.InitContainerStatuses = init
	return p
}

func done(name string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}}
}

func types(conds []metav1.Condition) string {
	var out []string
	for _, c := range conds {
		out = append(out, c.Type+"="+string(c.Status))
	}
	return strings.Join(out, ",")
}

// An app that builds has a Build step between the image and the start: its
// clone, build and copy run there, and a failed build fails it, with the
// build's error lines as evidence.
func TestDeriveBuildStep(t *testing.T) {
	building := corev1.ContainerStatus{Name: "build", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	failed := corev1.ContainerStatus{Name: "build", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		Reason: "Error", ExitCode: 1,
		Message: "npm error code EUSAGE\nnpm error\nnpm error The `npm ci` command can only install with an existing package-lock.json\nnpm error A complete log of this run can be found in: /tmp/npm/_logs/x.log\n",
	}}}
	pulled := []corev1.Event{ev("Pulled", "image pulled")}
	cases := []struct {
		name   string
		o      Observed
		phase  string
		want   string
		reason string
	}{
		{"no pod yet", Observed{Builds: true}, "Pending",
			"Scheduled=False,HomeReady=Unknown,ImageReady=Unknown,Built=Unknown,Started=Unknown,Ready=Unknown", "Starting"},
		{"building", Observed{Builds: true, Pod: sitePod(corev1.PodPending, done("clone"), done("source-check"), building), Events: pulled}, "Starting",
			"Scheduled=True,HomeReady=True,ImageReady=True,Built=False,Started=Unknown,Ready=Unknown", "Starting"},
		{"clone fails", Observed{Builds: true, Pod: sitePod(corev1.PodPending, corev1.ContainerStatus{Name: "clone", State: failed.State}), Events: pulled}, "Failed",
			"Scheduled=True,HomeReady=True,ImageReady=True,Built=False,Started=Unknown,Ready=Unknown", "CloneFailed"},
		{"build fails", Observed{Builds: true, Pod: sitePod(corev1.PodPending, done("clone"), done("source-check"), failed), Events: pulled}, "Failed",
			"Scheduled=True,HomeReady=True,ImageReady=True,Built=False,Started=Unknown,Ready=Unknown", "BuildFailed"},
		{"built, starting", Observed{Builds: true, Pod: sitePod(corev1.PodPending, done("clone"), done("source-check"), done("build"), done("site-copy"), done("site-config")), Events: pulled}, "Starting",
			"Scheduled=True,HomeReady=True,ImageReady=True,Built=True,Started=False,Ready=Unknown", "Starting"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.o.Now = now
			phase, conds, evidence := Derive(ws(false), c.o)
			if phase != c.phase || types(conds) != c.want {
				t.Fatalf("phase %s, conditions %s; want %s, %s", phase, types(conds), c.phase, c.want)
			}
			if b := find(conds, model.ConditionBuilt); b.Status == metav1.ConditionFalse && b.Reason != c.reason {
				t.Fatalf("Built reason = %s, want %s", b.Reason, c.reason)
			}
			if c.reason == "BuildFailed" && (len(evidence) < 3 || !strings.Contains(evidence[1], "code EUSAGE") || !strings.Contains(evidence[2], "package-lock.json")) {
				t.Fatalf("evidence = %q", evidence)
			}
		})
	}
	// An app without a build keeps its five steps.
	_, conds, _ := Derive(ws(false), Observed{Pod: initFailed("clone", "fatal: nope"), Now: now})
	if find(conds, model.ConditionBuilt) != nil || failingReason(conds) != "CloneFailed" || find(conds, model.ConditionStarted).Status != metav1.ConditionFalse {
		t.Fatalf("conditions %s", types(conds))
	}
}

// The kubelet logs FailedCreatePodSandBox each time the container runtime
// refuses to set up the pod (the message text is containerd's). One event
// can be a hiccup the kubelet retries past; the same event repeated means
// the app never starts, and it must fail with the event as evidence.
func TestDerivePodSandboxFailure(t *testing.T) {
	const msg = `Failed to create pod sandbox: rpc error: code = Unknown desc = failed to create containerd container: user group "100" is specified without user`
	creating := corev1.ContainerStatus{Name: "app", State: corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}
	sandbox := func(count int32, at time.Time) corev1.Event {
		e := evAt("FailedCreatePodSandBox", msg, at)
		e.Count = count
		return e
	}
	series := sandbox(0, now)
	series.Series = &corev1.EventSeries{Count: 4}
	cases := []struct {
		name   string
		events []corev1.Event
		phase  string
		step   string
		reason string
	}{
		{"first refusal is still starting", []corev1.Event{sandbox(1, now)}, "Starting", model.ConditionImageReady, "Starting"},
		{"repeated refusals fail", []corev1.Event{sandbox(3, now)}, "Failed", model.ConditionStarted, "SandboxFailed"},
		{"repeated as an event series fail", []corev1.Event{series}, "Failed", model.ConditionStarted, "SandboxFailed"},
		{"superseded by an image pull", []corev1.Event{sandbox(3, now.Add(-time.Minute)), evAt("Pulling", `Pulling image "x"`, now)},
			"Starting", model.ConditionImageReady, "Starting"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			phase, conds, evidence := Derive(ws(false), Observed{Pod: pod(corev1.PodPending, true, false, creating), Events: tc.events, Now: now})
			if phase != tc.phase {
				t.Fatalf("phase = %q, want %q", phase, tc.phase)
			}
			c := find(conds, tc.step)
			if c == nil || c.Status != metav1.ConditionFalse || c.Reason != tc.reason {
				t.Fatalf("step %s = %+v, want False/%s", tc.step, c, tc.reason)
			}
			if tc.reason == "SandboxFailed" && !strings.Contains(strings.Join(evidence, "\n"), `user group "100" is specified without user`) {
				t.Fatalf("evidence %q lacks the runtime's message", evidence)
			}
		})
	}
}

// envPod is the pod of an app whose environment is installed before it
// starts: its code is cloned, the platform files and the sample copied, then
// the tools and the install run, with statuses as given (the others not
// started yet).
func envPod(phase corev1.PodPhase, init ...corev1.ContainerStatus) *corev1.Pod {
	p := pod(phase, true, false)
	for _, n := range []string{"clone", "source-check", "javo-files", "pod-user", "sample-dir", "sample-copy", "env-tools", "env-install"} {
		p.Spec.InitContainers = append(p.Spec.InitContainers, corev1.Container{Name: n})
	}
	p.Status.InitContainerStatuses = init
	return p
}

// An app that installs an environment has a Set up the environment step
// (EnvReady) after the image: the clone, the pod files and the install run
// there, and a failed install fails it.
func TestDeriveEnvironmentStep(t *testing.T) {
	runningInit := func(name string) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	}
	failed := corev1.ContainerStatus{Name: "env-install", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		Reason: "Error", ExitCode: 1, Message: "Error: the platform linux-64 is not part of the lock file"}}}
	pulled := []corev1.Event{ev("Pulled", "image pulled")}
	before := []corev1.ContainerStatus{done("clone"), done("source-check"), done("javo-files"), done("pod-user"), done("sample-dir"), done("sample-copy")}
	cases := []struct {
		name, phase, want, reason string
		o                         Observed
	}{
		{"no pod yet", "Pending", "Scheduled=False,HomeReady=Unknown,ImageReady=Unknown,EnvReady=Unknown,Started=Unknown,Ready=Unknown", "Starting",
			Observed{InstallsEnv: true}},
		{"cloning", "Starting", "Scheduled=True,HomeReady=True,ImageReady=True,EnvReady=False,Started=Unknown,Ready=Unknown", "Starting",
			Observed{Pod: envPod(corev1.PodPending, runningInit("clone")), Events: pulled}},
		{"installing", "Starting", "Scheduled=True,HomeReady=True,ImageReady=True,EnvReady=False,Started=Unknown,Ready=Unknown", "Starting",
			Observed{Pod: envPod(corev1.PodPending, append(slices.Clone(before), done("env-tools"), runningInit("env-install"))...), Events: pulled}},
		{"install fails", "Failed", "Scheduled=True,HomeReady=True,ImageReady=True,EnvReady=False,Started=Unknown,Ready=Unknown", "EnvironmentInstallFailed",
			Observed{Pod: envPod(corev1.PodPending, append(slices.Clone(before), done("env-tools"), failed)...), Events: pulled}},
		{"installed, starting", "Starting", "Scheduled=True,HomeReady=True,ImageReady=True,EnvReady=True,Started=False,Ready=Unknown", "Starting",
			Observed{Pod: envPod(corev1.PodPending, append(slices.Clone(before), done("env-tools"), done("env-install"))...), Events: pulled}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.o.Now = now
			phase, conds, _ := Derive(ws(false), c.o)
			if phase != c.phase || types(conds) != c.want {
				t.Fatalf("phase %s, conditions %s; want %s, %s", phase, types(conds), c.phase, c.want)
			}
			if e := find(conds, model.ConditionEnvReady); e.Status == metav1.ConditionFalse && e.Reason != c.reason {
				t.Fatalf("EnvReady reason = %s, want %s", e.Reason, c.reason)
			}
		})
	}
}

// A site that builds and installs an environment builds first, as its pod does.
func TestDeriveBuildComesBeforeTheEnvironment(t *testing.T) {
	p := sitePod(corev1.PodPending, done("clone"), done("source-check"), done("build"), done("site-copy"), done("site-config"),
		corev1.ContainerStatus{Name: "env-install", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}})
	p.Spec.InitContainers = append(p.Spec.InitContainers, corev1.Container{Name: "env-tools"}, corev1.Container{Name: "env-install"})
	p.Status.InitContainerStatuses = slices.Insert(p.Status.InitContainerStatuses, 5, done("env-tools"))
	_, conds, _ := Derive(ws(false), Observed{Builds: true, Pod: p, Events: []corev1.Event{ev("Pulled", "x")}, Now: now})
	if want := "Scheduled=True,HomeReady=True,ImageReady=True,Built=True,EnvReady=False,Started=Unknown,Ready=Unknown"; types(conds) != want {
		t.Fatalf("conditions %s, want %s", types(conds), want)
	}
}

// An environment without the program the app starts fails with its own
// reason, and the step's plain sentence is the evidence.
func TestDeriveAnEnvironmentWithoutTheProgramSaysWhich(t *testing.T) {
	msg := "The environment smoke-pushed has no streamlit. Add streamlit to it in Nebi, or pick another environment."
	p := initFailed("env-install", msg)
	p.Status.InitContainerStatuses[0].State.Terminated.ExitCode = 78
	phase, conds, evidence := Derive(ws(false), Observed{Pod: p, Now: now})
	if phase != "Failed" || failingReason(conds) != "EnvironmentLacksProgram" || len(evidence) == 0 || evidence[len(evidence)-1] != msg {
		t.Fatalf("phase %s reason %q evidence %v", phase, failingReason(conds), evidence)
	}
}

// A note from an install step that succeeded (an environment beside the
// template's that has no kernel) is shown with the Started step.
func TestDeriveShowsTheEnvironmentNoteWithTheStartedStep(t *testing.T) {
	note := "The environment smoke-pushed has no Jupyter kernel, so JupyterLab does not offer it. Add ipykernel (or another kernel) to it, then restart the app."
	p := pod(corev1.PodRunning, true, true, running(now.Add(-time.Minute)))
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "env-install",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0, Message: note}}}}
	_, conds, _ := Derive(ws(false), Observed{Pod: p, Now: now})
	for _, c := range conds {
		if c.Type == model.ConditionStarted {
			if !strings.Contains(c.Message, note) {
				t.Fatalf("Started message %q", c.Message)
			}
			return
		}
	}
	t.Fatal("no Started condition")
}

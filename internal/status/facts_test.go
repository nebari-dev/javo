package status

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/nebari-dev/javo/internal/model"
)

// t0 is when the pod was placed on a node.
var t0 = time.Date(2026, 10, 31, 10, 7, 12, 0, time.UTC)

// readyPod is a pod that went through every step: placed at t0, volumes
// mounted 2s later, container started at t0+3s, ready at t0+6s.
func readyPod() *corev1.Pod {
	at := func(s int) metav1.Time { return metav1.NewTime(t0.Add(time.Duration(s) * time.Second)) }
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: at(-1)},
		Spec: corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: "app", ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(8080)}}}}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: at(0)},
				{Type: corev1.PodReadyToStartContainers, Status: corev1.ConditionTrue, LastTransitionTime: at(2)},
				{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: at(6)},
			},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app", Ready: true, RestartCount: 0,
				Image:   "ghcr.io/example/bokeh:3.9.2",
				ImageID: "ghcr.io/example/bokeh@sha256:4b1c",
				State:   corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: at(3)}},
			}},
		},
	}
}

func homeClaim() *corev1.PersistentVolumeClaim {
	sc := "standard"
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "home-alice-1a2b"},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: &sc},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound,
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}},
	}
}

func pulledEvent(msg string) corev1.Event {
	return corev1.Event{Reason: "Pulled", Message: msg,
		InvolvedObject: corev1.ObjectReference{FieldPath: "spec.containers{app}"}, LastTimestamp: metav1.NewTime(t0.Add(time.Second))}
}

func lines(conds []metav1.Condition, t string) []string {
	c := find(conds, t)
	if c == nil {
		return nil
	}
	return strings.Split(c.Message, "\n")
}

func wantLines(t *testing.T, conds []metav1.Condition, typ string, want ...string) {
	t.Helper()
	got := lines(conds, typ)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s message:\n got %q\nwant %q", typ, got, want)
	}
}

// Each done step records what the API reported about it, so the facts
// outlive the events behind them.
func TestDoneStepsRecordWhatHappened(t *testing.T) {
	o := Observed{Pod: readyPod(), Claim: homeClaim(), Now: t0.Add(time.Hour),
		Events: []corev1.Event{pulledEvent(`Container image "ghcr.io/example/bokeh:3.9.2" already present on machine`)}}
	phase, conds, _ := Derive(ws(false), o)
	if phase != "Running" {
		t.Fatalf("phase = %q", phase)
	}
	wantLines(t, conds, model.ConditionScheduled, "node node-1", "placed at 2026-10-31 10:07:12 UTC")
	wantLines(t, conds, model.ConditionHomeReady, "claim home-alice-1a2b, 10Gi, storage class standard", "mounted within 2s of placement")
	wantLines(t, conds, model.ConditionImageReady, "image ghcr.io/example/bokeh:3.9.2", "digest sha256:4b1c", "already on the node, not downloaded")
	wantLines(t, conds, model.ConditionStarted, "container app started at 2026-10-31 10:07:15 UTC", "restarts 0")
	wantLines(t, conds, model.ConditionReady, "checked TCP :8080", "answered 3s after the container started")
}

func TestDownloadedImageRecordsThePullTime(t *testing.T) {
	o := Observed{Pod: readyPod(), Claim: homeClaim(), Now: t0,
		Events: []corev1.Event{pulledEvent(`Successfully pulled image "ghcr.io/example/bokeh:3.9.2" in 27.1s (27.1s including waiting). Image size: 412 MB.`)}}
	_, conds, _ := Derive(ws(false), o)
	if got := lines(conds, model.ConditionImageReady); got[len(got)-1] != "downloaded in 27.1s" {
		t.Errorf("ImageReady = %q", got)
	}
}

// What the API did not report is left out: no filler line stands in for it.
func TestMissingFactsAreLeftOut(t *testing.T) {
	p := readyPod()
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	p.Status.ContainerStatuses[0].ImageID = ""
	claim := homeClaim()
	claim.Spec.StorageClassName = nil
	_, conds, _ := Derive(ws(false), Observed{Pod: p, Claim: claim, Now: t0})
	wantLines(t, conds, model.ConditionScheduled, "node node-1")
	wantLines(t, conds, model.ConditionHomeReady, "claim home-alice-1a2b, 10Gi")
	wantLines(t, conds, model.ConditionImageReady, "image ghcr.io/example/bokeh:3.9.2")
	wantLines(t, conds, model.ConditionReady, "checked TCP :8080")
}

// Events last about an hour. Once the Pulled event is gone, the download
// line recorded while this pod ran is kept.
func TestDownloadFactOutlivesItsEvent(t *testing.T) {
	p := readyPod()
	w := ws(false)
	w.Status.Conditions = []metav1.Condition{{Type: model.ConditionImageReady, Status: metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(t0.Add(time.Second)),
		Message:            "image ghcr.io/example/bokeh:3.9.2\ndigest sha256:4b1c\ndownloaded in 27.1s"}}
	_, conds, _ := Derive(w, Observed{Pod: p, Claim: homeClaim(), Now: t0.Add(2 * time.Hour)})
	wantLines(t, conds, model.ConditionImageReady, "image ghcr.io/example/bokeh:3.9.2", "digest sha256:4b1c", "downloaded in 27.1s")

	// A line recorded for an earlier pod does not describe this one.
	w.Status.Conditions[0].LastTransitionTime = metav1.NewTime(t0.Add(-time.Hour))
	_, conds, _ = Derive(w, Observed{Pod: p, Claim: homeClaim(), Now: t0.Add(2 * time.Hour)})
	wantLines(t, conds, model.ConditionImageReady, "image ghcr.io/example/bokeh:3.9.2", "digest sha256:4b1c")
}

// While the app has not answered yet, the step says what is checked.
func TestReadyInProgressSaysWhatIsChecked(t *testing.T) {
	o := Observed{Pod: pod(corev1.PodRunning, true, false, running(now.Add(-10*time.Second))), Now: now, ReadyTimeout: 2 * time.Minute}
	_, _, evidence := Derive(ws(false), o)
	if strings.Join(evidence, "\n") != "checking GET :8501/u/alice/sales-app/, no answer yet" {
		t.Errorf("evidence = %q", evidence)
	}
}

// The kubelet's Pulled message gives the image size in bytes; the download
// line says it.
func TestDownloadLineSaysTheImageSize(t *testing.T) {
	o := Observed{Pod: readyPod(), Claim: homeClaim(), Now: t0,
		Events: []corev1.Event{pulledEvent(`Successfully pulled image "ghcr.io/example/bokeh:3.9.2" in 4.2s (4.2s including waiting). Image size: 412345678 bytes.`)}}
	_, conds, _ := Derive(ws(false), o)
	if got := lines(conds, model.ConditionImageReady); got[len(got)-1] != "downloaded in 4.2s, 412 MB" {
		t.Errorf("ImageReady = %q", got)
	}
}

// envPodWith is readyPod with an environment install that ran from t0+1s to
// t0+13s and left message as its termination message.
func envPodWith(message string) *corev1.Pod {
	at := func(s int) metav1.Time { return metav1.NewTime(t0.Add(time.Duration(s) * time.Second)) }
	p := readyPod()
	p.Spec.InitContainers = []corev1.Container{{Name: "env-tools"}, {Name: "env-install"}}
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{
		{Name: "env-tools", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed", StartedAt: at(1), FinishedAt: at(1)}}},
		{Name: "env-install", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed", StartedAt: at(1), FinishedAt: at(13), Message: message}}},
	}
	return p
}

// A finished environment install records how long it took, so the step
// says what happened once its log is gone.
func TestEnvironmentStepRecordsTheInstall(t *testing.T) {
	o := Observed{Pod: envPodWith(""), Claim: homeClaim(), Now: t0.Add(time.Hour), InstallsEnv: true}
	_, conds, _ := Derive(ws(false), o)
	wantLines(t, conds, model.ConditionEnvReady, "environment ready in 12s")
}

// The note an install leaves (an environment without a kernel) is shown
// once with the Started step, however many facts that step has.
func TestEnvironmentNoteIsShownOnce(t *testing.T) {
	note := "The environment sales has no Jupyter kernel, so JupyterLab does not offer it."
	o := Observed{Pod: envPodWith(note), Claim: homeClaim(), Now: t0.Add(time.Hour), InstallsEnv: true}
	_, conds, _ := Derive(ws(false), o)
	if n := strings.Count(find(conds, model.ConditionStarted).Message, note); n != 1 {
		t.Errorf("note shown %d times in %q", n, find(conds, model.ConditionStarted).Message)
	}
}

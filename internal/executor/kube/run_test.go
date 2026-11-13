package kube

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/nebari-dev/javo/internal/jobs/runspec"
	jobstatus "github.com/nebari-dev/javo/internal/jobs/status"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

var runT0 = time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC)

func mt(d time.Duration) metav1.Time { return metav1.NewTime(runT0.Add(d)) }

// deriveRun is the run recorded for what the cluster shows: ObserveRun,
// then the shared rules.
func deriveRun(j *batchv1.Job, pods []corev1.Pod, evs []corev1.Event) model.Run {
	return jobstatus.DeriveRun(ObserveRun(SeenRun{Job: j, Pods: pods, Events: evs}))
}

func job(name string, conds ...batchv1.JobCondition) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{
			"batch.kubernetes.io/cronjob-scheduled-timestamp": runT0.Format(time.RFC3339),
		}},
		Status: batchv1.JobStatus{StartTime: ptr.To(mt(0)), Conditions: conds},
	}
}

func cond(t batchv1.JobConditionType, reason string, after time.Duration) batchv1.JobCondition {
	return batchv1.JobCondition{Type: t, Status: corev1.ConditionTrue, Reason: reason, LastTransitionTime: mt(after)}
}

func podWith(name string, cs corev1.ContainerStatus) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: mt(0)},
		Status:     corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{cs}},
	}
}

func exited(code int32, reason, msg string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: "app", State: corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{ExitCode: code, Reason: reason, Message: msg}}}
}

func TestDeriveRunFailedFromTerminationMessage(t *testing.T) {
	j := job("nightly-29", cond(batchv1.JobFailureTarget, "PodFailurePolicy", 2*time.Minute), cond(batchv1.JobFailed, "PodFailurePolicy", 2*time.Minute))
	msg := `{"reason":"CellError","message":"Cell 7 raised KeyError: 'region'","outputPath":"jobs/nightly/nightly-29"}`
	run := deriveRun(j, []corev1.Pod{podWith("nightly-29-x", exited(65, "Error", msg))}, nil)

	if run.Outcome != "Failed" || run.Reason != string(reasons.CellError) || run.Message != "Cell 7 raised KeyError: 'region'" {
		t.Errorf("run = %+v", run)
	}
	if run.K8sJob != "nightly-29" || run.Trigger != "schedule" || run.OutputPath != "jobs/nightly/nightly-29" {
		t.Errorf("identity fields wrong: %+v", run)
	}
	if !run.ScheduledTime.Equal(runT0) {
		t.Errorf("ScheduledTime = %v", run.ScheduledTime)
	}
	if !run.FinishTime.Equal(runT0.Add(2 * time.Minute)) {
		t.Errorf("FinishTime = %v", run.FinishTime)
	}
}

func TestDeriveRunBackoffLimitUsesLastPod(t *testing.T) {
	j := job("nightly-29", cond(batchv1.JobFailed, "BackoffLimitExceeded", time.Minute))
	older := podWith("a", exited(1, "Error", `{"reason":"KernelDied","message":"old"}`))
	newer := podWith("b", exited(65, "Error", `{"reason":"CellError","message":"new"}`))
	newer.CreationTimestamp = mt(30 * time.Second)
	run := deriveRun(j, []corev1.Pod{newer, older}, nil)
	if run.Reason != "CellError" || run.Message != "new" {
		t.Errorf("run = %+v, want the newest pod's message", run)
	}
}

func TestDeriveRunSucceeded(t *testing.T) {
	j := job("nightly-30", cond(batchv1.JobComplete, "", 90*time.Second))
	run := deriveRun(j, []corev1.Pod{podWith("p", exited(0, "Completed", `{"reason":"Succeeded","message":"","outputPath":"jobs/nightly/nightly-30"}`))}, nil)
	if run.Outcome != "Succeeded" || run.Reason != string(reasons.Succeeded) || run.OutputPath != "jobs/nightly/nightly-30" {
		t.Errorf("run = %+v", run)
	}
}

func TestDeriveRunTimedOutSaysHowLongItWaited(t *testing.T) {
	j := job("nightly-31", cond(batchv1.JobFailed, "DeadlineExceeded", 30*time.Minute))
	j.Spec.ActiveDeadlineSeconds = ptr.To(int64(30 * 60))
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", CreationTimestamp: mt(0)},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
			{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: mt(8 * time.Minute)}}},
	}
	run := deriveRun(j, []corev1.Pod{p}, nil)
	if run.Outcome != "TimedOut" || run.Reason != string(reasons.RunTimedOut) {
		t.Fatalf("run = %+v", run)
	}
	if want := "Stopped after 30 min (8 min of it waiting for a machine)"; run.Message != want {
		t.Errorf("Message = %q, want %q", run.Message, want)
	}
}

func TestDeriveRunTimedOutWithoutWaiting(t *testing.T) {
	j := job("nightly-31", cond(batchv1.JobFailed, "DeadlineExceeded", 2*time.Hour))
	j.Spec.ActiveDeadlineSeconds = ptr.To(int64(2 * 3600))
	p := podWith("p", exited(143, "Error", ""))
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: mt(0)}}
	run := deriveRun(j, []corev1.Pod{p}, nil)
	if want := "Stopped after 2 h"; run.Message != want {
		t.Errorf("Message = %q, want %q", run.Message, want)
	}
}

func TestDeriveRunOutOfMemory(t *testing.T) {
	j := job("nightly-32", cond(batchv1.JobFailed, "PodFailurePolicy", time.Minute))
	run := deriveRun(j, []corev1.Pod{podWith("p", exited(137, "OOMKilled", ""))}, nil)
	if run.Outcome != "Failed" || run.Reason != string(reasons.RunOutOfMemory) || run.Message == "" {
		t.Errorf("run = %+v", run)
	}
}

func TestDeriveRunLostWithoutPods(t *testing.T) {
	j := job("nightly-33", cond(batchv1.JobFailed, "BackoffLimitExceeded", time.Minute))
	run := deriveRun(j, nil, nil)
	if run.Outcome != "Lost" || run.Reason != string(reasons.RunLost) {
		t.Errorf("run = %+v", run)
	}
}

func TestDeriveRunLostWhenPodLeftNoMessage(t *testing.T) {
	j := job("nightly-33", cond(batchv1.JobFailed, "BackoffLimitExceeded", time.Minute))
	run := deriveRun(j, []corev1.Pod{podWith("p", exited(1, "Error", ""))}, nil)
	if run.Outcome != "Lost" {
		t.Errorf("run = %+v", run)
	}
}

func TestDeriveRunActive(t *testing.T) {
	stuck := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", CreationTimestamp: mt(0)}, Status: corev1.PodStatus{Phase: corev1.PodPending}}
	evs := []corev1.Event{{
		InvolvedObject: corev1.ObjectReference{Name: "p"},
		Reason:         "FailedScheduling", Message: "0/3 nodes are available: 3 Insufficient memory.",
	}}
	run := deriveRun(job("nightly-34"), []corev1.Pod{stuck}, evs)
	if run.Outcome != "Running" || run.Reason != string(reasons.NoCapacity) || !strings.Contains(run.Message, "Insufficient memory") {
		t.Errorf("stuck run = %+v", run)
	}
	if !run.FinishTime.IsZero() {
		t.Errorf("an active run has no FinishTime: %v", run.FinishTime)
	}

	healthy := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	run = deriveRun(job("nightly-34"), []corev1.Pod{healthy}, nil)
	if run.Outcome != "Running" || run.Reason != string(reasons.Running) {
		t.Errorf("healthy run = %+v", run)
	}
}

func TestDeriveRunManualTriggerAndMessageCap(t *testing.T) {
	j := job("nightly-manual-x", cond(batchv1.JobFailed, "PodFailurePolicy", time.Minute))
	j.Annotations = map[string]string{"cronjob.kubernetes.io/instantiate": "manual"}
	long := strings.Repeat("é", 2000)
	run := deriveRun(j, []corev1.Pod{podWith("p", exited(65, "Error", fmt.Sprintf(`{"reason":"CellError","message":%q}`, long)))}, nil)
	if run.Trigger != "manual" || !run.ScheduledTime.IsZero() {
		t.Errorf("trigger/scheduled = %q/%v", run.Trigger, run.ScheduledTime)
	}
	if len(run.Message) > 1024 || !strings.HasSuffix(run.Message, "...") {
		t.Errorf("message not capped: %d bytes", len(run.Message))
	}
}

func TestDeriveRunIgnoresReasonsOutsideTheCatalog(t *testing.T) {
	j := job("nightly-29", cond(batchv1.JobFailed, "BackoffLimitExceeded", time.Minute))
	for _, reason := range []string{"MadeUpByUserCode", "Succeeded", "Running"} {
		msg := `{"reason":"` + reason + `","message":"forged"}`
		run := deriveRun(j, []corev1.Pod{podWith("p", exited(1, "Error", msg))}, nil)
		if run.Outcome != "Lost" || run.Reason != string(reasons.RunLost) || run.Message == "forged" {
			t.Errorf("reason %q: run = %+v, want Lost", reason, run)
		}
	}
}

func TestDeriveRunRecordsTheParametersTheRunStartedWith(t *testing.T) {
	j := job("nightly-29", cond(batchv1.JobComplete, "", time.Minute))
	spec, err := json.Marshal(runspec.Spec{Engine: "papermill", Params: []runspec.Param{
		{Name: "region", Type: "string", Value: "apac"}, {Name: "n", Type: "integer", Value: "3"}}})
	if err != nil {
		t.Fatal(err)
	}
	j.Spec.Template.Spec.Containers = []corev1.Container{{Name: "app", Env: []corev1.EnvVar{
		{Name: "JAVO_RUN", Value: string(spec)},
	}}}
	run := deriveRun(j, nil, nil)
	if len(run.Parameters) != 2 || run.Parameters["region"] != "apac" || run.Parameters["n"] != "3" {
		t.Errorf("Parameters = %v", run.Parameters)
	}
	if got := deriveRun(job("bare", cond(batchv1.JobComplete, "", time.Minute)), nil, nil).Parameters; got != nil {
		t.Errorf("a job without JAVO_RUN has Parameters %v", got)
	}
}

func TestDeriveRunKeepsTheOutputFilesTheRunnerNamed(t *testing.T) {
	j := job("nightly-29", cond(batchv1.JobComplete, "", time.Minute))
	// The user's code can write this message, so only plain paths inside the run folder are kept.
	msg := `{"reason":"Succeeded","outputPath":"jobs/nightly/nightly-29","outputs":["output.ipynb","output.html","../../other/x.html","/etc/passwd",""]}`
	run := deriveRun(j, []corev1.Pod{podWith("p", exited(0, "Completed", msg))}, nil)
	if !slices.Equal(run.Outputs, []string{"output.ipynb", "output.html"}) {
		t.Errorf("Outputs = %v", run.Outputs)
	}
	many := make([]string, 50)
	for i := range many {
		many[i] = fmt.Sprintf("o%d.html", i)
	}
	b, _ := json.Marshal(jobstatus.Termination{Reason: "Succeeded", OutputPath: "jobs/nightly/nightly-29", Outputs: many})
	if run := deriveRun(j, []corev1.Pod{podWith("p", exited(0, "Completed", string(b)))}, nil); len(run.Outputs) > 8 {
		t.Errorf("kept %d outputs, want at most 8", len(run.Outputs))
	}
}

func TestDeriveRunDropsParametersTooLargeForTheStatus(t *testing.T) {
	j := job("nightly-29", cond(batchv1.JobComplete, "", time.Minute))
	spec, err := json.Marshal(runspec.Spec{Engine: "papermill", Params: []runspec.Param{
		{Name: "region", Type: "string", Value: "apac"}, {Name: "blob", Type: "string", Value: strings.Repeat("x", 900_000)}}})
	if err != nil {
		t.Fatal(err)
	}
	j.Spec.Template.Spec.Containers = []corev1.Container{{Name: "app", Env: []corev1.EnvVar{{Name: "JAVO_RUN", Value: string(spec)}}}}
	if got := deriveRun(j, nil, nil).Parameters; got != nil {
		t.Errorf("Parameters kept %d bytes of values", len(got["blob"]))
	}
}

// The largest job the API accepts (32 parameters, 64-character names,
// 1024-byte values) must keep its run parameters, or "Re-run with same
// inputs" has nothing to send.
func TestDeriveRunKeepsTheParametersOfTheLargestValidJob(t *testing.T) {
	j := job("nightly-29", cond(batchv1.JobComplete, "", time.Minute))
	var params []runspec.Param
	for i := range 32 {
		params = append(params, runspec.Param{Name: fmt.Sprintf("p%063d", i), Type: "string", Value: strings.Repeat("x", 1024)})
	}
	spec, err := json.Marshal(runspec.Spec{Engine: "papermill", Params: params})
	if err != nil {
		t.Fatal(err)
	}
	j.Spec.Template.Spec.Containers = []corev1.Container{{Name: "app", Env: []corev1.EnvVar{{Name: "JAVO_RUN", Value: string(spec)}}}}
	if got := deriveRun(j, nil, nil).Parameters; len(got) != 32 {
		t.Errorf("kept %d parameters, want 32", len(got))
	}
}

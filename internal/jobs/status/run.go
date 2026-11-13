// Package status derives the outcome of one job run from its Job, pods and
// events. The functions are pure; the reconciler supplies the objects.
package status

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/executor/kube"
	"github.com/nebari-dev/javo/internal/jobs/runspec"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
	podstatus "github.com/nebari-dev/javo/internal/status"
)

const (
	annotationScheduled = "batch.kubernetes.io/cronjob-scheduled-timestamp"
	annotationInstance  = "cronjob.kubernetes.io/instantiate"
	maxMessageBytes     = 1024
	// maxOutputs and maxOutputBytes bound the output files kept per run.
	maxOutputs     = 8
	maxOutputBytes = 255
	// maxParameterBytes bounds the names and values kept per run. It covers
	// the largest job the API accepts (32 parameters, 64-byte names,
	// 1024-byte values).
	maxParameterBytes = 32 * (64 + 1024)
)

// stuckCodes are the pod problems worth showing on a run that is still
// active; other pod codes are about apps, not runs.
var stuckCodes = []reasons.Code{reasons.NoCapacity, reasons.HomeBusy, reasons.ImagePullFailed}

// EmittedReasons lists the reason codes DeriveRun can return, so the catalog
// test can require copy for each. Codes the runner writes into the termination
// message come from the runner, not from here.
func EmittedReasons() []string {
	out := []string{string(reasons.Succeeded), string(reasons.Running), string(reasons.RunTimedOut),
		string(reasons.RunOutOfMemory), string(reasons.RunLost)}
	for _, c := range stuckCodes {
		out = append(out, string(c))
	}
	return out
}

// Termination is the one-line JSON the runner leaves in the container's
// termination message. The runner writes it and DeriveRun reads it.
type Termination struct {
	Reason     string `json:"reason"`
	Message    string `json:"message"`
	OutputPath string `json:"outputPath"`
	// Outputs are the engine's files, relative to OutputPath.
	Outputs []string `json:"outputs,omitempty"`
}

// DeriveRun is the record of one run of the Kubernetes Job (the row id is
// left for the caller). It never fails: whatever the cluster shows maps to
// some outcome, with Lost when it shows nothing usable.
func DeriveRun(job *batchv1.Job, pods []corev1.Pod, evs []corev1.Event, now time.Time) model.Run {
	run := model.Run{K8sJob: job.Name, Trigger: "schedule"}
	if job.Status.StartTime != nil {
		run.StartTime = job.Status.StartTime.Time
	}
	if job.Annotations[annotationInstance] == "manual" {
		run.Trigger = "manual"
	}
	if ts, err := time.Parse(time.RFC3339, job.Annotations[annotationScheduled]); err == nil {
		run.ScheduledTime = ts
	}

	run.Parameters = runParameters(job)
	last := lastPod(pods)
	var term *Termination
	if last != nil {
		term = readTermination(last)
		if term != nil {
			run.OutputPath = term.OutputPath
			run.Outputs = runOutputs(term.Outputs)
		}
	}

	switch complete, failed := condition(job, batchv1.JobComplete), condition(job, batchv1.JobFailed); {
	case complete != nil:
		run.Outcome, run.Reason = "Succeeded", string(reasons.Succeeded)
		run.FinishTime = finishTime(job, complete)
	case failed != nil:
		run.FinishTime = finishTime(job, failed)
		run.Outcome, run.Reason, run.Message = failure(job, failed, pods, last, term)
	default:
		run.Outcome, run.Reason = "Running", string(reasons.Running)
		if last != nil {
			if _, code, evidence, ok := kube.ClassifyPod(last, eventsFor(last, evs)); ok && slices.Contains(stuckCodes, reasons.Code(code)) {
				run.Reason, run.Message = code, strings.Join(evidence, "\n")
			}
		}
	}
	run.Message = podstatus.CapMessage([]string{run.Message}, 1, maxMessageBytes)
	return run
}

// runParameters are the inputs the Job was started with: the Params of the
// shared JAVO_RUN spec.
func runParameters(job *batchv1.Job) map[string]string {
	for _, c := range job.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name != "JAVO_RUN" {
				continue
			}
			var spec runspec.Spec
			if json.Unmarshal([]byte(e.Value), &spec) != nil || len(spec.Params) == 0 {
				return nil
			}
			out := make(map[string]string, len(spec.Params))
			size := 0
			for _, p := range spec.Params {
				out[p.Name] = p.Value
				size += len(p.Name) + len(p.Value)
			}
			if size > maxParameterBytes {
				return nil // too large to keep; the run's own params.yaml still has them
			}
			return out
		}
	}
	return nil
}

// runOutputs keeps the output files that are plain paths inside the run
// folder. The user's code can write the termination message, so nothing
// else is believed.
func runOutputs(names []string) []string {
	var out []string
	for _, n := range names {
		if len(out) == maxOutputs {
			break
		}
		if len(n) <= maxOutputBytes && filepath.IsLocal(n) {
			out = append(out, n)
		}
	}
	return out
}

// failure turns a failed Job into outcome, reason and message.
func failure(job *batchv1.Job, failed *batchv1.JobCondition, pods []corev1.Pod, last *corev1.Pod, term *Termination) (outcome, reason, message string) {
	if failed.Reason == "DeadlineExceeded" {
		return "TimedOut", string(reasons.RunTimedOut), timedOutMessage(job, failed, pods)
	}
	if last != nil && oomKilled(last) {
		return "Failed", string(reasons.RunOutOfMemory),
			"The run was stopped for using more memory than it is allowed."
	}
	// The user's own code can write the termination log, so only a catalog
	// reason that names a failure is believed.
	if term != nil && failureReason(term.Reason) {
		return "Failed", term.Reason, term.Message
	}
	if last == nil {
		return "Lost", string(reasons.RunLost), "The run's machine went away before it reported a result."
	}
	return "Lost", string(reasons.RunLost), "The run stopped without reporting a result."
}

func failureReason(code string) bool {
	if _, ok := reasons.Lookup(reasons.Code(code)); !ok {
		return false
	}
	return code != string(reasons.Succeeded) && code != string(reasons.Running)
}

// timedOutMessage says how long the limit was and how much of it passed
// before the first pod was placed on a machine.
func timedOutMessage(job *batchv1.Job, failed *batchv1.JobCondition, pods []corev1.Pod) string {
	if job.Spec.ActiveDeadlineSeconds == nil {
		return "Stopped at its time limit."
	}
	msg := "Stopped after " + humanDuration(time.Duration(*job.Spec.ActiveDeadlineSeconds)*time.Second)
	if job.Status.StartTime == nil {
		return msg
	}
	start := job.Status.StartTime.Time
	waited := failed.LastTransitionTime.Time.Sub(start)
	for _, p := range pods {
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionTrue && c.LastTransitionTime.Time.Sub(start) < waited {
				waited = c.LastTransitionTime.Time.Sub(start)
			}
		}
	}
	if mins := int(waited.Minutes()); mins > 0 {
		msg += fmt.Sprintf(" (%d min of it waiting for a machine)", mins)
	}
	return msg
}

func humanDuration(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%d h", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%d min", d/time.Minute)
	}
	return d.String()
}

func condition(job *batchv1.Job, t batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		if c := &job.Status.Conditions[i]; c.Type == t && c.Status == corev1.ConditionTrue {
			return c
		}
	}
	return nil
}

func finishTime(job *batchv1.Job, c *batchv1.JobCondition) time.Time {
	if job.Status.CompletionTime != nil {
		return job.Status.CompletionTime.Time
	}
	return c.LastTransitionTime.Time
}

// lastPod is the newest pod, the one whose outcome counts after retries.
func lastPod(pods []corev1.Pod) *corev1.Pod {
	var last *corev1.Pod
	for i := range pods {
		if last == nil || pods[i].CreationTimestamp.After(last.CreationTimestamp.Time) {
			last = &pods[i]
		}
	}
	return last
}

func appStatus(p *corev1.Pod) *corev1.ContainerStateTerminated {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name == podspec.ContainerName {
			if cs.State.Terminated != nil {
				return cs.State.Terminated
			}
			return cs.LastTerminationState.Terminated
		}
	}
	return nil
}

func oomKilled(p *corev1.Pod) bool {
	t := appStatus(p)
	return t != nil && t.Reason == "OOMKilled"
}

func readTermination(p *corev1.Pod) *Termination {
	t := appStatus(p)
	if t == nil || t.Message == "" {
		return nil
	}
	var m Termination
	if json.Unmarshal([]byte(t.Message), &m) != nil {
		return nil
	}
	return &m
}

func eventsFor(p *corev1.Pod, evs []corev1.Event) []corev1.Event {
	var out []corev1.Event
	for _, e := range evs {
		if e.InvolvedObject.Name == p.Name {
			out = append(out, e)
		}
	}
	return out
}

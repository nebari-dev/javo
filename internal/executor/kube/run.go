package kube

import (
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/workload"
)

const (
	annotationScheduled = "batch.kubernetes.io/cronjob-scheduled-timestamp"
	annotationInstance  = "cronjob.kubernetes.io/instantiate"
)

// SeenRun is what the cluster shows of one run: its Job, the pod of each
// attempt and the events about those pods.
type SeenRun struct {
	Job    *batchv1.Job
	Pods   []corev1.Pod
	Events []corev1.Event
}

// ObserveRun reads the run's state off its Job and pods, or nil when it has
// no Job.
func ObserveRun(s SeenRun) *workload.State {
	job := s.Job
	if job == nil {
		return nil
	}
	run := &workload.RunState{
		Handle:   job.Name,
		Manual:   job.Annotations[annotationInstance] == "manual",
		Env:      runEnv(job),
		Attempts: len(s.Pods),
	}
	st := &workload.State{Run: run}
	if job.Status.StartTime != nil {
		run.Started = job.Status.StartTime.Time
	}
	if ts, err := time.Parse(time.RFC3339, job.Annotations[annotationScheduled]); err == nil {
		run.Slot = ts
	}
	last := lastPod(s.Pods)
	if last != nil {
		if t := mainTerminated(last); t != nil {
			run.OutOfMemory, run.Message = t.Reason == "OOMKilled", t.Message
		}
	}

	switch complete, failed := jobCond(job, batchv1.JobComplete), jobCond(job, batchv1.JobFailed); {
	case complete != nil:
		run.Done, run.Succeeded, run.Finished = true, true, finishTime(job, complete)
	case failed != nil:
		run.Done, run.Finished = true, finishTime(job, failed)
		if failed.Reason == "DeadlineExceeded" {
			run.TimedOut = true
			if d := job.Spec.ActiveDeadlineSeconds; d != nil {
				run.Timeout = time.Duration(*d) * time.Second
			}
			run.Waited = waited(job, failed, s.Pods)
		}
	case last != nil:
		if _, code, evidence, ok := ClassifyPod(last, eventsFor(last, s.Events)); ok {
			st.Code, st.Evidence = reasons.Code(code), evidence
		}
	}
	return st
}

// runEnv are the plain variables of the Job's containers.
func runEnv(job *batchv1.Job) map[string]string {
	var out map[string]string
	for _, c := range job.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if _, set := out[e.Name]; set || e.ValueFrom != nil {
				continue
			}
			if out == nil {
				out = map[string]string{}
			}
			out[e.Name] = e.Value
		}
	}
	return out
}

// waited is how long after the Job started its first pod was placed on a
// machine: the whole run when none was, zero when the Job never started.
func waited(job *batchv1.Job, failed *batchv1.JobCondition, pods []corev1.Pod) time.Duration {
	if job.Status.StartTime == nil {
		return 0
	}
	start := job.Status.StartTime.Time
	d := failed.LastTransitionTime.Time.Sub(start)
	for _, p := range pods {
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionTrue && c.LastTransitionTime.Time.Sub(start) < d {
				d = c.LastTransitionTime.Time.Sub(start)
			}
		}
	}
	return d
}

func jobCond(job *batchv1.Job, t batchv1.JobConditionType) *batchv1.JobCondition {
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

// mainTerminated is how the pod's main container last ended, or nil when it
// has not.
func mainTerminated(p *corev1.Pod) *corev1.ContainerStateTerminated {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name == podspec.ContainerName {
			return lastTerminated(cs)
		}
	}
	return nil
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

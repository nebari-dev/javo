// Package podread reads what a pod and an event about it say: which
// startup step an init container does its work in, a container's status,
// whether it has a log, when an event happened. It needs no client of a
// cluster, so what shows a cluster's pods (the gateway's logs, the
// diagnosis) and what runs them (executor/kube) share it.
package podread

import (
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/executor/startup"
)

// InitNames are the names of p's init containers, the steps of its
// workload.
func InitNames(p *corev1.Pod) []string {
	if p == nil {
		return nil
	}
	names := make([]string, len(p.Spec.InitContainers))
	for i, c := range p.Spec.InitContainers {
		names[i] = c.Name
	}
	return names
}

// InitStep is the startup step an init container of p does its work in
// (startup.StepOf).
func InitStep(p *corev1.Pod, name string) string { return startup.StepOf(InitNames(p), name) }

// EventTime is when the event last happened.
func EventTime(e *corev1.Event) time.Time {
	switch {
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

// ContainerStatus returns the status of the pod's container or init
// container called name, or nil before it has one.
func ContainerStatus(pod *corev1.Pod, name string) *corev1.ContainerStatus {
	if pod == nil {
		return nil
	}
	for _, l := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses} {
		for i := range l {
			if l[i].Name == name {
				return &l[i]
			}
		}
	}
	return nil
}

// HasLog reports whether the asked-for run of the container has a log to
// read. A container waiting to restart (CrashLoopBackOff) still has the log
// of the run that just ended, which the kubelet serves as the current one.
func HasLog(cs *corev1.ContainerStatus, previous bool) bool {
	switch {
	case cs == nil:
		return false
	case previous:
		return cs.LastTerminationState.Terminated != nil
	}
	return cs.State.Running != nil || cs.State.Terminated != nil || cs.LastTerminationState.Terminated != nil
}

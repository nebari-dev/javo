// Package kube runs workloads on Kubernetes: it is the only place that
// reads a Pod, its claim and its events.
package kube

import (
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/source"
	"github.com/nebari-dev/javo/internal/workload"
)

// Seen is what the cluster shows of one workload.
type Seen struct {
	Pod    *corev1.Pod
	Claim  *corev1.PersistentVolumeClaim
	Events []corev1.Event
	// Machines are the cluster's nodes; empty is unknown. A pod that fits
	// none of them fails as SizeTooBig, unless SizeMayWait (the size is
	// allowUnschedulable: an autoscaler may add a big enough node).
	Machines    capacity.Cluster
	SizeMayWait bool
}

// Observe reads the workload's state off its pod, or nil when it has none.
func Observe(s Seen) *workload.State {
	p := s.Pod
	if p == nil {
		return nil
	}
	st := &workload.State{
		Builds:      podBuilds(p),
		InstallsEnv: podInstallsEnv(p),
		Created:     p.CreationTimestamp.Time,
		Facts:       stepFacts(p, s.Claim, s.Events),
		Pull:        pull(p, s.Events),
	}
	at := func(phase, step string, code reasons.Code, ev ...string) *workload.State {
		st.Phase, st.Step, st.Code, st.Evidence = phase, step, code, ev
		return st
	}

	// Checked on the nodes, not the scheduler's message: "Insufficient
	// cpu" reads the same for a full cluster and for a pod no node can hold.
	if need := capacity.PodNeed(p); !s.SizeMayWait && unscheduled(p) && !s.Machines.Holds(need) {
		if need.Plain() {
			mcpu, mmem := s.Machines.Largest()
			return at("Failed", model.ConditionScheduled, reasons.SizeTooBig,
				fmt.Sprintf("needs %s; the largest machine has %s CPU and %s", need.Words(), mcpu, mmem))
		}
		return at("Failed", model.ConditionScheduled, reasons.SizeTooBig,
			fmt.Sprintf("needs %s; no machine has all of that", need.Words()))
	}
	if failing, code, ev, ok := ClassifyPod(p, s.Events); ok {
		return at(phaseFor(reasons.Code(code)), failing, reasons.Code(code), ev...)
	}

	if podReady(p) {
		return at("Running", "", reasons.Ready)
	}

	scheduled := p.Spec.NodeName != "" || podCond(p, corev1.PodScheduled)
	if p.Status.Phase == corev1.PodPending || !scheduled {
		if s.Claim != nil && s.Claim.Status.Phase != corev1.ClaimBound {
			return at("Starting", model.ConditionHomeReady, reasons.HomePending,
				fmt.Sprintf("claim %s is %s", s.Claim.Name, s.Claim.Status.Phase))
		}
		if !scheduled {
			return at("Starting", model.ConditionScheduled, reasons.Starting)
		}
		// The kubelet reports Pulled once the image is on the node (also when
		// it already was); until then the download is the step in progress.
		if newest(s.Events, "Pulled") == nil {
			return at("Starting", model.ConditionImageReady, reasons.Starting)
		}
		// The steps before the app's own run in init containers; a pod that
		// builds is in its Built step until they are all done.
		if c := runningInit(p); c != "" {
			step := InitStep(p, c)
			if step == model.ConditionStarted && podBuilds(p) {
				step = model.ConditionBuilt
			}
			return at("Starting", step, reasons.Starting)
		}
		return at("Starting", model.ConditionStarted, reasons.Starting)
	}

	// Running but not ready.
	st.Unanswered = &workload.Unanswered{Check: probeURL(p)}
	if since, ok := runningSince(p); ok {
		st.Unanswered.Since = since
	}
	return at("Starting", model.ConditionReady, reasons.Starting)
}

// podInstallsEnv reports whether the pod installs an environment before the
// app starts.
func podInstallsEnv(p *corev1.Pod) bool {
	return p != nil && slices.ContainsFunc(p.Spec.InitContainers, func(c corev1.Container) bool {
		return c.Name == pixienv.InstallContainer
	})
}

// InitStep is the startup step an init container of p does its work in:
// Built for the steps that fetch and build a site, EnvReady for the
// environment install and, on a pod that has one, everything before it
// that is not the build, else Started.
func InitStep(p *corev1.Pod, name string) string {
	switch {
	case podBuilds(p) && source.IsSiteStep(name):
		return model.ConditionBuilt
	case podInstallsEnv(p):
		return model.ConditionEnvReady
	}
	return model.ConditionStarted
}

// runningInit is the first init container of p that has not finished well,
// or "" when all have.
func runningInit(p *corev1.Pod) string {
	for _, c := range p.Spec.InitContainers {
		i := slices.IndexFunc(p.Status.InitContainerStatuses, func(cs corev1.ContainerStatus) bool { return cs.Name == c.Name })
		if i < 0 {
			return c.Name
		}
		if t := p.Status.InitContainerStatuses[i].State.Terminated; t == nil || t.ExitCode != 0 {
			return c.Name
		}
	}
	return ""
}

// podBuilds reports whether the pod makes the site it serves: it has a build
// or site-copy step.
func podBuilds(p *corev1.Pod) bool {
	return p != nil && slices.ContainsFunc(p.Spec.InitContainers, func(c corev1.Container) bool {
		return c.Name == source.BuildContainer || c.Name == source.SiteCopyContainer
	})
}

func lastTerminated(cs corev1.ContainerStatus) *corev1.ContainerStateTerminated {
	if cs.State.Terminated != nil {
		return cs.State.Terminated
	}
	return cs.LastTerminationState.Terminated
}

func podReady(p *corev1.Pod) bool { return podCond(p, corev1.PodReady) }

// unscheduled reports whether the pod has no node yet.
func unscheduled(p *corev1.Pod) bool {
	return p.Spec.NodeName == "" && !podCond(p, corev1.PodScheduled)
}
func podCond(p *corev1.Pod, t corev1.PodConditionType) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == t {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// newest returns the most recent event whose Reason is one of names.
// Matching is on the Reason field only.
func newest(evs []corev1.Event, names ...string) *corev1.Event {
	var best *corev1.Event
	for i := range evs {
		for _, r := range names {
			if evs[i].Reason == r && (best == nil || EventTime(&evs[i]).After(EventTime(best))) {
				best = &evs[i]
			}
		}
	}
	return best
}

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

// superseded reports whether the pod moved past the failure event (a volume
// attach or a sandbox refusal): a container is running, or the kubelet
// logged image pull or container progress after it.
func superseded(p *corev1.Pod, evs []corev1.Event, failure *corev1.Event) bool {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Running != nil {
			return true
		}
	}
	for _, r := range []string{"Pulling", "Pulled", "Created", "Started"} {
		if e := newest(evs, r); e != nil && EventTime(e).After(EventTime(failure)) {
			return true
		}
	}
	return false
}

func eventLine(e *corev1.Event) string { return fmt.Sprintf("event %s: %s", e.Reason, e.Message) }

func runningSince(p *corev1.Pod) (time.Time, bool) {
	for _, cs := range p.Status.ContainerStatuses {
		if r := cs.State.Running; r != nil {
			return r.StartedAt.Time, true
		}
	}
	if p.Status.StartTime != nil {
		return p.Status.StartTime.Time, true
	}
	return time.Time{}, false
}

// probeURL reads the readiness probe (an HTTP GET of a path, or a TCP
// connect to a port) from the Pod spec.
func probeURL(p *corev1.Pod) string {
	for _, c := range p.Spec.Containers {
		if c.ReadinessProbe == nil {
			continue
		}
		if h := c.ReadinessProbe.HTTPGet; h != nil {
			return fmt.Sprintf("GET :%s%s", h.Port.String(), h.Path)
		}
		if s := c.ReadinessProbe.TCPSocket; s != nil {
			return fmt.Sprintf("TCP :%s", s.Port.String())
		}
	}
	return "(none configured)"
}

package status

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/source"
)

// ClassifyPod applies the pod and event rules for a pod that is not doing its
// job: image pull failures, crashes, out of memory, FailedScheduling split by
// message, and volume attach errors. It returns the condition type that
// fails, the reason code and the quoted evidence lines, or ok=false when
// nothing is wrong (including a ready pod). The rules read only the pod and
// its events; checks that need the home claim or a ready timeout stay with
// the caller.
func ClassifyPod(p *corev1.Pod, evs []corev1.Event, _ time.Time) (failing, code string, evidence []string, ok bool) {
	found := func(f string, c reasons.Code, ev ...string) (string, string, []string, bool) {
		var lines []string
		for _, e := range ev {
			if e != "" {
				lines = append(lines, e)
			}
		}
		return f, string(c), lines, true
	}

	// The kubelet reports an eviction or a refused pod on the pod itself.
	// Evicted has one Reason for many causes, so it is the second reason
	// (after FailedScheduling) classified by message: only the scratch limit
	// reads as ScratchFull. The kubelet words it three ways: the pod's or a
	// container's ephemeral-storage limit, the node's ephemeral-storage
	// pressure, and the emptyDir sizeLimit (which is how scratch fills up).
	switch {
	case p.Status.Reason == "Evicted" && isScratchEviction(p.Status.Message):
		return found(model.ConditionStarted, reasons.ScratchFull, strings.TrimSpace(p.Status.Message))
	case p.Status.Reason == string(reasons.SupplementalGroupsPolicyNotSupported):
		return found(model.ConditionScheduled, reasons.SupplementalGroupsPolicyNotSupported, strings.TrimSpace(p.Status.Message))
	}

	// Container-level failures win: they are the most specific. Init
	// containers (the sample copy, the clone and its checks) come first, since the app never starts
	// while one fails. A restarting failed container reads as Waiting with
	// the failure in its last termination state.
	all := append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...)
	for i, cs := range all {
		isInit := i < len(p.Status.InitContainerStatuses)
		if w := cs.State.Waiting; w != nil && (w.Reason == "ImagePullBackOff" || w.Reason == "ErrImagePull") {
			return found(model.ConditionImageReady, reasons.ImagePullFailed,
				fmt.Sprintf("container %s waiting: %s: %s", cs.Name, w.Reason, w.Message))
		}
		if w := cs.State.Waiting; w != nil && w.Reason == "CreateContainerConfigError" && strings.Contains(w.Message, "runAsNonRoot") {
			// The kubelet's verifyRunAsNonRoot: the image runs as root, or
			// names a user it cannot check.
			return found(model.ConditionStarted, reasons.ImageRunsAsRoot,
				fmt.Sprintf("container %s waiting: %s: %s", cs.Name, w.Reason, w.Message))
		}
		if t := lastTerminated(cs); t != nil && (cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" || cs.State.Terminated != nil) {
			ev := fmt.Sprintf("container %s terminated: %s, exit code %d", cs.Name, t.Reason, t.ExitCode)
			if isInit {
				// A pod that builds its site fetches the code and builds it
				// in its Built step; one with an environment installs it in
				// its EnvReady step.
				step := InitStep(p, cs.Name)
				if t.ExitCode != 0 {
					// The steps of an app from Git say what to fix; evidence is
					// the failing line, never git-sync's whole log.
					if cs.Name == pixienv.InstallContainer {
						code := reasons.EnvironmentInstallFailed
						if t.ExitCode == pixienv.ExitProgramMissing {
							code = reasons.EnvironmentLacksProgram
						}
						return found(step, code, ev, strings.TrimSpace(t.Message))
					}
					if code, ok := source.Classify(cs.Name, t.Message); ok {
						return found(step, code, append([]string{ev}, source.Evidence(cs.Name, t.Message)...)...)
					}
					return found(step, reasons.SampleMissing, ev, strings.TrimSpace(t.Message))
				}
				continue
			}
			if t.Reason == "OOMKilled" {
				return found(model.ConditionStarted, reasons.OutOfMemory, ev)
			}
			if t.ExitCode != 0 {
				return found(model.ConditionStarted, reasons.AppCrashed, ev)
			}
		}
	}

	if podReady(p) {
		return "", "", nil, false
	}

	scheduled := p.Spec.NodeName != "" || podCond(p, corev1.PodScheduled)
	if p.Status.Phase == corev1.PodPending || !scheduled {
		if !scheduled {
			// FailedScheduling has one Reason for many causes, so it is the
			// only event classified by message. A scheduling failure explains
			// the pod better than a claim that is Pending only because it
			// waits for the first consumer.
			if e := newest(evs, "FailedScheduling"); e != nil {
				switch {
				case strings.Contains(e.Message, "Insufficient"):
					return found(model.ConditionScheduled, reasons.NoCapacity, eventLine(e))
				case strings.Contains(e.Message, "volume node affinity conflict"):
					return found(model.ConditionHomeReady, reasons.HomeBusy, eventLine(e))
				case strings.Contains(e.Message, "unbound") && strings.Contains(e.Message, "PersistentVolumeClaim"):
					return found(model.ConditionHomeReady, reasons.HomePending, eventLine(e))
				default:
					return found(model.ConditionScheduled, reasons.Starting, eventLine(e))
				}
			}
		}
		if e := newest(evs, "FailedAttachVolume", "Multi-Attach"); e != nil && !superseded(p, evs, e) {
			return found(model.ConditionHomeReady, reasons.HomeBusy, eventLine(e))
		}
		// The kubelet retries a refused sandbox, so one refusal can be a
		// hiccup; the same refusal repeated means the app never starts.
		if e := newest(evs, "FailedCreatePodSandBox"); e != nil && eventCount(e) >= sandboxRetries && !superseded(p, evs, e) {
			return found(model.ConditionStarted, reasons.SandboxFailed, eventLine(e))
		}
	}
	return "", "", nil, false
}

// sandboxRetries is how many times the kubelet must report a refused pod
// sandbox before the app counts as failed.
const sandboxRetries = 3

// eventCount is how many times the event happened: the recorder folds
// repeats into Count, or into Series for events.k8s.io events.
func eventCount(e *corev1.Event) int32 {
	n := max(e.Count, 1)
	if e.Series != nil {
		n = max(n, e.Series.Count)
	}
	return n
}

// phaseFor is the workspace phase for a classified code: the startup codes
// still progress, everything else has failed.
func phaseFor(code reasons.Code) string {
	switch code {
	case reasons.NoCapacity, reasons.HomeBusy, reasons.HomePending, reasons.Starting:
		return "Starting"
	}
	return "Failed"
}

// isScratchEviction reports whether an Evicted pod's message is about local
// disk: "...ephemeral-storage" (node pressure), "ephemeral local storage"
// (pod limit) or "local ephemeral storage" (container limit), or "EmptyDir
// volume ... exceeds the limit" (emptyDir sizeLimit).
func isScratchEviction(msg string) bool {
	return strings.Contains(msg, "ephemeral-storage") ||
		strings.Contains(msg, "ephemeral local storage") ||
		strings.Contains(msg, "local ephemeral storage") ||
		strings.Contains(msg, "EmptyDir volume")
}

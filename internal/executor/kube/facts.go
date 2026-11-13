package kube

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/workload"
)

const factTime = "2006-01-02 15:04:05 UTC"

// pullTime is the duration in the kubelet's Pulled message:
// `Successfully pulled image "x" in 27.1s (27.1s including waiting)`.
var pullTime = regexp.MustCompile(`" in ([0-9][0-9.]*[a-zµ]+)`)

// pullSize is the image size in the kubelet's Pulled message:
// `... Image size: 412345678 bytes.`.
var pullSize = regexp.MustCompile(`Image size: ([0-9]+) bytes`)

// stepFacts is, per condition type, what the API reported about how that
// step went for this pod: short lines meant to outlive the events behind
// them (events last about an hour). What the API did not report is left
// out.
func stepFacts(p *corev1.Pod, claim *corev1.PersistentVolumeClaim, evs []corev1.Event) map[string][]string {
	cs := appStatus(p)
	scheduled := podCondition(p, corev1.PodScheduled)
	out := map[string][]string{}
	add := func(step string, lines ...string) {
		for _, l := range lines {
			if l != "" {
				out[step] = append(out[step], l)
			}
		}
	}

	// A finished environment install: how long it took, and a note it left
	// (such as an environment beside the template's that has no kernel),
	// which belongs with the Started step.
	for _, ic := range p.Status.InitContainerStatuses {
		t := ic.State.Terminated
		if ic.Name != pixienv.InstallContainer || t == nil || t.ExitCode != 0 {
			continue
		}
		if !t.StartedAt.IsZero() && !t.FinishedAt.IsZero() {
			add(model.ConditionEnvReady, "environment ready in "+since(t.StartedAt, t.FinishedAt))
		}
		add(model.ConditionStarted, strings.TrimSpace(t.Message))
	}

	if p.Spec.NodeName != "" {
		add(model.ConditionScheduled, "node "+p.Spec.NodeName)
	}
	if scheduled != nil && !scheduled.LastTransitionTime.IsZero() {
		add(model.ConditionScheduled, "placed at "+scheduled.LastTransitionTime.UTC().Format(factTime))
	}

	if claim != nil {
		add(model.ConditionHomeReady, claimLine(claim))
		if m := podCondition(p, corev1.PodReadyToStartContainers); m != nil && scheduled != nil &&
			!m.LastTransitionTime.IsZero() && !scheduled.LastTransitionTime.IsZero() {
			add(model.ConditionHomeReady, "mounted within "+since(scheduled.LastTransitionTime, m.LastTransitionTime)+" of placement")
		}
	}

	if cs != nil {
		if cs.Image != "" {
			add(model.ConditionImageReady, "image "+cs.Image)
		}
		if _, d, ok := strings.Cut(cs.ImageID, "@"); ok {
			add(model.ConditionImageReady, "digest "+d)
		} else if strings.HasPrefix(cs.ImageID, "sha256:") {
			add(model.ConditionImageReady, "digest "+cs.ImageID)
		}

		if run := cs.State.Running; run != nil {
			add(model.ConditionStarted,
				fmt.Sprintf("container %s started at %s", cs.Name, run.StartedAt.UTC().Format(factTime)),
				fmt.Sprintf("restarts %d", cs.RestartCount))
		}
	}

	if u := probeURL(p); u != "" {
		add(model.ConditionReady, "checked "+u)
	}
	if r := podCondition(p, corev1.PodReady); r != nil && cs != nil && cs.State.Running != nil &&
		!r.LastTransitionTime.IsZero() && !cs.State.Running.StartedAt.IsZero() {
		add(model.ConditionReady, "answered "+since(cs.State.Running.StartedAt, r.LastTransitionTime)+" after the container started")
	}
	return out
}

// pull says whether the image was already on the node or pulled (with the
// time and size the kubelet gave), from the app container's newest Pulled
// event; nil without one, or before the app container has a status.
func pull(p *corev1.Pod, evs []corev1.Event) *workload.Pull {
	if appStatus(p) == nil {
		return nil
	}
	var app []corev1.Event
	for _, e := range evs {
		if e.InvolvedObject.FieldPath == "" || e.InvolvedObject.FieldPath == "spec.containers{"+podspec.ContainerName+"}" {
			app = append(app, e)
		}
	}
	e := newest(app, "Pulled")
	if e == nil {
		return nil
	}
	if strings.Contains(e.Message, "already present") {
		return &workload.Pull{Cached: true}
	}
	var got workload.Pull
	if m := pullTime.FindStringSubmatch(e.Message); m != nil {
		got.Took = m[1]
	}
	if m := pullSize.FindStringSubmatch(e.Message); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			got.Bytes = n
		}
	}
	return &got
}

func claimLine(c *corev1.PersistentVolumeClaim) string {
	line := "claim " + c.Name
	if q, ok := c.Status.Capacity[corev1.ResourceStorage]; ok {
		line += ", " + q.String()
	} else if q, ok := c.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		line += ", " + q.String() + " requested"
	}
	if c.Spec.StorageClassName != nil && *c.Spec.StorageClassName != "" {
		line += ", storage class " + *c.Spec.StorageClassName
	}
	return line
}

func appStatus(p *corev1.Pod) *corev1.ContainerStatus {
	for i := range p.Status.ContainerStatuses {
		if p.Status.ContainerStatuses[i].Name == podspec.ContainerName {
			return &p.Status.ContainerStatuses[i]
		}
	}
	return nil
}

func podCondition(p *corev1.Pod, t corev1.PodConditionType) *corev1.PodCondition {
	for i := range p.Status.Conditions {
		if c := &p.Status.Conditions[i]; c.Type == t && c.Status == corev1.ConditionTrue {
			return c
		}
	}
	return nil
}

func since(from, to metav1.Time) string {
	return max(to.Sub(from.Time), 0).Round(time.Second).String()
}

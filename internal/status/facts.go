package status

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/podspec"
)

const factTime = "2006-01-02 15:04:05 UTC"

// pullTime is the duration in the kubelet's Pulled message:
// `Successfully pulled image "x" in 27.1s (27.1s including waiting)`.
var pullTime = regexp.MustCompile(`" in ([0-9][0-9.]*[a-zµ]+)`)

// pullSize is the image size in the kubelet's Pulled message:
// `... Image size: 412345678 bytes.`.
var pullSize = regexp.MustCompile(`Image size: ([0-9]+) bytes`)

// downloadPrefixes start the line that says how the image got on the node.
var downloadPrefixes = []string{"already on the node", "downloaded"}

// stepFacts is, per condition type, what the API reported about how that
// step went for this pod: short lines meant to outlive the events behind
// them (events last about an hour). What the API did not report is left
// out. prev is the workspace's recorded conditions; a download line
// recorded for this same pod is kept once its Pulled event is gone.
func stepFacts(p *corev1.Pod, claim *corev1.PersistentVolumeClaim, evs []corev1.Event, prev []metav1.Condition) map[string][]string {
	if p == nil {
		return nil
	}
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
		add(model.ConditionImageReady, downloadLine(p, evs, prev))

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

// downloadLine says whether the image was already on the node or pulled
// (with the time and size the kubelet gave), from the app container's
// newest Pulled event. Without one it keeps the line recorded while this
// pod ran, else it is "".
func downloadLine(p *corev1.Pod, evs []corev1.Event, prev []metav1.Condition) string {
	var app []corev1.Event
	for _, e := range evs {
		if e.InvolvedObject.FieldPath == "" || e.InvolvedObject.FieldPath == "spec.containers{"+podspec.ContainerName+"}" {
			app = append(app, e)
		}
	}
	if e := newest(app, "Pulled"); e != nil {
		if strings.Contains(e.Message, "already present") {
			return "already on the node, not downloaded"
		}
		line := "downloaded"
		if m := pullTime.FindStringSubmatch(e.Message); m != nil {
			line += " in " + m[1]
		}
		if m := pullSize.FindStringSubmatch(e.Message); m != nil {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				line += ", " + megabytes(n)
			}
		}
		return line
	}
	if i := slices.IndexFunc(prev, func(c metav1.Condition) bool { return c.Type == model.ConditionImageReady }); i >= 0 {
		c := prev[i]
		if c.Status == metav1.ConditionTrue && !c.LastTransitionTime.Before(&p.CreationTimestamp) {
			for _, l := range strings.Split(c.Message, "\n") {
				for _, pre := range downloadPrefixes {
					if strings.HasPrefix(l, pre) {
						return l
					}
				}
			}
		}
	}
	return ""
}

// megabytes is n bytes in whole MB (10^6), the unit registries show.
func megabytes(n int64) string {
	return fmt.Sprintf("%d MB", (n+500_000)/1_000_000)
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

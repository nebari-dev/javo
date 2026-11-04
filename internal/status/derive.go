// Package status derives an app's phase and conditions from observed
// cluster state. Status is never the source of truth; this is pure.
package status

import (
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/source"
)

// Observed is the cluster state Derive reads.
type Observed struct {
	Pod          *corev1.Pod
	Claim        *corev1.PersistentVolumeClaim
	Events       []corev1.Event
	Now          time.Time
	ReadyTimeout time.Duration
	// TemplateMissing is true when the workspace's template does not exist.
	TemplateMissing bool
	// TemplateNotAllowed is true when the owner may no longer use the template.
	TemplateNotAllowed bool
	// TemplateError is the error from building the pod from the template, if any.
	TemplateError string
	// ImageNotAllowed is why the app's own image was refused when its pod
	// was built (its registry is not allowed); empty when it was not.
	ImageNotAllowed string
	// SecretsUnreadable is why the app's secrets could not be opened for its
	// pod (never a value); empty when they could or it has none.
	SecretsUnreadable string
	// EnvironmentProblem is why the app's environment cannot be handed to its
	// pod; empty when it can or it has none.
	EnvironmentProblem string
	// HomeConflict is true when the home claim records a different owner.
	HomeConflict bool
	// HomeReason is why the home cannot be mounted yet (from the Home's Ready
	// condition); empty when it can. HomeEvidence is what the Home found.
	HomeReason   reasons.Code
	HomeEvidence string
	// HomeMigrating is true while the owner's Home is being copied to another
	// mode; apps stay stopped until the switch.
	HomeMigrating bool
	// HomeRestoring is true while an admin restores the owner's Home from a
	// backup; apps stay stopped until the files are back.
	HomeRestoring bool
	// SizeUnknown is the workspace's size when it is not a configured size.
	SizeUnknown string
	// IdentityPending is true when the owner has no uid yet and uids are allocated.
	IdentityPending bool
	// AppLogin is the template's appLogin ("Enabled" or "Disabled").
	AppLogin string
	// NetworkPolicy is the latest probe state. Empty counts as Unknown.
	NetworkPolicy netprobe.State
	// Machines are the cluster's nodes; empty is unknown. A pod that fits
	// none of them fails as SizeTooBig, unless SizeMayWait (the size is
	// allowUnschedulable: an autoscaler may add a big enough node).
	Machines    capacity.Cluster
	SizeMayWait bool
	// Builds is true when the app's template builds what it serves (a site
	// from Git or from a home file): the app then has a Built step.
	Builds bool
	// InstallsEnv is true when the app's pod installs an environment before
	// the app starts: the app then has an EnvReady step.
	InstallsEnv bool
}

// LoginlessBlocked reports whether a new pod for a template with this
// appLogin must not start: the app has no login of its own and the cluster
// is not known to enforce NetworkPolicy.
func LoginlessBlocked(appLogin string, policy netprobe.State) bool {
	return appLogin == "Disabled" && policy != netprobe.Enforced
}

// condition types in startup order; an app that builds has Built before
// Started (orderOf).
var order = []string{
	model.ConditionScheduled,
	model.ConditionHomeReady,
	model.ConditionImageReady,
	model.ConditionStarted,
	model.ConditionReady,
}

// orderOf is the condition types of the app observed in o, in startup
// order: a Built step before Started for an app that builds, then an
// EnvReady step for one that installs an environment (its pod builds first).
func orderOf(o Observed) []string {
	out := slices.Clone(order)
	i := slices.Index(out, model.ConditionStarted)
	if o.InstallsEnv || podInstallsEnv(o.Pod) {
		out = slices.Insert(out, i, model.ConditionEnvReady)
	}
	if o.Builds || podBuilds(o.Pod) {
		out = slices.Insert(out, i, model.ConditionBuilt)
	}
	return out
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

// Derive returns the phase, the conditions (five, six with a Built step) and
// the evidence lines (quoted event or container text) behind the first
// failing condition.
func Derive(ws *model.App, o Observed) (phase string, conds []metav1.Condition, evidence []string) {
	order := orderOf(o)
	build := func(phase, failing string, code reasons.Code, ev ...string) (string, []metav1.Condition, []string) {
		var lines []string
		for _, e := range ev {
			if e != "" {
				lines = append(lines, e)
			}
		}
		cs := conditions(ws, o.Now, order, failing, code)
		facts := stepFacts(o.Pod, o.Claim, o.Events, ws.Status.Conditions)
		for i := range cs {
			if f := facts[cs[i].Type]; cs[i].Status == metav1.ConditionTrue && len(f) > 0 {
				cs[i].Message = CapMessage(f, maxEvidenceLines, maxEvidenceChars)
			}
		}
		return phase, cs, lines
	}

	if ws.Held(o.Now) {
		cs := make([]metav1.Condition, 0, len(order))
		for _, t := range order {
			cs = append(cs, cond(ws, o.Now, t, metav1.ConditionFalse, reasons.RestoreHeld))
		}
		return "Pending", cs, nil
	}
	if ws.Paused {
		code, why := reasons.Stopped, ""
		switch {
		case ws.StoppedByAdmin:
			code = reasons.StoppedByAdmin
		case ws.StoppedIdleAfter > 0:
			code, why = reasons.StoppedIdle, reasons.Msg(reasons.MsgStoppedIdle, reasons.Minutes(ws.StoppedIdleAfter))
		}
		cs := make([]metav1.Condition, 0, len(order))
		for _, t := range order {
			c := cond(ws, o.Now, t, metav1.ConditionFalse, code)
			if why != "" {
				c.Message += "\n" + why
			}
			cs = append(cs, c)
		}
		return "Stopped", cs, nil
	}
	if o.TemplateMissing {
		return build("Failed", model.ConditionScheduled, reasons.TemplateMissing)
	}
	if o.TemplateNotAllowed {
		return build("Failed", model.ConditionScheduled, reasons.TemplateNotAllowed)
	}
	if o.TemplateError != "" {
		return build("Failed", model.ConditionScheduled, reasons.TemplateInvalid, o.TemplateError)
	}
	if o.ImageNotAllowed != "" {
		return build("Failed", model.ConditionScheduled, reasons.ImageNotAllowed, o.ImageNotAllowed)
	}
	if o.SecretsUnreadable != "" {
		return build("Failed", model.ConditionScheduled, reasons.SecretsUnreadable, o.SecretsUnreadable)
	}
	if o.EnvironmentProblem != "" {
		return build("Failed", model.ConditionScheduled, reasons.EnvironmentUnavailable, o.EnvironmentProblem)
	}
	if o.SizeUnknown != "" {
		return build("Failed", model.ConditionScheduled, reasons.SizeUnknown,
			fmt.Sprintf("size %q is not one of the configured sizes", o.SizeUnknown))
	}
	// A pod that already runs is left alone: a probe that failed once does
	// not stop the policy from being enforced.
	if o.Pod == nil && LoginlessBlocked(o.AppLogin, o.NetworkPolicy) {
		return build("Failed", model.ConditionScheduled, reasons.NetworkPolicyNotEnforced)
	}
	if o.HomeConflict {
		return build("Failed", model.ConditionHomeReady, reasons.HomeConflict)
	}
	if o.HomeMigrating {
		return build("Stopped", model.ConditionHomeReady, reasons.HomeMigrating)
	}
	if o.HomeRestoring {
		return build("Stopped", model.ConditionHomeReady, reasons.HomeRestoring)
	}
	if o.HomeReason != "" {
		phase := "Failed"
		if slices.Contains([]reasons.Code{reasons.HomePreparing, reasons.HomePending, reasons.IdentityPending}, o.HomeReason) {
			phase = "Pending"
		}
		return build(phase, model.ConditionHomeReady, o.HomeReason, o.HomeEvidence)
	}
	if o.IdentityPending {
		return build("Pending", model.ConditionHomeReady, reasons.IdentityPending)
	}
	p := o.Pod
	if p == nil {
		return build("Pending", model.ConditionScheduled, reasons.Starting)
	}

	// Checked on the nodes, not the scheduler's message: "Insufficient
	// cpu" reads the same for a full cluster and for a pod no node can hold.
	if need := capacity.PodNeed(p); !o.SizeMayWait && unscheduled(p) && !o.Machines.Holds(need) {
		if need.Plain() {
			mcpu, mmem := o.Machines.Largest()
			return build("Failed", model.ConditionScheduled, reasons.SizeTooBig,
				fmt.Sprintf("needs %s; the largest machine has %s CPU and %s", need.Words(), mcpu, mmem))
		}
		return build("Failed", model.ConditionScheduled, reasons.SizeTooBig,
			fmt.Sprintf("needs %s; no machine has all of that", need.Words()))
	}
	if failing, code, ev, ok := ClassifyPod(p, o.Events, o.Now); ok {
		return build(phaseFor(reasons.Code(code)), failing, reasons.Code(code), ev...)
	}

	if podReady(p) {
		return build("Running", "", reasons.Ready)
	}

	scheduled := p.Spec.NodeName != "" || podCond(p, corev1.PodScheduled)
	if p.Status.Phase == corev1.PodPending || !scheduled {
		if o.Claim != nil && o.Claim.Status.Phase != corev1.ClaimBound {
			return build("Starting", model.ConditionHomeReady, reasons.HomePending,
				fmt.Sprintf("claim %s is %s", o.Claim.Name, o.Claim.Status.Phase))
		}
		if !scheduled {
			return build("Starting", model.ConditionScheduled, reasons.Starting)
		}
		// The kubelet reports Pulled once the image is on the node (also when
		// it already was); until then the download is the step in progress.
		if newest(o.Events, "Pulled") == nil {
			return build("Starting", model.ConditionImageReady, reasons.Starting)
		}
		// The steps before the app's own run in init containers; a pod that
		// builds is in its Built step until they are all done.
		if c := runningInit(p); c != "" {
			step := InitStep(p, c)
			if step == model.ConditionStarted && podBuilds(p) {
				step = model.ConditionBuilt
			}
			return build("Starting", step, reasons.Starting)
		}
		return build("Starting", model.ConditionStarted, reasons.Starting)
	}

	// Running but not ready.
	if since, ok := runningSince(p); ok && o.ReadyTimeout > 0 && o.Now.Sub(since) > o.ReadyTimeout {
		return build("Failed", model.ConditionReady, reasons.AppNotAnswering,
			fmt.Sprintf("readiness probe %s not ready for %s", probeURL(p), o.Now.Sub(since).Round(time.Second)))
	}
	return build("Starting", model.ConditionReady, reasons.Starting,
		fmt.Sprintf("checking %s, no answer yet", probeURL(p)))
}

// conditions marks every type before failing True, failing False with code,
// and later types Unknown (not yet evaluated). An empty failing means all True.
func conditions(ws *model.App, now time.Time, order []string, failing string, code reasons.Code) []metav1.Condition {
	cs := make([]metav1.Condition, 0, len(order))
	hit := false
	for _, t := range order {
		switch {
		case failing == "":
			cs = append(cs, cond(ws, now, t, metav1.ConditionTrue, reasons.Ready))
		case t == failing:
			hit = true
			cs = append(cs, cond(ws, now, t, metav1.ConditionFalse, code))
		case hit:
			cs = append(cs, cond(ws, now, t, metav1.ConditionUnknown, reasons.Starting))
		default:
			cs = append(cs, cond(ws, now, t, metav1.ConditionTrue, reasons.Ready))
		}
	}
	return cs
}

func cond(ws *model.App, now time.Time, t string, s metav1.ConditionStatus, code reasons.Code) metav1.Condition {
	r, _ := reasons.Lookup(code)
	return metav1.Condition{Type: t, Status: s, Reason: string(code), Message: r.Title,
		ObservedGeneration: ws.Version, LastTransitionTime: metav1.NewTime(now)}
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

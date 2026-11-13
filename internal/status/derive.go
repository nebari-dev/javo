// Package status derives an app's phase and conditions from what was
// observed of it. Status is never the source of truth; this is pure.
package status

import (
	"fmt"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/workload"
)

// Observed is what Derive reads.
type Observed struct {
	// Workload is what the executor saw of the app's workload; nil when it
	// has none.
	Workload     *workload.State
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
	if o.InstallsEnv || o.Workload != nil && o.Workload.InstallsEnv {
		out = slices.Insert(out, i, model.ConditionEnvReady)
	}
	if o.Builds || o.Workload != nil && o.Workload.Builds {
		out = slices.Insert(out, i, model.ConditionBuilt)
	}
	return out
}

// Derive returns the phase, the conditions (five, six with a Built step) and
// the evidence lines (quoted event or container text) behind the first
// failing condition.
func Derive(ws *model.App, o Observed) (phase string, conds []metav1.Condition, evidence []string) {
	order := orderOf(o)
	st := o.Workload
	build := func(phase, failing string, code reasons.Code, ev ...string) (string, []metav1.Condition, []string) {
		var lines []string
		for _, e := range ev {
			if e != "" {
				lines = append(lines, e)
			}
		}
		cs := conditions(ws, o.Now, order, failing, code)
		facts := stepFacts(st, ws.Status.Conditions)
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
	if st == nil && LoginlessBlocked(o.AppLogin, o.NetworkPolicy) {
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
	if st == nil {
		return build("Pending", model.ConditionScheduled, reasons.Starting)
	}
	if u := st.Unanswered; u != nil {
		if waited := o.Now.Sub(u.Since); !u.Since.IsZero() && o.ReadyTimeout > 0 && waited > o.ReadyTimeout {
			return build("Failed", model.ConditionReady, reasons.AppNotAnswering,
				fmt.Sprintf("readiness probe %s not ready for %s", u.Check, waited.Round(time.Second)))
		}
		return build("Starting", model.ConditionReady, reasons.Starting,
			fmt.Sprintf("checking %s, no answer yet", u.Check))
	}
	return build(st.Phase, st.Step, st.Code, st.Evidence...)
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

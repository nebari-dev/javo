package supervise

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nebari-dev/javo/internal/executor/startup"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/workload"
)

// killed is the exit code of a unit ended by SIGKILL (128 + 9). The
// supervisor's own kills are never recorded as a failure, so a main unit
// that ends this way is taken to have been ended by the kernel for going
// over its memory limit. The exit code is the sign that always shows: not
// every Docker daemon sets its OOMKilled flag for such a container
// (docker.ContainerState). Someone else's SIGKILL reads the same.
const killed = 137

const factTime = "2006-01-02 15:04:05 UTC"

// state is what w is doing, in the shapes the Kubernetes executor reports
// (internal/executor/kube): the same steps, codes and facts, so one status
// rule reads both.
func (w *work) state() *workload.State {
	st := &workload.State{
		Builds: startup.Builds(w.steps()), InstallsEnv: startup.InstallsEnv(w.steps()),
		Created: w.created, Facts: w.facts(),
		Fingerprint: w.spec.Fingerprint, Running: !w.mainSince.IsZero(),
	}
	// It describes the app and is never recorded on a workload.
	st.Fingerprint.SizeUnpicked = false
	main := w.spec.Main.Name
	// A unit that failed explains the workload best, the earliest first.
	for _, u := range append(slices.Clone(w.spec.Steps), w.spec.Main) {
		if f := w.failed[u.Name]; f != nil {
			st.Phase, st.Step = "Failed", w.stepOf(u.Name)
			st.Code, st.Evidence = w.diagnose(u.Name, f)
			return st
		}
	}
	switch {
	case w.at != main:
		st.Phase, st.Step, st.Code = "Starting", w.stepOf(w.at), reasons.Starting
	case w.mainSince.IsZero():
		st.Phase, st.Step, st.Code = "Starting", model.ConditionStarted, reasons.Starting
	case !w.ready:
		st.Phase, st.Step, st.Code = "Starting", model.ConditionReady, reasons.Starting
		st.Unanswered = &workload.Unanswered{Since: w.mainSince, Check: checkWords(w.spec)}
	default:
		st.Phase, st.Code = "Running", reasons.Ready
	}
	return st
}

// steps are the names of the workload's steps.
func (w *work) steps() []string {
	names := make([]string, len(w.spec.Steps))
	for i, u := range w.spec.Steps {
		names[i] = u.Name
	}
	return names
}

// stepOf is the startup step the unit called name does its work in: the
// main unit in Started, a step where startup.StepOf says.
func (w *work) stepOf(name string) string {
	if name == w.spec.Main.Name || name == "" {
		return model.ConditionStarted
	}
	return startup.StepOf(w.steps(), name)
}

// diagnose names the failure f of the unit called name, with its evidence:
// what happened, then the lines that say why.
func (w *work) diagnose(name string, f *failure) (reasons.Code, []string) {
	if name == w.spec.Main.Name {
		code := reasons.AppCrashed
		if f.exit == killed {
			code = reasons.OutOfMemory
		}
		return code, append([]string{f.what}, f.output...)
	}
	code, lines := startup.Failed(name, f.exit, strings.Join(f.output, "\n"))
	if lines == nil {
		lines = f.output
	}
	return code, append([]string{f.what}, lines...)
}

// facts is, per startup step, short lines on how it went.
func (w *work) facts() map[string][]string {
	out := map[string][]string{
		model.ConditionReady: {"checked " + checkWords(w.spec)},
	}
	if d, ok := w.took[pixienv.InstallContainer]; ok {
		out[model.ConditionEnvReady] = []string{"environment ready in " + d.Round(time.Second).String()}
	}
	if !w.mainSince.IsZero() {
		out[model.ConditionStarted] = []string{
			fmt.Sprintf("unit %s started at %s", w.spec.Main.Name, w.mainSince.UTC().Format(factTime)),
			fmt.Sprintf("restarts %d", w.restarts),
		}
	}
	if w.ready {
		out[model.ConditionReady] = append(out[model.ConditionReady],
			"answered "+w.answered.Sub(w.mainSince).Round(time.Second).String()+" after the unit started")
	}
	return out
}

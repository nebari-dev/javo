package supervise

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/source"
	"github.com/nebari-dev/javo/internal/workload"
)

const factTime = "2006-01-02 15:04:05 UTC"

// state is what w is doing, in the shapes the Kubernetes executor reports
// (internal/executor/kube): the same steps, codes and facts, so one status
// rule reads both.
func (w *work) state() *workload.State {
	st := &workload.State{
		Builds: w.builds(), InstallsEnv: w.installsEnv(),
		Created: w.created, Facts: w.facts(),
	}
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

func (w *work) builds() bool {
	return w.spec.Step(source.BuildContainer) != nil || w.spec.Step(source.SiteCopyContainer) != nil
}

func (w *work) installsEnv() bool { return w.spec.Step(pixienv.InstallContainer) != nil }

// stepOf is the startup step the unit called name does its work in: Built
// for the steps that fetch and build a site, EnvReady for the environment
// install and, in a workload that has one, every other step, else Started.
func (w *work) stepOf(name string) string {
	switch {
	case name == w.spec.Main.Name || name == "":
		return model.ConditionStarted
	case w.builds() && source.IsSiteStep(name):
		return model.ConditionBuilt
	case w.installsEnv():
		return model.ConditionEnvReady
	}
	return model.ConditionStarted
}

// diagnose names the failure f of the unit called name, with its evidence:
// what happened, then the lines that say why.
func (w *work) diagnose(name string, f *failure) (reasons.Code, []string) {
	ev := append([]string{f.what}, f.output...)
	text := strings.Join(f.output, "\n")
	switch {
	case name == w.spec.Main.Name:
		return reasons.AppCrashed, ev
	case name == pixienv.InstallContainer && f.exit == pixienv.ExitProgramMissing:
		return reasons.EnvironmentLacksProgram, ev
	case name == pixienv.InstallContainer:
		return reasons.EnvironmentInstallFailed, ev
	}
	if code, ok := source.Classify(name, text); ok {
		return code, append([]string{f.what}, source.Evidence(name, text)...)
	}
	return reasons.SampleMissing, ev
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

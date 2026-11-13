// Package startup says what the steps of a workload mean, the same way on
// every platform: which startup step (a model.Condition* type) a step
// does its work in, and why one that failed did. The executors read their
// own platform (a Pod's init containers, a container's exit) and ask here.
package startup

import (
	"slices"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/source"
)

// Builds reports whether a workload whose steps are called steps makes
// the site it serves: it has a build or site-copy step.
func Builds(steps []string) bool {
	return slices.Contains(steps, source.BuildContainer) || slices.Contains(steps, source.SiteCopyContainer)
}

// InstallsEnv reports whether a workload whose steps are called steps
// installs an environment before its main unit starts.
func InstallsEnv(steps []string) bool { return slices.Contains(steps, pixienv.InstallContainer) }

// StepOf is the startup step that the step called name, of a workload
// whose steps are called steps, does its work in: Built for the steps
// that fetch and build a site, EnvReady for the environment install and,
// in a workload that has one, everything before it that is not the build,
// else Started.
func StepOf(steps []string, name string) string {
	switch {
	case Builds(steps) && source.IsSiteStep(name):
		return model.ConditionBuilt
	case InstallsEnv(steps):
		return model.ConditionEnvReady
	}
	return model.ConditionStarted
}

// Failed names why the step called name ended with the exit code exit,
// having said the text said last. The steps of an app from Git say what
// to fix, and evidence is then the lines of said that do, never the whole
// of it; for any other step evidence is nil, and all of said is.
func Failed(name string, exit int, said string) (code reasons.Code, evidence []string) {
	if name == pixienv.InstallContainer {
		if exit == pixienv.ExitProgramMissing {
			return reasons.EnvironmentLacksProgram, nil
		}
		return reasons.EnvironmentInstallFailed, nil
	}
	if code, ok := source.Classify(name, said); ok {
		// Never nil: the step's own rules picked, even when they picked
		// nothing.
		return code, append([]string{}, source.Evidence(name, said)...)
	}
	return reasons.SampleMissing, nil
}

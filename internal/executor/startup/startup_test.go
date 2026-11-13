package startup_test

import (
	"slices"
	"testing"

	"github.com/nebari-dev/javo/internal/executor/startup"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/source"
)

// A step does its work in Built when the workload builds a site and the
// step fetches or builds it, in EnvReady when the workload installs an
// environment, and in Started otherwise.
func TestStepOf(t *testing.T) {
	site := []string{source.CloneContainer, source.CheckContainer, source.BuildContainer}
	env := []string{"sample-copy", pixienv.InstallContainer}
	for _, c := range []struct {
		steps []string
		name  string
		want  string
	}{
		{site, source.CloneContainer, model.ConditionBuilt},
		{site, source.BuildContainer, model.ConditionBuilt},
		{site, "pod-files", model.ConditionStarted},
		{[]string{source.SiteCopyContainer}, source.SiteCopyContainer, model.ConditionBuilt},
		// A clone that is not built is not a Built step.
		{[]string{source.CloneContainer}, source.CloneContainer, model.ConditionStarted},
		{env, pixienv.InstallContainer, model.ConditionEnvReady},
		{env, "sample-copy", model.ConditionEnvReady},
		{append(slices.Clone(site), pixienv.InstallContainer), source.CloneContainer, model.ConditionBuilt},
		{append(slices.Clone(site), pixienv.InstallContainer), "pod-files", model.ConditionEnvReady},
		{[]string{"sample-copy"}, "sample-copy", model.ConditionStarted},
		{nil, "anything", model.ConditionStarted},
	} {
		if got := startup.StepOf(c.steps, c.name); got != c.want {
			t.Errorf("StepOf(%v, %q) = %q, want %q", c.steps, c.name, got, c.want)
		}
	}
	if !startup.Builds(site) || startup.Builds(env) || !startup.InstallsEnv(env) || startup.InstallsEnv(site) {
		t.Error("Builds or InstallsEnv misread the steps")
	}
}

// A failed step is named by what it is: the environment install by its
// exit code, a step of an app from Git by what it printed, and any other
// as a sample that could not be copied.
func TestFailed(t *testing.T) {
	for _, c := range []struct {
		name string
		exit int
		said string
		code reasons.Code
		// picked is whether the step's own rules pick the evidence.
		picked bool
	}{
		{pixienv.InstallContainer, 1, "boom", reasons.EnvironmentInstallFailed, false},
		{pixienv.InstallContainer, pixienv.ExitProgramMissing, "", reasons.EnvironmentLacksProgram, false},
		{source.BuildContainer, 1, "npm ERR!", reasons.BuildFailed, true},
		{"sample-copy", 3, "cp: no such folder", reasons.SampleMissing, false},
	} {
		code, lines := startup.Failed(c.name, c.exit, c.said)
		if code != c.code || (lines != nil) != c.picked {
			t.Errorf("Failed(%q, %d) = %q with evidence %q, want %q", c.name, c.exit, code, lines, c.code)
		}
	}
	// The evidence of a step of an app from Git is source.Evidence's.
	said := "fatal: repository not found"
	if _, lines := startup.Failed(source.CloneContainer, 1, said); !slices.Equal(lines, source.Evidence(source.CloneContainer, said)) {
		t.Errorf("evidence %q", lines)
	}
}

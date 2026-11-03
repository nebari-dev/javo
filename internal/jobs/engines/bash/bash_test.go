package bash_test

import (
	"slices"
	"testing"

	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/jobs/engines/bash"
)

func TestStepsRunTheScriptThroughALoginBashInTheHomeFolder(t *testing.T) {
	script := "echo \"Hello from $(hostname)\"\nls ~"
	r := engine.Run{Command: []string{script}, RunDir: "/home/r/1", Home: "/home"}
	var e engine.Engine = bash.New() // the runner only sees the interface
	got, err := e.Steps(r)
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"bash", "-lc", script}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("Steps = %q, want %q", got, want)
	}
	if dir := engine.WorkDir(e, r); dir != "/home" {
		t.Errorf("WorkDir = %q, want the home folder", dir)
	}
}

func TestStepsRejectAnythingButOneScript(t *testing.T) {
	for _, cmd := range [][]string{nil, {""}, {"echo a", "echo b"}} {
		if _, err := bash.New().Steps(engine.Run{Command: cmd}); err == nil {
			t.Errorf("Steps(%q): want an error", cmd)
		}
	}
}

func TestEnvCarriesParametersLikeACommandJob(t *testing.T) {
	r := engine.Run{ParamsFile: "/r/params.yaml", Params: map[string]string{"region": "emea"}}
	want := []string{"JAVO_PARAMS_FILE=/r/params.yaml", "JAVO_PARAM_REGION=emea"}
	if got := bash.New().Env(r); !slices.Equal(got, want) {
		t.Errorf("Env = %q, want %q", got, want)
	}
}

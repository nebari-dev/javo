package command_test

import (
	"slices"
	"testing"

	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/jobs/engines/command"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestStepsAreTheCommandUnchangedAndNeverCarryParameterValues(t *testing.T) {
	r := engine.Run{
		Command: []string{"python", "etl.py", "--fast"},
		Params:  map[string]string{"region": "emea; rm -rf ~"},
	}
	got, err := command.New().Steps(r)
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"python", "etl.py", "--fast"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("Steps = %q, want %q", got, want)
	}
}

func TestStepsRejectAnEmptyCommand(t *testing.T) {
	if _, err := command.New().Steps(engine.Run{}); err == nil {
		t.Error("want an error for an empty command")
	}
}

func TestEnvCarriesParametersAndTheParamsFile(t *testing.T) {
	r := engine.Run{ParamsFile: "/r/params.yaml", Params: map[string]string{"region": "emea; rm -rf ~", "max-rows": "5"}}
	var e engine.Engine = command.New() // the runner only sees the interface
	got := e.Env(r)
	want := []string{"JAVO_PARAMS_FILE=/r/params.yaml", "JAVO_PARAM_MAX_ROWS=5", "JAVO_PARAM_REGION=emea; rm -rf ~"}
	if !slices.Equal(got, want) {
		t.Errorf("Env = %q, want %q", got, want)
	}
}

func TestClassifyNonZeroExitIsCommandFailed(t *testing.T) {
	got := command.New().Classify(engine.Run{}, 0, 2, []byte("working\nno such file: data.csv\n"))
	if got.Reason != string(reasons.CommandFailed) {
		t.Fatalf("Reason = %q", got.Reason)
	}
	if want := "The command exited with code 2: no such file: data.csv"; got.Message != want {
		t.Errorf("Message = %q, want %q", got.Message, want)
	}
}

func TestClassifyZeroExitHasNoReason(t *testing.T) {
	if got := command.New().Classify(engine.Run{}, 0, 0, nil); got.Reason != "" {
		t.Errorf("Reason = %q", got.Reason)
	}
}

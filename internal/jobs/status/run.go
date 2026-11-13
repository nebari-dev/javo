// Package status derives the outcome of one job run from what an executor
// saw of it. The functions are pure; the reconciler supplies the state.
package status

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nebari-dev/javo/internal/jobs/runspec"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	podstatus "github.com/nebari-dev/javo/internal/status"
	"github.com/nebari-dev/javo/internal/workload"
)

const (
	// envRun is the variable a run's spec is started with.
	envRun          = "JAVO_RUN"
	maxMessageBytes = 1024
	// maxOutputs and maxOutputBytes bound the output files kept per run.
	maxOutputs     = 8
	maxOutputBytes = 255
	// maxParameterBytes bounds the names and values kept per run. It covers
	// the largest job the API accepts (32 parameters, 64-byte names,
	// 1024-byte values).
	maxParameterBytes = 32 * (64 + 1024)
)

// stuckCodes are the pod problems worth showing on a run that is still
// active; other pod codes are about apps, not runs.
var stuckCodes = []reasons.Code{reasons.NoCapacity, reasons.HomeBusy, reasons.ImagePullFailed}

// EmittedReasons lists the reason codes DeriveRun can return, so the catalog
// test can require copy for each. Codes the runner writes into the termination
// message come from the runner, not from here.
func EmittedReasons() []string {
	out := []string{string(reasons.Succeeded), string(reasons.Running), string(reasons.RunTimedOut),
		string(reasons.RunOutOfMemory), string(reasons.RunLost)}
	for _, c := range stuckCodes {
		out = append(out, string(c))
	}
	return out
}

// Termination is the one-line JSON the runner leaves in the container's
// termination message. The runner writes it and DeriveRun reads it.
type Termination struct {
	Reason     string `json:"reason"`
	Message    string `json:"message"`
	OutputPath string `json:"outputPath"`
	// Outputs are the engine's files, relative to OutputPath.
	Outputs []string `json:"outputs,omitempty"`
}

// DeriveRun is the record of one run an executor reported (the row id is
// left for the caller). It never fails: whatever the platform shows maps to
// some outcome, with Lost when it shows nothing usable.
func DeriveRun(st *workload.State) model.Run {
	seen := st.Run
	run := model.Run{K8sJob: seen.Handle, Trigger: "schedule", StartTime: seen.Started, ScheduledTime: seen.Slot}
	if seen.Manual {
		run.Trigger = "manual"
	}

	run.Parameters = runParameters(seen.Env[envRun])
	term := readTermination(seen.Message)
	if term != nil {
		run.OutputPath = term.OutputPath
		run.Outputs = runOutputs(term.Outputs)
	}

	switch {
	case seen.Done && seen.Succeeded:
		run.Outcome, run.Reason = "Succeeded", string(reasons.Succeeded)
		run.FinishTime = seen.Finished
	case seen.Done:
		run.FinishTime = seen.Finished
		run.Outcome, run.Reason, run.Message = failure(seen, term)
	default:
		run.Outcome, run.Reason = "Running", string(reasons.Running)
		if slices.Contains(stuckCodes, st.Code) {
			run.Reason, run.Message = string(st.Code), strings.Join(st.Evidence, "\n")
		}
	}
	run.Message = podstatus.CapMessage([]string{run.Message}, 1, maxMessageBytes)
	return run
}

// runParameters are the inputs the run was started with: the Params of the
// shared JAVO_RUN spec.
func runParameters(raw string) map[string]string {
	var spec runspec.Spec
	if json.Unmarshal([]byte(raw), &spec) != nil || len(spec.Params) == 0 {
		return nil
	}
	out := make(map[string]string, len(spec.Params))
	size := 0
	for _, p := range spec.Params {
		out[p.Name] = p.Value
		size += len(p.Name) + len(p.Value)
	}
	if size > maxParameterBytes {
		return nil // too large to keep; the run's own params.yaml still has them
	}
	return out
}

// runOutputs keeps the output files that are plain paths inside the run
// folder. The user's code can write the termination message, so nothing
// else is believed.
func runOutputs(names []string) []string {
	var out []string
	for _, n := range names {
		if len(out) == maxOutputs {
			break
		}
		if len(n) <= maxOutputBytes && filepath.IsLocal(n) {
			out = append(out, n)
		}
	}
	return out
}

// failure turns a run that ended badly into outcome, reason and message.
func failure(seen *workload.RunState, term *Termination) (outcome, reason, message string) {
	if seen.TimedOut {
		return "TimedOut", string(reasons.RunTimedOut), timedOutMessage(seen)
	}
	if seen.OutOfMemory {
		return "Failed", string(reasons.RunOutOfMemory),
			"The run was stopped for using more memory than it is allowed."
	}
	// The user's own code can write the termination log, so only a catalog
	// reason that names a failure is believed.
	if term != nil && failureReason(term.Reason) {
		return "Failed", term.Reason, term.Message
	}
	if seen.Attempts == 0 {
		return "Lost", string(reasons.RunLost), "The run's machine went away before it reported a result."
	}
	return "Lost", string(reasons.RunLost), "The run stopped without reporting a result."
}

func failureReason(code string) bool {
	if _, ok := reasons.Lookup(reasons.Code(code)); !ok {
		return false
	}
	return code != string(reasons.Succeeded) && code != string(reasons.Running)
}

// timedOutMessage says how long the limit was and how much of it passed
// before the run was given a machine.
func timedOutMessage(seen *workload.RunState) string {
	if seen.Timeout == 0 {
		return "Stopped at its time limit."
	}
	msg := "Stopped after " + humanDuration(seen.Timeout)
	if mins := int(seen.Waited.Minutes()); mins > 0 {
		msg += fmt.Sprintf(" (%d min of it waiting for a machine)", mins)
	}
	return msg
}

func humanDuration(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%d h", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%d min", d/time.Minute)
	}
	return d.String()
}

func readTermination(raw string) *Termination {
	if raw == "" {
		return nil
	}
	var m Termination
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	return &m
}

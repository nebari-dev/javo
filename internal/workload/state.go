// Package workload describes what Javo runs (an app, a job's run) and what
// was seen of it, in terms no one platform owns: the executors translate
// to and from it.
package workload

import (
	"time"

	"github.com/nebari-dev/javo/internal/reasons"
)

// State is what an executor saw of one workload that exists.
type State struct {
	// Phase is Starting, Running or Failed.
	Phase string
	// Step is the startup step (a model.Condition* type) the workload is in
	// or failed at; empty once it runs and answers.
	Step string
	// Code is why it is there, and Evidence the platform's own words for it.
	Code     reasons.Code
	Evidence []string
	// Unanswered is set while the workload runs and its readiness check has
	// not passed: whether that is still starting or a failure depends on
	// how long the caller waits.
	Unanswered *Unanswered

	// Builds and InstallsEnv say the workload has a Built and an EnvReady
	// step.
	Builds, InstallsEnv bool
	// Created is when this instance was made.
	Created time.Time
	// Facts are, per startup step, short lines on how it went.
	Facts map[string][]string
	// Pull is how the image got there; nil when the platform did not say.
	Pull *Pull
}

// Unanswered is a readiness check that has not passed yet.
type Unanswered struct {
	// Since is when the workload started running; zero when unknown.
	Since time.Time
	// Check is the check in words, such as "GET :8501/".
	Check string
}

// Pull is how a workload's image got onto the machine.
type Pull struct {
	// Cached is true when it was already there.
	Cached bool
	// Took is how long the download took, as the platform worded it; empty
	// when it did not say.
	Took string
	// Bytes is the image size; zero when the platform did not say.
	Bytes int64
}

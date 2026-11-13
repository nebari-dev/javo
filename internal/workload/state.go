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

	// Fingerprint is what this instance was built from.
	Fingerprint Fingerprint
	// Stale is set for an instance built in a way the executor has since
	// given up: it must be built again whatever its Fingerprint says.
	Stale bool
	// Running is set once the platform has started the workload, whether
	// or not it answers yet.
	Running bool
	// Commit is the commit of the code it runs, once it has fetched it;
	// empty for a workload that runs no code from Git.
	Commit string

	// Run is set for a run (Spec.Batch), which has no Phase or Step: Code
	// and Evidence are then the problem its newest attempt has while the
	// run is not done.
	Run *RunState
}

// RunState is what an executor saw of one run.
type RunState struct {
	// Handle is the name the platform knows the run by.
	Handle string
	// Manual is set when a person started the run, and Slot is the time a
	// schedule started it for; zero when none did.
	Manual bool
	Slot   time.Time
	// Env are the plain variables the run was started with.
	Env map[string]string

	// Started is when the run began; zero when it has not.
	Started time.Time
	// Done is set once the run can no longer change, with Finished the
	// time and Succeeded whether it ended well.
	Done, Succeeded bool
	Finished        time.Time
	// TimedOut is set when the run was stopped at its time limit, which
	// is Timeout (zero when the platform did not say). Waited is how much
	// of that time passed before an attempt was given a machine.
	TimedOut bool
	Timeout  time.Duration
	Waited   time.Duration

	// Attempts is how many times the run was tried and can still be seen.
	// OutOfMemory and Message are about the newest: whether it was stopped
	// for using too much memory, and what its runner left behind.
	Attempts    int
	OutOfMemory bool
	Message     string
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

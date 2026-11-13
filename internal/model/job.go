package model

import (
	"errors"
	"regexp"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/jobs/schedule"
)

// Job phases.
const (
	JobPhaseReady   = "Ready"
	JobPhasePaused  = "Paused"
	JobPhaseInvalid = "Invalid"
	JobPhaseBlocked = "Blocked"
)

// Job condition types (ConditionScheduled is shared with App).
const (
	ConditionValid = "Valid"
	// ConditionNotified is False when a failure notice for a run could not be sent.
	ConditionNotified = "Notified"
)

// BackupTemplate is the built-in template of the backup and restore jobs:
// their runs follow a full backup or restore that the
// platform does.
const BackupTemplate = "backup"

// Run outcomes.
const (
	RunOutcomeRunning   = "Running"
	RunOutcomeSucceeded = "Succeeded"
	RunOutcomeFailed    = "Failed"
	RunOutcomeTimedOut  = "TimedOut"
	RunOutcomeSkipped   = "Skipped"
	RunOutcomeMissed    = "Missed"
	RunOutcomeCancelled = "Cancelled"
	RunOutcomeLost      = "Lost"
)

// JobSource names the notebook or script in the owner's home.
// +kubebuilder:object:generate=true
type JobSource struct {
	// Path is relative to the owner's home. Empty only for engine command,
	// which may run without a file.
	Path string `json:"path,omitempty"`
}

// JobParameter is one named input, passed to the engine as a typed value.
// +kubebuilder:object:generate=true
type JobParameter struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Default is the value as text; it must parse as Type.
	Default string `json:"default,omitempty"`
}

// EnvVar is an environment variable with a plain value: an
// app's container or a job's run gets it as is. A value from a secret is a
// SecretBinding instead.
// +kubebuilder:object:generate=true
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// JobRunPolicy bounds one run.
// +kubebuilder:object:generate=true
type JobRunPolicy struct {
	Timeout *metav1.Duration `json:"timeout,omitempty"`
	// Retries applies to infrastructure failures only; a notebook error
	// fails the run at once.
	Retries     int32            `json:"retries,omitempty"`
	CellTimeout *metav1.Duration `json:"cellTimeout,omitempty"`
}

// JobSchedule is a cron schedule in an explicit IANA time zone.
// +kubebuilder:object:generate=true
type JobSchedule struct {
	Cron             string           `json:"cron"`
	TimeZone         string           `json:"timeZone"`
	Concurrency      string           `json:"concurrency,omitempty"`
	StartingDeadline *metav1.Duration `json:"startingDeadline,omitempty"`
	Paused           bool             `json:"paused,omitempty"`
}

// JobHistory says how many finished runs to keep.
// +kubebuilder:object:generate=true
type JobHistory struct {
	KeepRuns int32 `json:"keepRuns,omitempty"`
	// Pointers so that an explicit 0 is kept instead of replaced by the default.
	KeepSucceeded *int32 `json:"keepSucceeded,omitempty"`
	KeepFailed    *int32 `json:"keepFailed,omitempty"`
}

// JobNotify says when to tell the owner about a run.
// +kubebuilder:object:generate=true
type JobNotify struct {
	// A pointer so that an explicit false is kept instead of replaced by the default.
	OnFailure *bool `json:"onFailure,omitempty"`
	OnSuccess bool  `json:"onSuccess,omitempty"`
}

// JobSpec is what a person writes for a job: the notebook or script to run,
// where, and when. The owner is on Job, not here.
type JobSpec struct {
	DisplayName string `json:"displayName,omitempty"`

	// Template is the name of the template whose image runs the job.
	Template string `json:"template"`

	// Engine is papermill, marimo, quarto, command or bash.
	Engine string `json:"engine"`

	Source JobSource `json:"source"`

	// Command is the argv for engine command, or the one script for engine
	// bash.
	Command []string `json:"command,omitempty"`

	// Kernel names the Jupyter kernel, for engine papermill.
	Kernel string `json:"kernel,omitempty"`

	Parameters []JobParameter `json:"parameters,omitempty"`

	Env []EnvVar `json:"env,omitempty"`

	// Environment is the pixi environment each run gets; nil runs the
	// template image's own tools. It is set when the job is created.
	Environment *Environment `json:"environment,omitempty"`

	Run JobRunPolicy `json:"run,omitempty"`

	// Schedule is nil for a job that only runs on demand.
	Schedule *JobSchedule `json:"schedule,omitempty"`

	History JobHistory `json:"history,omitempty"`
	Notify  JobNotify  `json:"notify,omitempty"`
}

// JobStatus is the observed state of a job. The controller writes it; the
// gateway never does.
type JobStatus struct {
	Phase            string             `json:"phase,omitempty"`
	Conditions       []metav1.Condition `json:"conditions,omitempty"`
	NextRunTimes     []time.Time        `json:"nextRunTimes,omitempty"`
	LastScheduleTime time.Time          `json:"lastScheduleTime,omitzero"`
	// ObservedVersion is the Version of the spec the controller last acted on.
	// It has its own column, so it is not part of the stored JSON.
	ObservedVersion int64 `json:"-"`
}

// Job is one row of the jobs table. The zero time means "not set".
type Job struct {
	ID        string
	Namespace string
	Name      string
	Owner     string
	Spec      JobSpec
	// Size names one of the admin-defined sizes each run gets; empty means
	// the default. Resources is a custom CPU and memory instead, for people
	// the admin allows; nil runs at Size. A change applies from the next run.
	Size      string
	Resources *AppResources
	Version   int64
	// CreatedAt orders lists newest first.
	CreatedAt time.Time
	// DeletedAt is set when the owner deletes the job; the controller removes
	// its objects and the row is then purged.
	DeletedAt time.Time
	Status    JobStatus
}

// Run is the record of one run, kept after its Kubernetes Job is gone.
type Run struct {
	ID    string
	JobID string
	// Handle is the name the executor knows the run by, and of its run
	// folder; empty until the controller has started it.
	Handle string
	// Slot is the time of the schedule the run was started for: a job has
	// one run per slot. Zero for a run a person started, and for runs
	// recorded before the controller started them itself.
	Slot time.Time
	// Trigger is schedule or manual.
	Trigger string
	// RequestedAt is when a person asked for a manual run. A run with no
	// Outcome has been requested and not yet started.
	RequestedAt   time.Time
	ScheduledTime time.Time
	StartTime     time.Time
	FinishTime    time.Time
	Outcome       string
	Reason        string
	Message       string
	// OutputPath is the run folder, relative to the home folder.
	OutputPath string
	// Outputs are the files the engine made, relative to OutputPath.
	Outputs    []string
	Parameters map[string]string
	// NotifiedAt is when the owner was told about this run; zero if not yet.
	NotifiedAt time.Time
	// CancelRequested is set by the gateway; the controller acts on it.
	CancelRequested bool
}

var (
	paramName   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	integerText = regexp.MustCompile(`^-?[0-9]+$`)
	numberText  = regexp.MustCompile(`^-?[0-9]+([.][0-9]+)?$`)
)

// ApplyDefaults fills what an unset field means. A pointer field that is set,
// even to 0 or false, is kept.
func (j *Job) ApplyDefaults() {
	s := &j.Spec
	if s.Schedule != nil && s.Schedule.Concurrency == "" {
		s.Schedule.Concurrency = "Forbid"
	}
	if s.History.KeepRuns == 0 {
		s.History.KeepRuns = 20
	}
	if s.History.KeepSucceeded == nil {
		s.History.KeepSucceeded = ptr(int32(3))
	}
	if s.History.KeepFailed == nil {
		s.History.KeepFailed = ptr(int32(3))
	}
	if s.Notify.OnFailure == nil {
		s.Notify.OnFailure = ptr(true)
	}
}

func ptr[T any](v T) *T { return &v }

// Validate checks the rules every saved job satisfies.
func (j *Job) Validate() error {
	var v validator
	v.name(j.Name)
	v.owner("spec.owner", j.Owner)
	v.size(j.Size, j.Resources)
	return v.merge(j.Spec.Validate())
}

// ValidateUpdate is Validate plus the rules for changing old into j: the
// owner never changes.
func (j *Job) ValidateUpdate(old *Job) error {
	var v validator
	v.immutable("spec.owner", "owner is immutable", j.Owner, old.Owner)
	return v.merge(j.Validate())
}

// RunsCommand says whether the job runs typed text (engine command or bash)
// instead of a file.
func (s *JobSpec) RunsCommand() bool { return s.Engine == "command" || s.Engine == "bash" }

// Validate checks the job's spec. The schedule is read with the parser
// Kubernetes uses for CronJobs.
func (s *JobSpec) Validate() error {
	var v validator
	if s.Template == "" {
		v.add("spec.template", "template is required")
	}
	v.oneOf("spec.engine", s.Engine, false, jobEngines...)
	switch {
	case s.Engine == "bash":
		if len(s.Command) != 1 {
			v.add("spec.command", "engine bash takes one script")
		}
	case (s.Engine == "command") != (len(s.Command) > 0):
		v.add("spec.command", "command is required for engine command and not allowed otherwise")
	}
	v.maxItems("spec.command", len(s.Command), 64)
	if s.Kernel != "" && s.Engine != "papermill" {
		v.add("spec.kernel", "kernel is only for engine papermill")
	}
	if !s.RunsCommand() && s.Source.Path == "" {
		v.add("spec.source.path", "source.path is required for every engine but command")
	}
	v.maxLen("spec.source.path", s.Source.Path, 4096)
	validateParameters(&v, "spec.parameters", s.Parameters)
	for i, e := range s.Env {
		if e.Name == "" {
			v.add(indexed("spec.env", i)+".name", "name is required")
		}
	}
	if s.Run.Retries < 0 || s.Run.Retries > 5 {
		v.add("spec.run.retries", "retries must be between 0 and 5")
	}
	if sc := s.Schedule; sc != nil {
		sc.validate(&v, "spec.schedule")
	}
	// Callers run ApplyDefaults before Validate, as CRD defaulting did, so an
	// unset keepRuns is 20 here and 0 means a write skipped the defaults.
	h := s.History
	if h.KeepRuns < 1 || h.KeepRuns > 200 {
		v.add("spec.history.keepRuns", "keepRuns must be between 1 and 200")
	}
	for field, n := range map[string]*int32{"keepSucceeded": h.KeepSucceeded, "keepFailed": h.KeepFailed} {
		if n != nil && (*n < 0 || *n > 20) {
			v.add("spec.history."+field, field+" must be between 0 and 20")
		}
	}
	s.Environment.validate(&v, "spec.environment")
	return v.err()
}

func (sc *JobSchedule) validate(v *validator, field string) {
	v.oneOf(field+".concurrency", sc.Concurrency, true, "Allow", "Forbid", "Replace")
	if sc.Cron == "" {
		v.add(field+".cron", "cron is required")
	}
	if sc.TimeZone == "" {
		v.add(field+".timeZone", "timeZone is required")
	}
	if sc.Cron == "" || sc.TimeZone == "" {
		return
	}
	switch err := schedule.Validate(sc.Cron, sc.TimeZone); {
	case errors.Is(err, schedule.ErrCronInvalid):
		v.add(field+".cron", err.Error())
	case errors.Is(err, schedule.ErrTimeZoneInvalid):
		v.add(field+".timeZone", err.Error())
	}
}

func validateParameters(v *validator, field string, params []JobParameter) {
	v.maxItems(field, len(params), 32)
	for i, p := range params {
		f := indexed(field, i)
		if !paramName.MatchString(p.Name) {
			v.add(f+".name", "name must start with a letter or underscore and have at most 64 letters, digits and underscores")
		}
		v.oneOf(f+".type", p.Type, false, "string", "integer", "number", "boolean")
		v.maxLen(f+".default", p.Default, 1024)
		switch {
		case p.Default == "":
		case p.Type == "integer" && !integerText.MatchString(p.Default):
			v.add(f+".default", "default must be an integer")
		case p.Type == "number" && !numberText.MatchString(p.Default):
			v.add(f+".default", "default must be a number")
		case p.Type == "boolean" && p.Default != "true" && p.Default != "false":
			v.add(f+".default", "default must be true or false")
		}
	}
}

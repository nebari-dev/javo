package jobs

import (
	"cmp"
	"path"
	"slices"
	"time"

	"k8s.io/utils/ptr"

	"github.com/nebari-dev/javo/internal/cmdline"
	"github.com/nebari-dev/javo/internal/gateway/environments"
	"github.com/nebari-dev/javo/internal/gateway/envvars"
	"github.com/nebari-dev/javo/internal/gateway/sizing"
	"github.com/nebari-dev/javo/internal/gateway/templates"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// maxStrip is how many outcomes the run strip of a job shows.
const maxStrip = 12

// Problem is a failure explained from the reason catalog.
type Problem struct {
	Reason   string   `json:"reason"`
	Title    string   `json:"title"`
	What     string   `json:"what"`
	Todo     string   `json:"todo"`
	Blame    string   `json:"blame"`
	Who      string   `json:"who"`
	Evidence []string `json:"evidence"`
}

// problemFor renders a catalog code as a Problem; evidence is the detail the
// cluster gave, shown as plain text.
func problemFor(code string, evidence ...string) *Problem {
	r, _ := reasons.Lookup(reasons.Code(code))
	p := &Problem{Reason: code, Title: r.Title, What: r.What, Todo: r.Todo, Blame: r.Blame, Who: r.Who, Evidence: []string{}}
	for _, e := range evidence {
		if e != "" {
			p.Evidence = append(p.Evidence, e)
		}
	}
	return p
}

// ScheduleView is a job's schedule; times are shown with its IANA zone.
type ScheduleView struct {
	Cron             string `json:"cron"`
	TimeZone         string `json:"timeZone"`
	Concurrency      string `json:"concurrency" enum:"Allow,Forbid,Replace"`
	StartingDeadline string `json:"startingDeadline,omitempty" doc:"Duration such as 15m; a run later than this after its time is dropped"`
	Paused           bool   `json:"paused"`
}

// RunView is one run of a job.
type RunView struct {
	ID            string            `json:"id"`
	Trigger       string            `json:"trigger" enum:"schedule,manual"`
	ScheduledTime *time.Time        `json:"scheduledTime,omitempty"`
	StartTime     *time.Time        `json:"startTime,omitempty"`
	FinishTime    *time.Time        `json:"finishTime,omitempty"`
	Outcome       string            `json:"outcome"`
	Reason        string            `json:"reason,omitempty"`
	Title         string            `json:"title,omitempty"`
	Message       string            `json:"message,omitempty" doc:"Plain text from the run; never markup"`
	OutputPath    string            `json:"outputPath,omitempty" doc:"The run folder, relative to the home folder"`
	Outputs       []string          `json:"outputs" doc:"Result files relative to the home folder, HTML first"`
	Parameters    map[string]string `json:"parameters,omitempty" doc:"The inputs this run was started with"`
}

// JobView is a job as listed.
type JobView struct {
	Name        string        `json:"name"`
	DisplayName string        `json:"displayName"`
	Engine      string        `json:"engine"`
	Source      string        `json:"source" doc:"Path relative to the home folder; empty for a command job"`
	Command     string        `json:"command,omitempty" doc:"The command line of a command job"`
	Template    string        `json:"template"`
	Schedule    *ScheduleView `json:"schedule,omitempty"`
	NextRuns    []time.Time   `json:"nextRuns"`
	Phase       string        `json:"phase"`
	LastRun     *RunView      `json:"lastRun,omitempty"`
	Recent      []string      `json:"recent" doc:"Outcomes of the latest runs, oldest first"`
	Problem     *Problem      `json:"problem,omitempty"`
}

// JobRunBody bounds one run. Durations are Go durations such as 90m.
type JobRunBody struct {
	Timeout     string `json:"timeout,omitempty"`
	Retries     int32  `json:"retries,omitempty" minimum:"0" maximum:"5"`
	CellTimeout string `json:"cellTimeout,omitempty"`
}

// JobNotifyBody says when to tell the owner.
type JobNotifyBody struct {
	OnFailure *bool `json:"onFailure,omitempty" doc:"Defaults to true"`
	OnSuccess bool  `json:"onSuccess,omitempty"`
}

// JobScheduleBody is the schedule a form sends.
type JobScheduleBody struct {
	Cron             string `json:"cron" maxLength:"256"`
	TimeZone         string `json:"timeZone" maxLength:"64"`
	Concurrency      string `json:"concurrency,omitempty" enum:"Allow,Forbid,Replace" doc:"Defaults to Forbid"`
	StartingDeadline string `json:"startingDeadline,omitempty"`
	Paused           bool   `json:"paused,omitempty"`
}

// JobDetail is one job with its runs and settings.
type JobDetail struct {
	JobView
	Kernel     string               `json:"kernel,omitempty"`
	Parameters []templates.JobParam `json:"parameters"`
	Run        JobRunBody           `json:"run"`
	Notify     JobNotifyBody        `json:"notify"`
	Runs       []RunView            `json:"runs" doc:"Newest first"`
	// Env are the variables each run gets: plain values as stored, secrets
	// by name, never their values.
	Env []envvars.EnvVar `json:"env"`
	// Size is the name of the size each run gets; empty when no sizes are
	// configured or the job has a custom amount (Resources).
	Size      string         `json:"size"`
	Resources *sizing.Amount `json:"resources,omitempty" doc:"The custom amount each run gets instead of a size"`
	// Environment is the pixi environment each run gets; absent for none.
	Environment *environments.EnvironmentView `json:"environment,omitempty"`
	// ResumeNotice is set while the schedule is paused and resuming would start a missed run.
	ResumeNotice string `json:"resumeNotice,omitempty" doc:"What resuming a paused schedule will do, when it starts a missed run"`
}

// failedOutcomes are the run outcomes that put a job in "needs your attention".
var failedOutcomes = []string{
	model.RunOutcomeFailed, model.RunOutcomeTimedOut, model.RunOutcomeLost, model.RunOutcomeMissed,
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// outcomeOf is a run's outcome as shown: a run that was requested and not yet
// started by the controller has none stored, and shows as running.
func outcomeOf(r model.Run) string {
	if r.Outcome == "" {
		return model.RunOutcomeRunning
	}
	return r.Outcome
}

func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return utc(&t)
}

func runView(r model.Run) RunView {
	v := RunView{
		ID: r.ID, Trigger: r.Trigger, Outcome: outcomeOf(r), Reason: r.Reason, Message: r.Message,
		OutputPath: r.OutputPath, Outputs: []string{}, Parameters: r.Parameters,
		ScheduledTime: timeOrNil(r.ScheduledTime), StartTime: timeOrNil(r.StartTime), FinishTime: timeOrNil(r.FinishTime),
	}
	if r.Reason != "" {
		rs, _ := reasons.Lookup(reasons.Code(r.Reason))
		v.Title = rs.Title
	}
	// Outputs are files in the run folder, the HTML first: it opens in the
	// browser, while a notebook downloads.
	if r.OutputPath != "" {
		for _, o := range r.Outputs {
			v.Outputs = append(v.Outputs, path.Join(r.OutputPath, o))
		}
		notHTML := func(p string) int {
			if path.Ext(p) == ".html" {
				return 0
			}
			return 1
		}
		slices.SortStableFunc(v.Outputs, func(a, b string) int { return cmp.Compare(notHTML(a), notHTML(b)) })
	}
	return v
}

func scheduleView(s *model.JobSchedule) *ScheduleView {
	if s == nil {
		return nil
	}
	v := &ScheduleView{Cron: s.Cron, TimeZone: s.TimeZone, Concurrency: s.Concurrency, Paused: s.Paused}
	if v.Concurrency == "" {
		v.Concurrency = "Forbid"
	}
	if s.StartingDeadline != nil {
		v.StartingDeadline = s.StartingDeadline.Duration.String()
	}
	return v
}

// problemOf says what is wrong with the job, or nil: a definition that
// cannot run, else the failure of its latest run.
func problemOf(j *model.Job, last *RunView) *Problem {
	switch j.Status.Phase {
	case model.JobPhaseInvalid, model.JobPhaseBlocked:
		for _, c := range j.Status.Conditions {
			if c.Type == model.ConditionValid && c.Status == "False" {
				return problemFor(c.Reason)
			}
		}
	}
	if last != nil && slices.Contains(failedOutcomes, last.Outcome) && last.Reason != "" {
		return problemFor(last.Reason, last.Message)
	}
	return nil
}

// jobView is a job as listed; runs are its runs, newest first.
func jobView(j *model.Job, runs []model.Run) JobView {
	phase := j.Status.Phase
	if phase == "" {
		phase = "Pending"
	}
	v := JobView{
		Name: j.Name, DisplayName: j.Spec.DisplayName, Engine: j.Spec.Engine,
		Source: j.Spec.Source.Path, Template: j.Spec.Template, Schedule: scheduleView(j.Spec.Schedule),
		NextRuns: []time.Time{}, Phase: phase, Recent: []string{},
	}
	// Words that cannot be quoted (invalid UTF-8) only come from a
	// definition written by hand; the job then shows no command line.
	if j.Spec.Engine == bashEngine && len(j.Spec.Command) == 1 {
		v.Command = j.Spec.Command[0] // the script, as typed
	} else {
		v.Command, _ = cmdline.Join(j.Spec.Command)
	}
	for _, t := range j.Status.NextRunTimes {
		v.NextRuns = append(v.NextRuns, t.UTC())
	}
	if len(runs) > 0 {
		last := runView(runs[0])
		v.LastRun = &last
	}
	for i := min(len(runs), maxStrip) - 1; i >= 0; i-- {
		v.Recent = append(v.Recent, outcomeOf(runs[i]))
	}
	v.Problem = problemOf(j, v.LastRun)
	return v
}

func jobDetail(j *model.Job, runs []model.Run) JobDetail {
	out := JobDetail{
		JobView: jobView(j, runs), Kernel: j.Spec.Kernel, Parameters: []templates.JobParam{},
		Run:    JobRunBody{Retries: j.Spec.Run.Retries},
		Notify: JobNotifyBody{OnFailure: new(ptr.Deref(j.Spec.Notify.OnFailure, true)), OnSuccess: j.Spec.Notify.OnSuccess},
		Runs:   []RunView{}, Env: []envvars.EnvVar{},
	}
	if t := j.Spec.Run.Timeout; t != nil {
		out.Run.Timeout = t.Duration.String()
	}
	if t := j.Spec.Run.CellTimeout; t != nil {
		out.Run.CellTimeout = t.Duration.String()
	}
	for _, p := range j.Spec.Parameters {
		out.Parameters = append(out.Parameters, templates.JobParam{Name: p.Name, Type: p.Type, Default: p.Default})
	}
	for _, r := range runs {
		out.Runs = append(out.Runs, runView(r))
	}
	if sc := j.Spec.Schedule; sc != nil && sc.Paused && missedWhilePaused(j, time.Now()) {
		out.ResumeNotice = reasons.Msg(reasons.MsgResumeStartsMissedRun)
	}
	return out
}

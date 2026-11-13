package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// Run triggers.
const (
	TriggerManual   = "manual"
	TriggerSchedule = "schedule"
)

// Jobs is the jobs and job_runs tables. As with apps, the gateway writes
// spec columns and requests (RequestRun, RequestCancel); the controller
// writes status columns and the record of each run (SetStatus, RecordRun).
type Jobs struct{ s *Store }

// Jobs returns the jobs table.
func (s *Store) Jobs() *Jobs { return &Jobs{s} }

var jobKind = rowKind[model.Job]{
	kind: "job", table: "jobs", read: readJob,
	info: func(j model.Job) rowInfo {
		return rowInfo{ID: j.ID, Namespace: j.Namespace, Name: j.Name, Owner: j.Owner, Version: j.Version, Deleted: !j.DeletedAt.IsZero()}
	},
	prepare:  func(j *model.Job) { j.ApplyDefaults() },
	validate: func(next, old *model.Job) error { return next.ValidateUpdate(old) },
	writeSpec: func(ctx context.Context, q *sqlcgen.Queries, id string, version int64, next *model.Job) (int64, error) {
		spec, err := marshal(next.Spec, "{}")
		if err != nil {
			return 0, err
		}
		cpu, mem := resourceColumns(next.Resources)
		return q.UpdateJobSpec(ctx, sqlcgen.UpdateJobSpecParams{
			Template: next.Spec.Template, Paused: paused(next.Spec), Spec: spec,
			Size: next.Size, CpuMillis: cpu, MemoryBytes: mem, ID: id, Version: version,
		})
	},
	deleteRow: func(ctx context.Context, q *sqlcgen.Queries, id string, at time.Time) (int64, error) {
		return q.MarkJobDeleted(ctx, sqlcgen.MarkJobDeletedParams{DeletedAt: nullTime(at), ID: id})
	},
	purgeRow: func(ctx context.Context, q *sqlcgen.Queries, id string) (int64, error) { return q.PurgeJob(ctx, id) },
	writeStatus: func(ctx context.Context, q *sqlcgen.Queries, id string, observed int64, status string, at time.Time) (int64, error) {
		return q.SetJobStatus(ctx, sqlcgen.SetJobStatusParams{Status: status, ObservedVersion: observed, StatusAt: nullTime(at), ID: id})
	},
}

func paused(s model.JobSpec) bool { return s.Schedule != nil && s.Schedule.Paused }

// Create stores a new job at version 1; a taken (owner, name) is ErrExists.
func (j *Jobs) Create(ctx context.Context, actor model.Actor, in *model.Job) (model.Job, error) {
	return j.CreateWith(ctx, actor, in, JobExtras{})
}

// JobExtras is what CreateWith stores with a new job: the person's secrets
// its runs get, by variable name, and the files of the environment it names
// (nil when it names none).
type JobExtras struct {
	Secrets     []model.SecretBinding
	Environment *model.EnvironmentFiles
}

// CreateWith is Create plus extras, in the same transaction: a binding that
// names no secret of the owner (*SecretMissingError) refuses the job and
// leaves nothing behind.
func (j *Jobs) CreateWith(ctx context.Context, actor model.Actor, in *model.Job, extras JobExtras) (model.Job, error) {
	if err := checkEnvironment(in.Spec.Environment, extras.Environment); err != nil {
		return model.Job{}, err
	}
	job := *in
	job.ApplyDefaults()
	if err := job.Validate(); err != nil {
		return model.Job{}, err
	}
	spec, err := marshal(job.Spec, "{}")
	if err != nil {
		return model.Job{}, err
	}
	id := newID()
	var out model.Job
	err = j.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		if err := within(ctx, q, job.Owner, j.s.jobCap(ctx, q, job.Owner)); err != nil {
			return effect{}, err
		}
		if err := insertJob(ctx, q, id, job, spec, now()); err != nil {
			return effect{}, err
		}
		if err := writeJobEnvironment(ctx, q, id, extras.Environment); err != nil {
			return effect{}, err
		}
		if err := writeJobBindings(ctx, q, id, job.Owner, extras.Secrets); err != nil {
			return effect{}, err
		}
		var err error
		if out, err = readJob(ctx, q, id); err != nil {
			return effect{}, err
		}
		return jobKind.effect("job.create", out), nil
	})
	return out, err
}

// insertJob writes a new job row at version 1; a taken (owner, name) is
// ErrExists. Create and Import share it.
func insertJob(ctx context.Context, q *sqlcgen.Queries, id string, job model.Job, spec string, createdAt time.Time) error {
	cpu, mem := resourceColumns(job.Resources)
	n, err := q.InsertJob(ctx, sqlcgen.InsertJobParams{
		ID: id, Namespace: job.Namespace, Name: job.Name, Owner: job.Owner, Template: job.Spec.Template,
		Paused: paused(job.Spec), Spec: spec, CreatedAt: dbtime.From(createdAt),
		Size: job.Size, CpuMillis: cpu, MemoryBytes: mem,
	})
	if err == nil && n == 0 {
		err = ErrExists
	}
	return err
}

// Get returns the job (also one marked deleted) or ErrNotFound.
func (j *Jobs) Get(ctx context.Context, id string) (model.Job, error) {
	return readJob(ctx, j.s.q, id)
}

func readJob(ctx context.Context, q *sqlcgen.Queries, id string) (model.Job, error) {
	row, err := q.GetJob(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, ErrNotFound
	}
	if err != nil {
		return model.Job{}, err
	}
	return jobOf(row)
}

// GetByOwnerName returns the person's job with this short name (also one
// marked deleted), or ErrNotFound.
func (j *Jobs) GetByOwnerName(ctx context.Context, owner, name string) (model.Job, error) {
	row, err := j.s.q.GetJobByOwnerName(ctx, sqlcgen.GetJobByOwnerNameParams{Owner: owner, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, ErrNotFound
	}
	if err != nil {
		return model.Job{}, err
	}
	return jobOf(row)
}

// List returns the person's jobs, newest first, without deleted ones.
func (j *Jobs) List(ctx context.Context, owner string) ([]model.Job, error) {
	rows, err := j.s.q.ListJobsByOwner(ctx, owner)
	if err != nil {
		return nil, err
	}
	return jobsOf(rows)
}

// ListAll returns every job, deleted ones included: the controller's resync.
func (j *Jobs) ListAll(ctx context.Context) ([]model.Job, error) {
	rows, err := j.s.q.ListAllJobs(ctx)
	if err != nil {
		return nil, err
	}
	return jobsOf(rows)
}

// UpdateSpec changes a job's spec with the same rules as Apps.UpdateSpec.
func (j *Jobs) UpdateSpec(ctx context.Context, actor model.Actor, id string, version int64, fn func(*model.Job) error) (model.Job, error) {
	return jobKind.update(ctx, j.s, actor, id, version, fn)
}

// MarkDeleted records that the owner deleted the job; the controller removes
// its CronJob and Jobs, then calls Purge.
func (j *Jobs) MarkDeleted(ctx context.Context, actor model.Actor, id string) error {
	return jobKind.markDeleted(ctx, j.s, actor, id, nil)
}

// Purge removes a job marked deleted, with its runs.
func (j *Jobs) Purge(ctx context.Context, actor model.Actor, id string) error {
	return jobKind.purge(ctx, j.s, actor, id)
}

// SetStatus records what the controller observed; see Apps.SetStatus. The
// Notified condition belongs to the notifier: the stored one replaces any in
// st, in the same transaction, so a status built from an older read of the
// row cannot drop it.
func (j *Jobs) SetStatus(ctx context.Context, id string, observedVersion int64, st model.JobStatus) error {
	return j.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		job, err := readJob(ctx, q, id)
		if err != nil {
			return err
		}
		st.Conditions = withoutNotified(st.Conditions)
		if kept := meta.FindStatusCondition(job.Status.Conditions, model.ConditionNotified); kept != nil {
			st.Conditions = append(st.Conditions, *kept)
		}
		return writeJobStatus(ctx, q, id, observedVersion, st)
	})
}

// SetCondition sets one condition on the stored status, leaving the rest of
// the status as it is. It reads, merges and writes in one transaction, so it
// cannot bring back an older phase written by the controller meanwhile.
func (j *Jobs) SetCondition(ctx context.Context, id string, cond metav1.Condition) error {
	return j.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		job, err := readJob(ctx, q, id)
		if err != nil {
			return err
		}
		st := job.Status
		st.Conditions = append([]metav1.Condition(nil), st.Conditions...)
		meta.SetStatusCondition(&st.Conditions, cond)
		return writeJobStatus(ctx, q, id, st.ObservedVersion, st)
	})
}

func withoutNotified(conds []metav1.Condition) []metav1.Condition {
	return slices.DeleteFunc(slices.Clone(conds), func(c metav1.Condition) bool { return c.Type == model.ConditionNotified })
}

func writeJobStatus(ctx context.Context, q *sqlcgen.Queries, id string, observed int64, st model.JobStatus) error {
	text, err := marshal(st, "{}")
	if err != nil {
		return err
	}
	n, err := jobKind.writeStatus(ctx, q, id, observed, text, now())
	if err == nil && n == 0 {
		err = ErrNotFound
	}
	return err
}

// RequestRun adds a manual run for the controller to start. Until it does,
// the run has an empty Outcome.
func (j *Jobs) RequestRun(ctx context.Context, actor model.Actor, jobID string, params map[string]string) (model.Run, error) {
	run := model.Run{ID: newID(), JobID: jobID, Trigger: TriggerManual, RequestedAt: now(), Parameters: params}
	var out model.Run
	err := j.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		job, err := readJob(ctx, q, jobID)
		if err != nil {
			return effect{}, err
		}
		if !job.DeletedAt.IsZero() {
			return effect{}, ErrNotFound
		}
		if err := within(ctx, q, job.Owner, j.s.runCaps(ctx, q, job.Owner, job.ID)...); err != nil {
			return effect{}, err
		}
		if err := insertRun(ctx, q, run); err != nil {
			return effect{}, err
		}
		if out, err = readRun(ctx, q, run.ID); err != nil {
			return effect{}, err
		}
		return jobKind.effect("job.run", job, Change{"job_runs", run.ID}), nil
	})
	return out, err
}

// RequestCancel asks the controller to cancel a run. A run that already
// finished is ErrConflict.
func (j *Jobs) RequestCancel(ctx context.Context, actor model.Actor, runID string) error {
	return j.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		run, err := readRun(ctx, q, runID)
		if err != nil {
			return effect{}, err
		}
		job, err := readJob(ctx, q, run.JobID)
		if err != nil {
			return effect{}, err
		}
		n, err := q.RequestRunCancel(ctx, runID)
		if err == nil && n == 0 {
			err = ErrConflict
		}
		return jobKind.effect("job.cancel", job, Change{"job_runs", runID}), err
	})
}

// RecordRun is the controller's record of a run. With an ID it updates what
// the controller reports (handle, times, outcome, reason, outputs)
// and leaves a pending cancel request, the notified time, and any time,
// output path or outputs the report leaves empty alone, or returns
// ErrNotFound; without an ID it adds the run (a scheduled one), unless the
// job already has a run with the same handle, which is updated
// instead, so recording a Job again after a resync or retry keeps one run.
// It is neither audited nor announced.
func (j *Jobs) RecordRun(ctx context.Context, run model.Run) (model.Run, error) {
	var out model.Run
	err := j.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		added := false
		if run.ID == "" {
			var err error
			if added, err = addRun(ctx, q, &run); err != nil {
				return err
			}
		}
		if !added {
			stored, err := readRun(ctx, q, run.ID)
			if err != nil {
				return err
			}
			mergeRun(&run, stored)
			outputs, err := marshal(run.Outputs, "[]")
			if err != nil {
				return err
			}
			n, err := q.UpdateRun(ctx, sqlcgen.UpdateRunParams{
				Handle: run.Handle, StartedAt: nullTime(run.StartTime), FinishedAt: nullTime(run.FinishTime),
				Outcome: run.Outcome, Reason: run.Reason, Message: run.Message, OutputPath: run.OutputPath,
				Outputs: outputs, ID: run.ID,
			})
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrNotFound
			}
		}
		var err error
		out, err = readRun(ctx, q, run.ID)
		return err
	})
	return out, err
}

// mergeRun fills what a report leaves empty (handle, times, output
// path, outputs) from the stored run, so a Job recorded again from the Job
// object alone does not wipe what an earlier report stored.
func mergeRun(run *model.Run, stored model.Run) {
	if run.Handle == "" {
		run.Handle = stored.Handle
	}
	if run.StartTime.IsZero() {
		run.StartTime = stored.StartTime
	}
	if run.FinishTime.IsZero() {
		run.FinishTime = stored.FinishTime
	}
	if run.OutputPath == "" {
		run.OutputPath = stored.OutputPath
	}
	if len(run.Outputs) == 0 {
		run.Outputs = stored.Outputs
	}
}

// MarkRunNotified records that the owner was told about the run. Only this
// sets the time; RecordRun never touches it, so recording a run again cannot
// make the controller notify twice. A missing run is ErrNotFound.
func (j *Jobs) MarkRunNotified(ctx context.Context, runID string, at time.Time) error {
	return j.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.MarkRunNotified(ctx, sqlcgen.MarkRunNotifiedParams{NotifiedAt: nullTime(at), ID: runID})
		if err == nil && n == 0 {
			err = ErrNotFound
		}
		return err
	})
}

// UnnotifiedFailedRuns returns the finished runs that failed (Failed, TimedOut
// or Lost) and whose owner has not been told yet, oldest first.
func (j *Jobs) UnnotifiedFailedRuns(ctx context.Context) ([]model.Run, error) {
	rows, err := j.s.q.ListUnnotifiedRuns(ctx, sqlcgen.ListUnnotifiedRunsParams{
		Outcome: model.RunOutcomeFailed, Outcome_2: model.RunOutcomeTimedOut, Outcome_3: model.RunOutcomeLost,
	})
	if err != nil {
		return nil, err
	}
	return runsOf(rows)
}

// addRun inserts run under a new id. When its job already has a run with
// the same handle, nothing is inserted: run.ID becomes that run's id and
// added is false. When another run holds its slot, it is added without
// one: the run exists, whoever started it.
func addRun(ctx context.Context, q *sqlcgen.Queries, run *model.Run) (added bool, err error) {
	run.ID = newID()
	err = insertRun(ctx, q, *run)
	if !errors.Is(err, ErrExists) || run.Handle == "" {
		return err == nil, err
	}
	existing, err := q.GetRunByHandle(ctx, sqlcgen.GetRunByHandleParams{JobID: run.JobID, Handle: run.Handle})
	if errors.Is(err, sql.ErrNoRows) && !run.Slot.IsZero() {
		run.Slot = time.Time{}
		err = insertRun(ctx, q, *run)
		return err == nil, err
	}
	if err != nil {
		return false, err
	}
	run.ID = existing.ID
	return false, nil
}

// ClaimSlot adds run as the job's run for the slot run.Slot of its
// schedule, and reports true; when the job already has a run for that slot
// it adds nothing and returns that run and false. So a slot is decided
// once, whoever asks and however often. It is neither audited nor
// announced.
func (j *Jobs) ClaimSlot(ctx context.Context, run model.Run) (model.Run, bool, error) {
	if run.Slot.IsZero() {
		return model.Run{}, false, errors.New("store: claim a slot: the run has none")
	}
	var out model.Run
	claimed := false
	err := j.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		run.ID = newID()
		err := insertRun(ctx, q, run)
		claimed = err == nil
		if errors.Is(err, ErrExists) {
			var row sqlcgen.JobRun
			if row, err = q.GetRunBySlot(ctx, sqlcgen.GetRunBySlotParams{JobID: run.JobID, Slot: nullTime(run.Slot)}); err == nil {
				out, err = runOf(row)
			}
			return err
		}
		if err != nil {
			return err
		}
		out, err = readRun(ctx, q, run.ID)
		return err
	})
	return out, claimed, err
}

// Run returns one run or ErrNotFound.
func (j *Jobs) Run(ctx context.Context, id string) (model.Run, error) {
	return readRun(ctx, j.s.q, id)
}

// Runs returns the job's runs, newest first.
func (j *Jobs) Runs(ctx context.Context, jobID string) ([]model.Run, error) {
	rows, err := j.s.q.ListRunsByJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return runsOf(rows)
}

// PruneRuns deletes the job's finished runs beyond the newest keep and
// returns how many went. Newest is by when the run was scheduled or started
// (insert order breaks ties), not by when its row was written. Runs still
// going, runs whose handle is named in live (a Job or CronJob Event
// still listed, which the controller would record again as a new run) and
// other jobs' runs are kept.
// Deleting run history is audited; a prune that deletes nothing is not.
func (j *Jobs) PruneRuns(ctx context.Context, actor model.Actor, jobID string, keep int, live []string) (int64, error) {
	if keep < 0 {
		return 0, fmt.Errorf("store: prune runs: keep %d", keep)
	}
	var n int64
	err := j.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		n = 0
		rows, err := q.ListRunsByJob(ctx, jobID)
		if err != nil {
			return effect{}, err
		}
		runs, err := runsOf(rows)
		if err != nil {
			return effect{}, err
		}
		var finished []model.Run
		for _, r := range runs { // newest insert first, the tiebreak below
			if !r.FinishTime.IsZero() {
				finished = append(finished, r)
			}
		}
		slices.SortStableFunc(finished, func(a, b model.Run) int { return runTime(b).Compare(runTime(a)) })
		if len(finished) <= keep {
			return effect{}, nil
		}
		for _, r := range finished[keep:] {
			if r.Handle != "" && slices.Contains(live, r.Handle) {
				continue
			}
			deleted, err := q.DeleteRun(ctx, r.ID)
			if err != nil {
				return effect{}, err
			}
			n += deleted
		}
		if n == 0 {
			return effect{}, nil
		}
		job, err := readJob(ctx, q, jobID)
		if err != nil {
			return effect{}, err
		}
		// The runs are gone, so the change names the job: a job_runs change
		// is looked up by run id.
		eff := jobKind.effect("job.prune_runs", job)
		eff.audit.detail = map[string]int64{"removed": n, "keep": int64(keep)}
		return eff, nil
	})
	return n, err
}

// runTime is when the run was scheduled, or else started, requested or
// finished: the order a person reads history in.
func runTime(r model.Run) time.Time {
	for _, t := range []time.Time{r.ScheduledTime, r.StartTime, r.RequestedAt, r.FinishTime} {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func insertRun(ctx context.Context, q *sqlcgen.Queries, r model.Run) error {
	outputs, err := marshal(r.Outputs, "[]")
	if err != nil {
		return err
	}
	params, err := marshal(r.Parameters, "{}")
	if err != nil {
		return err
	}
	n, err := q.InsertRun(ctx, sqlcgen.InsertRunParams{
		ID: r.ID, JobID: r.JobID, Handle: r.Handle, Trigger: r.Trigger, CreatedAt: dbtime.From(now()),
		RequestedAt: nullTime(r.RequestedAt), ScheduledAt: nullTime(r.ScheduledTime),
		StartedAt: nullTime(r.StartTime), FinishedAt: nullTime(r.FinishTime), Outcome: r.Outcome,
		Reason: r.Reason, Message: r.Message, OutputPath: r.OutputPath, Outputs: outputs, Parameters: params,
		CancelRequested: r.CancelRequested, NotifiedAt: nullTime(r.NotifiedAt), Slot: nullTime(r.Slot),
	})
	if err == nil && n == 0 {
		err = ErrExists
	}
	return err
}

func readRun(ctx context.Context, q *sqlcgen.Queries, id string) (model.Run, error) {
	row, err := q.GetRun(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Run{}, ErrNotFound
	}
	if err != nil {
		return model.Run{}, err
	}
	return runOf(row)
}

func jobsOf(rows []sqlcgen.Job) ([]model.Job, error) {
	out := make([]model.Job, len(rows))
	for i, r := range rows {
		var err error
		if out[i], err = jobOf(r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func jobOf(r sqlcgen.Job) (model.Job, error) {
	job := model.Job{
		ID: r.ID, Namespace: r.Namespace, Name: r.Name, Owner: r.Owner, Version: r.Version,
		Size: r.Size, Resources: resourcesOf(r.CpuMillis, r.MemoryBytes),
		CreatedAt: r.CreatedAt.Time(), DeletedAt: timeOf(r.DeletedAt),
	}
	if err := unmarshal(r.Spec, &job.Spec); err != nil {
		return model.Job{}, err
	}
	if err := unmarshal(r.Status, &job.Status); err != nil {
		return model.Job{}, err
	}
	job.Status.ObservedVersion = r.ObservedVersion
	return job, nil
}

func runsOf(rows []sqlcgen.JobRun) ([]model.Run, error) {
	out := make([]model.Run, len(rows))
	for i, r := range rows {
		var err error
		if out[i], err = runOf(r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func runOf(r sqlcgen.JobRun) (model.Run, error) {
	run := model.Run{
		ID: r.ID, JobID: r.JobID, Handle: r.Handle, Trigger: r.Trigger, RequestedAt: timeOf(r.RequestedAt),
		ScheduledTime: timeOf(r.ScheduledAt), StartTime: timeOf(r.StartedAt), FinishTime: timeOf(r.FinishedAt),
		Outcome: r.Outcome, Reason: r.Reason, Message: r.Message, OutputPath: r.OutputPath,
		NotifiedAt: timeOf(r.NotifiedAt), CancelRequested: r.CancelRequested, Slot: timeOf(r.Slot),
	}
	if err := unmarshal(r.Outputs, &run.Outputs); err != nil {
		return model.Run{}, err
	}
	return run, unmarshal(r.Parameters, &run.Parameters)
}

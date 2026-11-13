package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/loop"
	"github.com/nebari-dev/javo/internal/controller/secretenv"
	"github.com/nebari-dev/javo/internal/controller/sweep"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/jobs/schedule"
	jobstatus "github.com/nebari-dev/javo/internal/jobs/status"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/workload"
)

// Reconciler starts the runs of job rows: the ones a person asked for and
// the ones their schedule comes due for, which it fires itself on every
// platform. It records every run in the database and never writes spec
// columns.
type Reconciler struct {
	// Exec runs the jobs' runs and Secrets keeps the bundles they read.
	// Both are required.
	Exec    executor.Executor
	Secrets executor.Secrets
	// Host says what a run is planned with to run as plain processes of
	// this machine; nil where runs are containers. A job whose template
	// cannot run that way is Blocked as TemplateInvalid.
	Host func(workload.Ref) workload.Host
	// Legacy removes what earlier versions left where the runs run; nil
	// where there is nothing to remove.
	Legacy Legacy
	// Store is the database jobs, runs, templates and people are read from.
	// Required.
	Store       *store.Store
	RunnerImage podspec.Image
	// PixiImage installs the environment of a job that runs in one
	// (internal/pixienv). Without it such a job is Blocked.
	PixiImage string
	// Access and Now are required: SetupWithManager refuses a Reconciler
	// without them. Now is the clock schedules are fired by.
	Access Access
	Now    func() time.Time
	// UIDMode is identity.uidMode: runs use the owner's uid (identity.PodUIDOf).
	UIDMode string
	// Storage reads what a run mounts, as for the owner's apps. Required.
	Storage podstorage.Resolver
	// ResyncEvery is how often every job row is looked at again regardless of
	// announcements, and every orphan swept. Zero means dbsource.DefaultResync.
	ResyncEvery time.Duration
	// SecretKeys opens the values of people's secrets. Nil when the
	// controller has no key-encryption key: a job whose runs get secrets is
	// then blocked, and its runs fail, as SecretsUnreadable.
	SecretKeys *secrets.Keys
	// Sizes are the admin-defined sizes a job's runs get; empty keeps the
	// template's resources.
	Sizes sizes.Catalog
	// Tokens stores the API tokens of jobs whose template gives one
	// (TemplateSpec.APIToken, as for apps), and APIURL is the address those
	// runs reach the API at. Nil Tokens makes such a job invalid.
	Tokens JobTokens
	APIURL string
	// APIInternalURL is the gateway's address inside the cluster.
	APIInternalURL string
	// MaxTimeout is limits.jobMaxTimeout: no run takes longer. Zero is no
	// platform limit.
	MaxTimeout time.Duration

	// readable is, per job with a schedule, the fingerprint of the secrets
	// its runs get that were last opened without trouble, so they are not
	// opened again on every look.
	readable sync.Map
}

// Legacy removes what earlier versions kept for a job where its runs run.
type Legacy interface {
	// Tidy removes what was kept for a job, or for one of its runs, under
	// names no longer used. No run of the job reads it any more when it is
	// called. An API token found there is handed to revoke first, by its id.
	Tidy(ctx context.Context, ref workload.Ref, revoke func(ctx context.Context, tokenID string) error) error
	// Retire removes the schedule the platform itself fired for the job (a
	// Kubernetes CronJob), without ending a run it started. When there is
	// one, keep is handed the time up to which it started the schedule's
	// slots, before it is removed.
	Retire(ctx context.Context, ref workload.Ref, keep func(through time.Time) error) error
	// Schedules names the jobs that still have such a schedule.
	Schedules(ctx context.Context) ([]workload.Ref, error)
}

// JobTokens makes, checks and revokes the API tokens of jobs.
type JobTokens interface {
	Get(ctx context.Context, id string) (token.Token, error)
	Create(ctx context.Context, actor model.Actor, t token.Token) (string, token.Token, error)
	Revoke(ctx context.Context, actor model.Actor, owner, id string) (token.Token, error)
}

// RBAC for the controller (make rbac renders config/rbac/controller; the
// Helm chart's Roles must grant the same rules, test/helm checks it). The
// Jobs, pods, events and Secrets are the Kubernetes executor's
// (internal/executor/kube); its rules are kept here, where make rbac looks.
// CronJobs are only read and removed: an earlier version fired schedules
// with them.
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update;patch;delete

const (
	// unmanagedRetry is how often a job in a not-yet-managed namespace is
	// looked at again.
	unmanagedRetry = 10 * time.Second
	// finalizeRetry is how soon a deleted job whose runs were still going
	// is looked at again.
	finalizeRetry = time.Second
)

// system is who the controller's audited writes are made as.
var system = model.Actor{System: model.SystemController}

// verdict is what the reconciler concluded about a job before looking at
// runs: the phase, and the catalog code behind a Blocked or Invalid phase.
type verdict struct {
	phase string
	code  reasons.Code // empty unless Blocked or Invalid
	tpl   *model.Template
	// layout is what runs mount and run as (the owner's uid).
	layout podstorage.Layout
	next   []time.Time
	// secrets are the owner's secrets each run gets (prepareSecrets).
	secrets secretenv.Set
	// size is what each run gets as CPU and memory.
	size sizes.Size
	// api is the owner's API token of a job whose template gives one.
	api API
	// env is the files of the job's environment; nil when it names none.
	env *model.EnvironmentFiles
	// active counts the job's runs still going and running the owner's runs
	// that were started (store.Jobs.RunningRuns); held is RunLimitReached when either
	// is at its cap (store.Limits), which holds the schedule (runCaps).
	active, running int64
	held            reasons.Code
	// through is the time up to which the schedule's slots are decided
	// (JobStatus.FiredThrough) and last the newest slot a run was started
	// for (JobStatus.LastScheduleTime), as they are after this look.
	through, last time.Time
}

// Reconcile records what the executor shows of one job's runs (the request
// names the job's id), decides the slot of its schedule that has come due
// and starts the runs that are to start.
func (r *Reconciler) Reconcile(ctx context.Context, id string) (time.Duration, error) {
	job, err := r.Store.Jobs().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		// Gone: what it left, if anything, is the sweep's.
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !job.DeletedAt.IsZero() {
		return r.finalize(ctx, &job)
	}
	// No run is called after the job alone, so this only asks whether the
	// executor runs anything where the job is.
	if _, err := r.Exec.Observe(ctx, jobRef(&job)); errors.Is(err, executor.ErrUnmanaged) {
		log.FromContext(ctx).Info("ignoring job in a namespace the controller does not manage", "namespace", job.Namespace)
		return unmanagedRetry, nil
	} else if err != nil {
		return 0, err
	}
	now := r.Now()

	v, err := r.assess(ctx, &job, now)
	if err != nil {
		return 0, err
	}
	seen, err := r.look(ctx, &job)
	if err != nil {
		return 0, err
	}
	if err := r.runCaps(ctx, &job, &v, seen); err != nil {
		return 0, err
	}
	release, err := r.recordRuns(ctx, &job, seen, now)
	if err != nil {
		return 0, err
	}
	// Read last, so a run started now gets the values as they are now.
	if err := r.prepareSecrets(ctx, &job, &v); err != nil {
		return 0, err
	}
	if err := r.fire(ctx, &job, &v, now); err != nil {
		return 0, err
	}
	if err := r.startRequested(ctx, &job, &v, now); err != nil {
		return 0, err
	}
	if err := r.dropLegacySecrets(ctx, &job, seen); err != nil {
		return 0, err
	}
	if err := r.writeStatus(ctx, &job, v, now); err != nil {
		return 0, err
	}
	// Only now is every finished run recorded, so the executor may let go
	// of it.
	for _, ref := range release {
		if err := r.Exec.Stop(ctx, ref); err != nil {
			return 0, err
		}
	}
	if len(v.next) > 0 {
		return v.next[0].Sub(now), nil
	}
	return 0, nil
}

// assess decides the phase from the template, the owner's access and the job
// itself.
func (r *Reconciler) assess(ctx context.Context, job *model.Job, now time.Time) (verdict, error) {
	tpl, err := r.Store.Templates().Get(ctx, job.Spec.Template)
	if errors.Is(err, store.ErrNotFound) {
		return verdict{phase: model.JobPhaseBlocked, code: reasons.TemplateMissing}, nil
	}
	if err != nil {
		return verdict{}, err
	}
	// Admin templates are stored as written; an empty field means its default.
	tpl.Spec.ApplyDefaults()
	v := verdict{phase: model.JobPhaseReady, tpl: &tpl}
	ok, err := r.Access.MayUse(ctx, job.Owner, &tpl)
	if err != nil {
		return verdict{}, err
	}
	if !ok {
		return verdict{phase: model.JobPhaseBlocked, code: reasons.OwnerLostAccess}, nil
	}
	uid, ok, err := identity.PodUIDOf(ctx, r.Store.People(store.IDRange{}), r.UIDMode, job.Owner)
	if err != nil {
		return verdict{}, err
	}
	if !ok {
		return verdict{phase: model.JobPhaseBlocked, code: reasons.IdentityPending}, nil
	}
	// Its runs mount the owner's home, made here for someone with no app.
	if _, err := r.Storage.EnsureHome(ctx, r.Store.Homes(), job.Namespace, job.Owner); err != nil {
		return verdict{}, err
	}
	in, err := r.Storage.Input(ctx, job.Namespace, job.Owner)
	if err != nil {
		return verdict{}, err
	}
	in.UID, in.GID = uid, podspec.FixedGID
	v.layout = podstorage.Build(job.Owner, in)
	if err := Validate(Inputs{Job: job, Template: &tpl}); err != nil {
		return verdict{phase: model.JobPhaseInvalid, code: invalidCode(err)}, nil
	}
	if v.size, ok = r.Sizes.For(job.Size, job.Resources); !ok {
		return verdict{phase: model.JobPhaseInvalid, code: reasons.JobSizeUnknown}, nil
	}
	if r.Host != nil {
		// As processes of this machine: its pixi installs environments,
		// and a template that needs an image cannot run.
		host := r.Host(jobRef(job))
		var no *executor.UnsupportedError
		if _, err := workspace.PlanRunOnHost(&host, v.layout, &tpl, job.Spec.Environment != nil); errors.As(err, &no) {
			log.FromContext(ctx).Info("job blocked: its runs cannot be processes of this machine", "job", job.Name, "why", no.Error())
			return verdict{phase: model.JobPhaseBlocked, code: reasons.TemplateInvalid}, nil
		}
	} else if tpl.Spec.Environment != "" && r.PixiImage == "" {
		return verdict{phase: model.JobPhaseBlocked, code: reasons.EnvironmentUnavailable}, nil
	}
	if job.Spec.Environment != nil {
		files, err := r.Store.Environments().ForJob(ctx, job.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return verdict{}, err
		}
		if err != nil || (r.PixiImage == "" && r.Host == nil) || pixienv.Fits(files) != nil {
			return verdict{phase: model.JobPhaseBlocked, code: reasons.EnvironmentUnavailable}, nil
		}
		v.env = &files
	}
	if v.api, err = r.syncToken(ctx, job, &tpl); err != nil {
		return verdict{}, err
	}
	if s := job.Spec.Schedule; s != nil {
		p, err := schedule.Next(s.Cron, s.TimeZone, now, 3)
		if err != nil {
			return verdict{phase: model.JobPhaseInvalid, code: reasons.ScheduleInvalid}, nil
		}
		if s.Paused {
			v.phase = model.JobPhasePaused
		} else {
			v.next = p.Next
		}
	}
	return v, nil
}

// runCaps counts what the per-person run caps (store.Limits) are checked
// against and holds the schedule when one is reached: a run would otherwise
// start at every slot whatever the concurrency (Allow) or the owner's other
// jobs.
func (r *Reconciler) runCaps(ctx context.Context, job *model.Job, v *verdict, seen []seenRun) error {
	l := r.Store.Limits()
	if l.RunsPerJob <= 0 && l.RunsPerPerson <= 0 {
		return nil
	}
	for _, s := range seen {
		if s.st != nil && going(s.st) {
			v.active++
		}
	}
	var err error
	if v.running, err = r.Store.Jobs().RunningRuns(ctx, job.Owner); err != nil {
		return err
	}
	if atRunCap(l, v.active, v.running) {
		v.held = reasons.RunLimitReached
	}
	return nil
}

// atRunCap reports whether a job with active runs going, of an owner with
// running runs, may start no more.
func atRunCap(l store.Limits, active, running int64) bool {
	return l.RunsPerJob > 0 && active >= l.RunsPerJob || l.RunsPerPerson > 0 && running >= l.RunsPerPerson
}

// inputs is what renders the job's runs under verdict v.
func (r *Reconciler) inputs(job *model.Job, v verdict) Inputs {
	return Inputs{Job: job, Template: v.tpl, RunnerImage: r.RunnerImage, Layout: v.layout, Secrets: v.secrets, Size: v.size, API: v.api,
		Environment: v.env, PixiImage: r.PixiImage, MaxTimeout: r.MaxTimeout}
}

func invalidCode(err error) reasons.Code {
	switch {
	case errors.Is(err, ErrEngineNotInTemplate):
		return reasons.EngineMissing
	case errors.Is(err, ErrKernelNotInTemplate):
		return reasons.KernelMissing
	}
	return reasons.JobsNotAllowed
}

// fire decides the slot of the job's schedule that has come due, at most
// one a look: the newest at or before now that is after the last one
// decided. The slot becomes a run row, claimed once whoever asks
// (store.Jobs.ClaimSlot): one startRequested is to start, or one already
// Missed when now is past the slot's starting deadline. Older slots that
// passed meanwhile are gone, and a slot still waiting to start is Skipped
// for the newer one.
//
// These are the rules of the Kubernetes CronJob controller, which fired
// schedules before (syncCronJob and mostRecentScheduleTime in
// pkg/controller/cronjob at v1.37.0): it looked at the most recent
// unstarted time only, counted from its last schedule time or else from
// when it was made, started nothing while suspended and took up the most
// recent time again once it no longer was. A paused or blocked job, and one
// at a run cap, is that suspended CronJob.
func (r *Reconciler) fire(ctx context.Context, job *model.Job, v *verdict, now time.Time) error {
	s := job.Spec.Schedule
	v.through, v.last = job.Status.FiredThrough, job.Status.LastScheduleTime
	if s == nil {
		v.through, v.last = time.Time{}, time.Time{}
	}
	if v.through.IsZero() {
		if err := r.takeOver(ctx, job, v); err != nil {
			return err
		}
	}
	if s == nil {
		v.through = time.Time{}
		return nil
	}
	if v.through.IsZero() {
		if v.code != "" {
			return nil
		}
		// A schedule counts from when it can first run.
		v.through = now
	}
	if v.code != "" || s.Paused || v.held != "" {
		return nil
	}
	slot, err := schedule.Previous(s.Cron, s.TimeZone, now)
	if err != nil || !slot.After(v.through) {
		return err
	}
	run := model.Run{JobID: job.ID, Trigger: TriggerSchedule, Slot: slot, ScheduledTime: slot}
	if d := s.StartingDeadline; d != nil && slot.Add(d.Duration).Before(now) {
		run.Outcome, run.Reason, run.FinishTime = model.RunOutcomeMissed, string(reasons.RunMissed), slot
	}
	if _, _, err := r.Store.Jobs().ClaimSlot(ctx, run); err != nil {
		return err
	}
	v.through = slot
	// A slot that waited until now has waited too long.
	runs, err := r.Store.Jobs().Runs(ctx, job.ID)
	if err != nil {
		return err
	}
	for _, old := range runs {
		if waiting(old) && old.Slot.Before(slot) {
			if err := r.endRequest(ctx, old, model.RunOutcomeSkipped, reasons.RunSkipped, old.Slot); err != nil {
				return err
			}
		}
	}
	return nil
}

// takeOver retires the schedule an earlier version had the platform fire
// for the job, if there is one, and goes on from where that stopped. The
// time is stored before the old schedule is removed, so a slot is neither
// started by both nor by none.
func (r *Reconciler) takeOver(ctx context.Context, job *model.Job, v *verdict) error {
	if r.Legacy == nil {
		return nil
	}
	return r.Legacy.Retire(ctx, jobRef(job), func(through time.Time) error {
		st := job.Status
		st.FiredThrough = through
		if err := r.Store.Jobs().SetStatus(ctx, job.ID, job.Status.ObservedVersion, st); err != nil {
			return err
		}
		job.Status.FiredThrough, v.through = through, through
		return nil
	})
}

// waiting is true for the run of a schedule's slot that has not started.
func waiting(run model.Run) bool {
	return run.Outcome == "" && run.Handle == "" && !run.Slot.IsZero()
}

// final reports whether a run's outcome can no longer change: it has one and
// it is not Running.
func final(outcome string) bool { return outcome != "" && outcome != model.RunOutcomeRunning }

// startRequested starts each run that is to start (a row with no outcome):
// the ones a person asked for and the slots fire claimed. The run's name
// comes from the row id, so a resync or restart between starting the run
// and recording it finds the same run again. A cancel asked for before the
// start ends the run without starting it. A job that cannot run now
// (blocked, invalid, at a run cap) fails a person's request with the
// reason; a slot waits.
func (r *Reconciler) startRequested(ctx context.Context, job *model.Job, v *verdict, now time.Time) error {
	runs, err := r.Store.Jobs().Runs(ctx, job.ID)
	if err != nil {
		return err
	}
	// scheduled are the job's scheduled runs that are going, which the
	// overlap rule is about; a run started by hand is not.
	var scheduled []workload.Ref
	for _, run := range runs {
		if run.Trigger == TriggerSchedule && run.Outcome == model.RunOutcomeRunning && run.Handle != "" {
			scheduled = append(scheduled, runRef(job, run.Handle))
		}
	}
	for _, run := range runs {
		if run.Outcome != "" || run.Handle != "" {
			continue
		}
		ref := runRef(job, RunName(job, run.ID))
		slot := !run.Slot.IsZero()
		if run.CancelRequested || (slot && job.Spec.Schedule == nil) {
			// A start cut short may have left the run, or the bundle written
			// for it with no run to go with.
			if _, err := r.drop(ctx, ref); err != nil {
				return err
			}
			if err := r.deleteRunSecret(ctx, job, ref); err != nil {
				return err
			}
			if err := r.endRequest(ctx, run, model.RunOutcomeCancelled, reasons.RunCancelled, now); err != nil {
				return err
			}
			continue
		}
		atCap := atRunCap(r.Store.Limits(), v.active, v.running)
		if slot {
			s := job.Spec.Schedule
			if d := s.StartingDeadline; d != nil && run.Slot.Add(d.Duration).Before(now) {
				if err := r.endRequest(ctx, run, model.RunOutcomeMissed, reasons.RunMissed, run.Slot); err != nil {
					return err
				}
				continue
			}
			// Held as its CronJob was suspended, or by the overlap rule: it
			// starts at a later look, unless a newer slot comes due first.
			if v.code != "" || s.Paused || atCap || (s.Concurrency == concurrencyForbid && len(scheduled) > 0) {
				continue
			}
			if s.Concurrency == concurrencyReplace {
				// Each is recorded as cancelled once the executor shows it stopped.
				for _, old := range scheduled {
					if err := r.Exec.Stop(ctx, old); err != nil {
						return err
					}
				}
			}
		}
		if v.code != "" {
			if err := r.endRequest(ctx, run, model.RunOutcomeFailed, v.code, now); err != nil {
				return err
			}
			continue
		}
		// The gateway counted this request against the caps; scheduled runs
		// may have taken the room since.
		if atCap {
			if err := r.endRequest(ctx, run, model.RunOutcomeFailed, reasons.RunLimitReached, now); err != nil {
				return err
			}
			continue
		}
		in := r.inputs(job, *v)
		if r.Host != nil {
			host := r.Host(ref)
			in.Host = &host
		}
		spec, err := PlanRun(in, run.ID, run.Parameters, run.Slot)
		if errors.Is(err, ErrParameterNotDeclared) {
			if err := r.endRequest(ctx, run, model.RunOutcomeFailed, reasons.ParametersInvalid, now); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		// The bundle comes first, so the run never starts without it.
		unreadable, err := r.startSecret(ctx, *v, ref)
		if err != nil {
			return err
		}
		if unreadable != "" {
			log.FromContext(ctx).Info("run not started: its secrets could not be opened", "run", run.ID, "why", unreadable)
			if err := r.endRequest(ctx, run, model.RunOutcomeFailed, reasons.SecretsUnreadable, now); err != nil {
				return err
			}
			continue
		}
		if err := r.Exec.Start(ctx, spec); err != nil {
			return err
		}
		// Again, now that the run is there for the bundle to go with.
		if _, err := r.startSecret(ctx, *v, ref); err != nil {
			return err
		}
		v.active, v.running = v.active+1, v.running+1
		if slot {
			scheduled = append(scheduled, ref)
			if run.Slot.After(v.last) {
				v.last = run.Slot
			}
		}
		run.Handle, run.Outcome, run.Reason = ref.Name, model.RunOutcomeRunning, string(reasons.Running)
		if _, err := r.Store.Jobs().RecordRun(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

// The overlap rules of a schedule (JobSchedule.Concurrency), under the
// names the Kubernetes CronJob gave them. Forbid makes a slot wait for the
// scheduled run before it, Replace stops that run, and Allow, or any other
// value, starts the slot beside it.
const (
	concurrencyForbid  = "Forbid"
	concurrencyReplace = "Replace"
)

// deleteRunSecret removes the bundle of the run ref, under its name and the
// one it had before RunSecretName.
func (r *Reconciler) deleteRunSecret(ctx context.Context, job *model.Job, ref workload.Ref) error {
	if err := r.Secrets.Delete(ctx, ref, RunSecretName(ref.Name)); err != nil {
		return err
	}
	return r.tidy(ctx, job, ref)
}

// endRequest records a run that was never started, as over at the time at.
func (r *Reconciler) endRequest(ctx context.Context, run model.Run, outcome string, code reasons.Code, at time.Time) error {
	run.Outcome, run.Reason, run.FinishTime = outcome, string(code), at
	_, err := r.Store.Jobs().RecordRun(ctx, run)
	return err
}

// seenRun is a run of the job the executor has, with what it shows of it:
// nil for a run already recorded as over, which is not looked at again.
type seenRun struct {
	ref workload.Ref
	st  *workload.State
}

// runsOf names the job's runs the executor has, scheduled and manual.
func (r *Reconciler) runsOf(ctx context.Context, job *model.Job) ([]workload.Ref, error) {
	all, err := r.Exec.List(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(ref workload.Ref) bool { return ref.ID != job.ID || ref.Namespace != job.Namespace }), nil
}

// look observes the job's runs the executor has. A run recorded as over
// stays as recorded: observing it again could replace the outcome and
// reason with worse ones once what explained them is gone, and would cost
// a look at the platform on every reconcile.
func (r *Reconciler) look(ctx context.Context, job *model.Job) ([]seenRun, error) {
	refs, err := r.runsOf(ctx, job)
	if err != nil {
		return nil, err
	}
	stored, err := r.Store.Jobs().Runs(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	over := map[string]bool{}
	for _, run := range stored {
		over[run.Handle] = run.Handle != "" && final(run.Outcome)
	}
	var seen []seenRun
	for _, ref := range refs {
		if over[ref.Name] {
			seen = append(seen, seenRun{ref: ref})
			continue
		}
		st, err := r.Exec.Observe(ctx, ref)
		if err != nil {
			return nil, err
		}
		if st != nil {
			seen = append(seen, seenRun{ref: ref, st: st})
		}
	}
	return seen, nil
}

// recordRuns writes every run the executor has of the job to job_runs;
// stops a run that was asked to be cancelled; calls a run lost when the
// executor no longer has it; and keeps only the newest keepRuns finished
// runs (plus those the executor still has). It returns the runs whose
// record is final: the executor may let go of them. A run the controller
// did not start (one a CronJob of an earlier version made) is recorded all
// the same, for the slot it was made for.
func (r *Reconciler) recordRuns(ctx context.Context, job *model.Job, seen []seenRun, now time.Time) ([]workload.Ref, error) {
	stored, err := r.Store.Jobs().Runs(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	byHandle := make(map[string]model.Run, len(stored))
	// starting are the runs whose start was cut short before it was
	// recorded: startRequested finishes it, and records them.
	starting := map[string]bool{}
	for _, run := range stored {
		switch {
		case run.Handle != "":
			byHandle[run.Handle] = run
		case run.Outcome == "":
			starting[RunName(job, run.ID)] = true
		}
	}

	had := map[string]bool{}
	var release []workload.Ref
	for _, s := range seen {
		had[s.ref.Name] = true
		if starting[s.ref.Name] {
			continue
		}
		old, known := byHandle[s.ref.Name]
		if s.st == nil {
			release = append(release, s.ref)
			continue
		}
		if known && old.CancelRequested && !s.st.Run.Done {
			if err := r.Exec.Stop(ctx, s.ref); err != nil {
				return nil, err
			}
		}
		run := jobstatus.DeriveRun(s.st)
		run.ID, run.JobID = old.ID, job.ID
		if !known || !sameRun(old, run) {
			if _, err := r.Store.Jobs().RecordRun(ctx, run); err != nil {
				return nil, err
			}
		}
		if final(run.Outcome) {
			release = append(release, s.ref)
		}
	}
	// A run recorded as Running that the executor no longer has (removed
	// behind its back, for example while the controller was not installed)
	// cannot finish any more.
	for _, old := range stored {
		if old.Outcome != model.RunOutcomeRunning || old.Handle == "" || had[old.Handle] {
			continue
		}
		st, err := r.Exec.Observe(ctx, runRef(job, old.Handle))
		if err != nil {
			return nil, err
		}
		if st != nil {
			continue
		}
		old.Outcome, old.Reason, old.FinishTime = model.RunOutcomeLost, string(reasons.RunLost), now
		if _, err := r.Store.Jobs().RecordRun(ctx, old); err != nil {
			return nil, err
		}
	}

	// A run the executor still has stays: the next reconcile would record
	// it again as a new run (and notify a failure again).
	live := make([]string, 0, len(seen))
	for _, s := range seen {
		live = append(live, s.ref.Name)
	}

	if _, err := r.Store.Jobs().PruneRuns(ctx, system, job.ID, int(job.Spec.History.KeepRuns), live); err != nil {
		return nil, err
	}
	return release, nil
}

// sameRun reports whether recording next would store nothing new.
func sameRun(old, next model.Run) bool {
	return old.Outcome == next.Outcome && old.Reason == next.Reason && old.Message == next.Message &&
		old.OutputPath == next.OutputPath && slices.Equal(old.Outputs, next.Outputs) &&
		old.StartTime.Equal(next.StartTime) && old.FinishTime.Equal(next.FinishTime)
}

// drop ends the run ref and lets go of it. It reports whether the run was
// still going: the executor then holds it until it shows it as stopped and
// is asked again.
func (r *Reconciler) drop(ctx context.Context, ref workload.Ref) (wasGoing bool, err error) {
	st, err := r.Exec.Observe(ctx, ref)
	if err != nil || st == nil {
		return false, err
	}
	if err := r.Exec.Stop(ctx, ref); err != nil {
		return false, err
	}
	if st.Run.Done {
		return false, nil
	}
	// Lets go at once where the executor already shows the run stopped.
	return true, r.Exec.Stop(ctx, ref)
}

// finalize removes what a deleted job left where its runs run, then the
// row. Its runs go with the row, so a run the executor holds has nowhere to
// be recorded: each is ended and let go of first, and the row stays until
// none is still going.
func (r *Reconciler) finalize(ctx context.Context, job *model.Job) (time.Duration, error) {
	if r.Legacy != nil {
		// One not looked at since the upgrade may still have its old schedule.
		if err := r.Legacy.Retire(ctx, jobRef(job), func(time.Time) error { return nil }); err != nil {
			return 0, err
		}
	}
	refs, err := r.runsOf(ctx, job)
	if err != nil {
		return 0, err
	}
	held := false
	for _, ref := range refs {
		wasGoing, err := r.drop(ctx, ref)
		if err != nil {
			return 0, err
		}
		held = held || wasGoing
		// It goes with the run too; deleting it here does not wait for that.
		if err := r.deleteRunSecret(ctx, job, ref); err != nil {
			return 0, err
		}
	}
	if held {
		return finalizeRetry, nil
	}
	if err := r.dropToken(ctx, job); err != nil {
		return 0, err
	}
	if err := r.tidy(ctx, job, jobRef(job)); err != nil {
		return 0, err
	}
	r.readable.Delete(job.ID)
	err = r.Store.Jobs().Purge(ctx, system, job.ID)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	return 0, err
}

// writeStatus updates the status columns only when they differ. Conditions
// that keep their status keep their transition time, and conditions this
// reconciler does not own (Notified) are kept, so a repeat reconcile writes
// nothing.
func (r *Reconciler) writeStatus(ctx context.Context, job *model.Job, v verdict, now time.Time) error {
	next := job.Status
	next.Phase, next.ObservedVersion = v.phase, job.Version
	next.NextRunTimes, next.LastScheduleTime, next.FiredThrough = nil, v.last.UTC(), v.through.UTC()
	for _, t := range v.next {
		next.NextRunTimes = append(next.NextRunTimes, t.UTC())
	}
	next.Conditions = append([]metav1.Condition(nil), job.Status.Conditions...)
	for _, c := range conditions(job, v, now) {
		meta.SetStatusCondition(&next.Conditions, c)
	}
	if same, err := sameStatus(job.Status, next); err != nil || same {
		return err
	}
	err := r.Store.Jobs().SetStatus(ctx, job.ID, job.Version, next)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

// sameStatus compares what would be stored: the status as JSON, plus the
// observed version, which has a column of its own.
func sameStatus(a, b model.JobStatus) (bool, error) {
	if a.ObservedVersion != b.ObservedVersion {
		return false, nil
	}
	x, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	y, err := json.Marshal(b)
	return string(x) == string(y), err
}

// conditions: Valid says whether the job can run; Scheduled (only for a job
// with a schedule) says whether the schedule is active.
func conditions(job *model.Job, v verdict, now time.Time) []metav1.Condition {
	cond := func(t string, s metav1.ConditionStatus, code reasons.Code) metav1.Condition {
		r, _ := reasons.Lookup(code)
		return metav1.Condition{Type: t, Status: s, Reason: string(code), Message: r.Title,
			ObservedGeneration: job.Version, LastTransitionTime: metav1.NewTime(now)}
	}
	var out []metav1.Condition
	if v.code != "" {
		out = append(out, cond(model.ConditionValid, metav1.ConditionFalse, v.code))
	} else {
		out = append(out, cond(model.ConditionValid, metav1.ConditionTrue, reasons.Ready))
	}
	switch {
	case job.Spec.Schedule == nil:
	case v.code != "":
		out = append(out, cond(model.ConditionScheduled, metav1.ConditionFalse, v.code))
	case v.phase == model.JobPhasePaused:
		out = append(out, cond(model.ConditionScheduled, metav1.ConditionFalse, reasons.SchedulePaused))
	case v.held != "":
		out = append(out, cond(model.ConditionScheduled, metav1.ConditionFalse, v.held))
	default:
		out = append(out, cond(model.ConditionScheduled, metav1.ConditionTrue, reasons.Ready))
	}
	return out
}

// Sweep removes the runs that carry a job id no row has any more: what a
// job left behind when its row was removed without the controller running.
// It asks the executor first and reads the rows after, so a run started
// while the sweep runs belongs to a row that is among them. The schedule an
// earlier version had the platform fire for such a job goes too.
func (r *Reconciler) Sweep(ctx context.Context) error {
	refs, err := r.Exec.List(ctx)
	if err != nil {
		return err
	}
	var schedules []workload.Ref
	if r.Legacy != nil {
		if schedules, err = r.Legacy.Schedules(ctx); err != nil {
			return err
		}
	}
	jobs, err := r.Store.Jobs().ListAll(ctx)
	if err != nil {
		return err
	}
	live := make(map[string]bool, len(jobs))
	for _, j := range jobs {
		live[j.ID] = true
	}
	var failed error
	for _, ref := range refs {
		if live[ref.ID] {
			continue
		}
		log.FromContext(ctx).Info("removing a run whose job's row is gone", "namespace", ref.Namespace, "name", ref.Name)
		// One that was going and is still held goes at the next sweep.
		if _, err := r.drop(ctx, ref); err != nil {
			failed = err
		}
	}
	for _, ref := range schedules {
		if live[ref.ID] {
			continue
		}
		log.FromContext(ctx).Info("removing the schedule of a job whose row is gone", "namespace", ref.Namespace, "name", ref.Name)
		if err := r.Legacy.Retire(ctx, ref, func(time.Time) error { return nil }); err != nil {
			failed = err
		}
	}
	return failed
}

// Setup wires the controller on d: job rows (and the run requests,
// templates and people they depend on) come from the database through feed;
// changes to their runs come from the executor. A job with a schedule is
// looked at again when its next slot comes. It also adds the orphan sweep.
// The caller runs feed.
func (r *Reconciler) Setup(d loop.Driver, feed *dbsource.Feed) error {
	if r.Exec == nil || r.Secrets == nil || r.Store == nil || r.Access == nil || r.Now == nil || r.RunnerImage.Ref == "" ||
		r.Storage.Homes == nil || r.Storage.Folders == nil {
		return errors.New("jobs reconciler needs Exec, Secrets, Store, Access, Now, RunnerImage and Storage")
	}
	if err := d.Add(&sweep.Runner{What: "jobs", Sweep: r.Sweep, Every: r.ResyncEvery, Changes: feed.Subscribe()}); err != nil {
		return err
	}
	return d.Reconcile("job", r,
		dbsource.For(feed.Subscribe(), "jobs", r.ResyncEvery, r.allJobs),
		dbsource.Fan(feed.Subscribe(), "job_runs", r.jobOfRun),
		dbsource.Fan(feed.Subscribe(), "templates", r.jobsOfTemplate),
		dbsource.Fan(feed.Subscribe(), "people", r.jobsOfPerson),
		r.runs())
}

// runs feeds the controller the job of every run the executor saw change.
func (r *Reconciler) runs() loop.Source {
	return loop.SourceFunc(func(ctx context.Context, enqueue func(id string)) error {
		go func() {
			err := r.Exec.Watch(ctx, func(ref workload.Ref) {
				// A run that lacks the row label names no row.
				if ref.ID != "" {
					enqueue(ref.ID)
				}
			})
			if err != nil && ctx.Err() == nil {
				log.FromContext(ctx).Error(err, "watching the jobs' runs")
			}
		}()
		return nil
	})
}

func jobID(j *model.Job) string { return j.ID }

// allJobs lists every job row, also those marked deleted: they still have
// objects to remove.
func (r *Reconciler) allJobs(ctx context.Context) ([]string, error) {
	jobs, err := r.Store.Jobs().ListAll(ctx)
	return dbsource.IDs(jobs, jobID, nil), err
}

// jobOfRun is the job a changed run row belongs to: a new request or cancel
// wakes it.
func (r *Reconciler) jobOfRun(ctx context.Context, runID string) ([]string, error) {
	run, err := r.Store.Jobs().Run(ctx, runID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return []string{run.JobID}, err
}

// jobsOfTemplate is every job that runs on the template, which just changed.
func (r *Reconciler) jobsOfTemplate(ctx context.Context, name string) ([]string, error) {
	jobs, err := r.Store.Jobs().ListAll(ctx)
	return dbsource.IDs(jobs, jobID, func(j *model.Job) bool { return j.Spec.Template == name }), err
}

// jobsOfPerson is the jobs of the person whose row just changed: a new uid or
// a new groups snapshot can unblock, or block, them.
func (r *Reconciler) jobsOfPerson(ctx context.Context, id string) ([]string, error) {
	person, err := r.Store.People(store.IDRange{}).ByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	jobs, err := r.Store.Jobs().List(ctx, person.Name)
	return dbsource.IDs(jobs, jobID, nil), err
}

// apiSecretName is the bundle holding the API token of job (see
// RunSecretName).
func apiSecretName(job *model.Job) string { return jobName(job) + ".job-api" }

// syncToken gives a job whose template gives an API token (as for apps,
// model.DefaultAPIAccess) its owner's token, made once and kept in a bundle
// of the job; a job whose template gives none has its token revoked.
func (r *Reconciler) syncToken(ctx context.Context, job *model.Job, tpl *model.Template) (API, error) {
	scope := token.ScopeFor(model.DefaultAPIAccess(tpl))
	if scope == "" {
		return API{}, r.dropToken(ctx, job)
	}
	if r.Tokens == nil {
		return API{}, errors.New("the template gives jobs an API token, but the controller has no token store")
	}
	name, ref := apiSecretName(job), jobRef(job)
	have, err := r.Secrets.Get(ctx, ref, name)
	if err != nil {
		return API{}, err
	}
	// A bundle can name a token whose record is gone (`javo user remove`),
	// one made before tokens recorded their job, which is not kept from
	// the owner's secrets, or one that is not this job's at all (an app's,
	// another job's), which is never adopted: all are made again.
	old := ""
	if have != nil {
		old = have.Meta[model.AnnotationTokenID]
	}
	if old != "" {
		switch tok, err := r.Tokens.Get(ctx, old); {
		case errors.Is(err, token.ErrNotFound):
		case err != nil:
			return API{}, err
		case tok.Owner != job.Owner || tok.App != nil || (tok.Job != job.ID && tok.Job != ""):
			// Not the job's: never adopted, and not the job's to revoke.
			old = ""
		case tok.Job == job.ID && have.Meta[model.AnnotationTokenScope] == string(scope) && len(have.Data[APITokenKey]) > 0:
			return API{URL: r.APIURL, Internal: r.APIInternalURL, Secret: name}, nil
		}
	}
	text, tok, err := r.Tokens.Create(ctx, system, token.Token{Name: "job " + job.Name, Owner: job.Owner, Scope: scope, Job: job.ID})
	if err != nil {
		return API{}, fmt.Errorf("make the job's API token: %w", err)
	}
	if old != "" {
		if _, err := r.Tokens.Revoke(ctx, system, job.Owner, old); err != nil && !errors.Is(err, token.ErrNotFound) {
			return API{}, err
		}
	}
	err = r.Secrets.Put(ctx, ref, name, executor.Bundle{
		Data: map[string][]byte{APITokenKey: []byte(text)},
		Meta: map[string]string{model.AnnotationTokenID: tok.ID, model.AnnotationTokenScope: string(scope)},
	})
	if err != nil {
		return API{}, fmt.Errorf("store the job's API token: %w", err)
	}
	return API{URL: r.APIURL, Internal: r.APIInternalURL, Secret: name}, nil
}

// dropToken revokes a job's API token and deletes its bundle, if it has
// one. A bundle that is not the job's is left alone.
func (r *Reconciler) dropToken(ctx context.Context, job *model.Job) error {
	name, ref := apiSecretName(job), jobRef(job)
	have, err := r.Secrets.Get(ctx, ref, name)
	if errors.Is(err, executor.ErrForeign) || (err == nil && have == nil) {
		return nil
	}
	if err != nil {
		return err
	}
	if id := have.Meta[model.AnnotationTokenID]; id != "" {
		if err := r.revokeOwn(ctx, job, id); err != nil {
			return err
		}
	}
	return r.Secrets.Delete(ctx, ref, name)
}

// revokeOwn revokes the token id when its record is the job's.
func (r *Reconciler) revokeOwn(ctx context.Context, job *model.Job, id string) error {
	if r.Tokens == nil {
		return nil
	}
	switch tok, err := r.Tokens.Get(ctx, id); {
	case errors.Is(err, token.ErrNotFound):
	case err != nil:
		return err
	case tok.Job == job.ID && tok.Owner == job.Owner:
		if _, err := r.Tokens.Revoke(ctx, system, job.Owner, id); err != nil && !errors.Is(err, token.ErrNotFound) {
			return err
		}
	}
	return nil
}

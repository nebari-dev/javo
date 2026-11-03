package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/namespace"
	"github.com/nebari-dev/javo/internal/controller/secretenv"
	"github.com/nebari-dev/javo/internal/controller/sweep"
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
)

// Reconciler renders job rows into CronJobs, turns run requests into Jobs,
// and records every run of those Jobs in the database. It never writes spec
// columns.
type Reconciler struct {
	client.Client
	// Store is the database jobs, runs, templates and people are read from.
	// Required.
	Store *store.Store
	// Reader reads live, bypassing the cache; it confirms that a Job is gone
	// before its run is called lost. Required.
	Reader      client.Reader
	RunnerImage podspec.Image
	// PixiImage installs the environment of a job that runs in one
	// (internal/pixienv). Without it such a job is Blocked.
	PixiImage string
	// Access and Now are required: SetupWithManager refuses a Reconciler
	// without them.
	Access Access
	Now    func() time.Time
	// UIDMode is identity.uidMode: runs use the owner's uid (identity.PodUIDOf).
	UIDMode string
	// Storage reads what a run mounts, as for the owner's apps. Required.
	Storage podstorage.Resolver
	// ManagedNamespacesOnly builds jobs only in namespaces labelled
	// javo.nebari.dev/managed=true, as for apps.
	ManagedNamespacesOnly bool
	// CronJobEvents caches the Events about CronJobs (manager.CronJobEvents);
	// the manager's cache keeps only pod events. Required.
	CronJobEvents cache.Cache
	// ResyncEvery is how often every job row is looked at again regardless of
	// announcements, and every orphan swept. Zero means dbsource.DefaultResync.
	ResyncEvery time.Duration
	// Secrets reads and writes, live, the Secrets runs read the owner's
	// secrets from (Secrets are never cached). Required.
	Secrets client.Client
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
}

// JobTokens makes, checks and revokes the API tokens of jobs.
type JobTokens interface {
	Get(ctx context.Context, id string) (token.Token, error)
	Create(ctx context.Context, actor model.Actor, t token.Token) (string, token.Token, error)
	Revoke(ctx context.Context, actor model.Actor, owner, id string) (token.Token, error)
}

// RBAC for the controller (make rbac renders config/rbac/controller; the
// Helm chart's Roles must grant the same rules, test/helm checks it).
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update;patch;delete

const (
	indexEventObject = "involvedObject.kindName"

	// unmanagedRetry is how often a job in a not-yet-managed namespace is
	// looked at again.
	unmanagedRetry = 10 * time.Second

	// annotationSpecHash records the CronJob spec the reconciler last wrote, so
	// an unchanged job costs no write. A suspended-by-block CronJob carries
	// hashBlocked, which no real spec hashes to.
	annotationSpecHash = model.Group + "/spec-hash"
	hashBlocked        = "blocked"

	// The reasons the Kubernetes CronJob controller gives its events (see the
	// header of reconciler_test.go).
	eventJobAlreadyActive = "JobAlreadyActive"
	eventMissSchedule     = "MissSchedule"
	eventMissPrefix       = "Missed scheduled time to start a job: "
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
	// scheduled is the Secret scheduled runs read, as it is now (nil: none),
	// and scheduledData the values to write to it when it is out of date.
	scheduled     *corev1.Secret
	scheduledData map[string][]byte
	// api is the owner's API token of a job whose template gives one.
	api API
	// env is the files of the job's environment; nil when it names none.
	env *model.EnvironmentFiles
}

// Reconcile makes the cluster match one job row (the request names its id)
// and records its runs.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	job, err := r.Store.Jobs().Get(ctx, req.Name)
	if errors.Is(err, store.ErrNotFound) {
		// Gone: its objects, if any are left, are the sweep's.
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if !job.DeletedAt.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, &job)
	}
	if r.ManagedNamespacesOnly {
		if managed, err := namespace.IsManaged(ctx, r.Client, job.Namespace); err != nil {
			return ctrl.Result{}, err
		} else if !managed {
			log.FromContext(ctx).Info("ignoring job in a namespace the controller does not manage", "namespace", job.Namespace)
			return ctrl.Result{RequeueAfter: unmanagedRetry}, nil
		}
	}
	now := r.Now()

	v, err := r.assess(ctx, &job, now)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.prepareSecrets(ctx, &job, &v); err != nil {
		return ctrl.Result{}, err
	}
	cj, err := r.syncCronJob(ctx, &job, v)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.syncScheduledSecret(ctx, &job, v, cj); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.startRequested(ctx, &job, v, now); err != nil {
		return ctrl.Result{}, err
	}
	release, err := r.recordRuns(ctx, &job, now)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.writeStatus(ctx, &job, v, cj, now); err != nil {
		return ctrl.Result{}, err
	}
	// Only now is every finished run recorded, so its Job may go.
	for _, j := range release {
		if err := r.removeFinalizer(ctx, j); err != nil {
			return ctrl.Result{}, err
		}
	}
	if len(v.next) > 0 {
		return ctrl.Result{RequeueAfter: v.next[0].Sub(now) + time.Second}, nil
	}
	return ctrl.Result{}, nil
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
	if tpl.Spec.Environment != "" && r.PixiImage == "" {
		return verdict{phase: model.JobPhaseBlocked, code: reasons.EnvironmentUnavailable}, nil
	}
	if job.Spec.Environment != nil {
		files, err := r.Store.Environments().ForJob(ctx, job.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return verdict{}, err
		}
		if err != nil || r.PixiImage == "" || pixienv.Fits(files) != nil {
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

// inputs is what renders the job's runs under verdict v.
func (r *Reconciler) inputs(job *model.Job, v verdict) Inputs {
	return Inputs{Job: job, Template: v.tpl, RunnerImage: r.RunnerImage, Layout: v.layout, Secrets: v.secrets, Size: v.size, API: v.api,
		Environment: v.env, PixiImage: r.PixiImage}
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

// syncCronJob makes the CronJob match the verdict and returns it (nil when
// there is none). Ready and Paused jobs with a schedule own an up to date
// CronJob; Blocked and Invalid ones suspend the one they have, so its history
// survives; a job without a schedule has none.
func (r *Reconciler) syncCronJob(ctx context.Context, job *model.Job, v verdict) (*batchv1.CronJob, error) {
	got := &batchv1.CronJob{}
	err := r.Get(ctx, client.ObjectKey{Namespace: job.Namespace, Name: cronJobName(job)}, got)
	exists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}
	if exists && got.Labels[model.LabelJob] != job.ID {
		return nil, fmt.Errorf("cronjob %s/%s exists and belongs to another job than %s", got.Namespace, got.Name, job.ID)
	}
	switch {
	case job.Spec.Schedule == nil:
		if exists {
			return nil, client.IgnoreNotFound(r.Delete(ctx, got))
		}
		return nil, nil
	case v.phase == model.JobPhaseBlocked || v.phase == model.JobPhaseInvalid:
		if !exists || (ptr.Deref(got.Spec.Suspend, false) && got.Annotations[annotationSpecHash] == hashBlocked) {
			return got, nil
		}
		base := got.DeepCopy()
		got.Spec.Suspend = ptr.To(true)
		metav1.SetMetaDataAnnotation(&got.ObjectMeta, annotationSpecHash, hashBlocked)
		return got, r.Patch(ctx, got, client.MergeFrom(base))
	}

	want, err := BuildCronJob(r.inputs(job, v))
	if err != nil {
		return nil, err
	}
	hash, err := specHash(want)
	if err != nil {
		return nil, err
	}
	metav1.SetMetaDataAnnotation(&want.ObjectMeta, annotationSpecHash, hash)
	if !exists {
		return want, r.Create(ctx, want)
	}
	if got.Annotations[annotationSpecHash] == hash {
		return got, nil
	}
	got.Labels, got.Spec = want.Labels, want.Spec
	metav1.SetMetaDataAnnotation(&got.ObjectMeta, annotationSpecHash, hash)
	return got, r.Update(ctx, got)
}

func specHash(cj *batchv1.CronJob) (string, error) {
	raw, err := json.Marshal(struct {
		Labels map[string]string
		Spec   batchv1.CronJobSpec
	}{cj.Labels, cj.Spec})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8]), nil
}

// final reports whether a run's outcome can no longer change: it has one and
// it is not Running.
func final(outcome string) bool { return outcome != "" && outcome != model.RunOutcomeRunning }

// startRequested turns each run a person requested (a row with no outcome)
// into a Job. The Job's name comes from the row id, so a resync or restart
// between creating the Job and recording it finds the same Job again. A
// cancel asked for before the start ends the run without a Job; a job that
// cannot run now (blocked, invalid) fails the request with the reason.
func (r *Reconciler) startRequested(ctx context.Context, job *model.Job, v verdict, now time.Time) error {
	runs, err := r.Store.Jobs().Runs(ctx, job.ID)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.Outcome != "" || run.K8sJob != "" {
			continue
		}
		if run.CancelRequested {
			// A start cut short after its Secret was written leaves it with no Job to own it.
			if err := r.deleteSecret(ctx, job.Namespace, RunSecretName(ManualJobName(job, run.ID))); err != nil {
				return err
			}
			if err := r.endRequest(ctx, run, model.RunOutcomeCancelled, reasons.RunCancelled, now); err != nil {
				return err
			}
			continue
		}
		if v.code != "" {
			if err := r.endRequest(ctx, run, model.RunOutcomeFailed, v.code, now); err != nil {
				return err
			}
			continue
		}
		manual, err := BuildManualJob(r.inputs(job, v), run.ID, run.Parameters)

		if errors.Is(err, ErrParameterNotDeclared) {
			if err := r.endRequest(ctx, run, model.RunOutcomeFailed, reasons.ParametersInvalid, now); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		// The Secret comes first, so the run's pod never starts without it.
		unreadable, err := r.startSecret(ctx, job, v, manual.Name)
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
		if err := r.Create(ctx, manual); client.IgnoreAlreadyExists(err) != nil {
			return err
		}
		if err := r.ownRunSecret(ctx, v, manual); err != nil {
			return err
		}
		run.K8sJob, run.Outcome, run.Reason = manual.Name, model.RunOutcomeRunning, string(reasons.Running)
		if _, err := r.Store.Jobs().RecordRun(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

// endRequest records a requested run that never got a Job.
func (r *Reconciler) endRequest(ctx context.Context, run model.Run, outcome string, code reasons.Code, now time.Time) error {
	run.Outcome, run.Reason, run.FinishTime = outcome, string(code), now
	_, err := r.Store.Jobs().RecordRun(ctx, run)
	return err
}

// recordRuns writes every run of the job's Jobs, and of the CronJob's
// skipped and missed starts, to job_runs; stops a Job whose run was asked to
// be cancelled; calls a run lost when its Job is gone; and keeps only the
// newest keepRuns finished runs (plus those whose Job still exists). It returns the Jobs whose run is final and
// may be released from their finalizer.
func (r *Reconciler) recordRuns(ctx context.Context, job *model.Job, now time.Time) ([]*batchv1.Job, error) {
	jobs, err := r.jobsOf(ctx, job)
	if err != nil {
		return nil, err
	}
	stored, err := r.Store.Jobs().Runs(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	byK8sJob := make(map[string]model.Run, len(stored))
	for _, run := range stored {
		if run.K8sJob != "" {
			byK8sJob[run.K8sJob] = run
		}
	}

	seen := map[string]bool{}
	var release []*batchv1.Job
	for i := range jobs.Items {
		k := &jobs.Items[i]
		seen[k.Name] = true
		old, known := byK8sJob[k.Name]
		// A final run stays as recorded: re-deriving it could replace the
		// outcome and reason with worse ones once the pods that explained it
		// are gone, and would list pods again on every reconcile.
		if known && final(old.Outcome) {
			release = append(release, k)
			continue
		}
		// A finished Job needs no hold: its run is recorded below in this same
		// pass, and a finalizer added now would only be removed again.
		if k.DeletionTimestamp.IsZero() && !Finished(k) && !slices.Contains(k.Finalizers, FinalizerRecordRun) {
			if err := r.addFinalizer(ctx, k); err != nil {
				return nil, err
			}
		}
		if known && old.CancelRequested && k.DeletionTimestamp.IsZero() && !Finished(k) {
			if err := r.Delete(ctx, k, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
				return nil, err
			}
		}
		run, err := r.deriveRun(ctx, k, now)
		if err != nil {
			return nil, err
		}
		run.ID, run.JobID = old.ID, job.ID
		if !known || !sameRun(old, run) {
			if _, err := r.Store.Jobs().RecordRun(ctx, run); err != nil {
				return nil, err
			}
		}
		if final(run.Outcome) {
			release = append(release, k)
		}
	}
	// A run recorded as Running whose Job is gone (deleted without the
	// finalizer, for example while the controller was not installed) cannot
	// finish any more. The cache may lag behind a Job just created, so the
	// API server has the last word.
	for _, old := range stored {
		if old.Outcome != model.RunOutcomeRunning || old.K8sJob == "" || seen[old.K8sJob] {
			continue
		}
		err := r.Reader.Get(ctx, client.ObjectKey{Namespace: job.Namespace, Name: old.K8sJob}, &batchv1.Job{})
		if err == nil {
			continue
		}
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		old.Outcome, old.Reason, old.FinishTime = model.RunOutcomeLost, string(reasons.RunLost), now
		if _, err := r.Store.Jobs().RecordRun(ctx, old); err != nil {
			return nil, err
		}
	}

	evs, err := r.cronJobEvents(ctx, job)
	if err != nil {
		return nil, err
	}
	// A run whose Job or CronJob Event is still listed stays: the next
	// reconcile would record it again as a new run (and notify a failure
	// again, or audit another prune of the same skip).
	live := make([]string, 0, len(jobs.Items)+len(evs))
	for i := range jobs.Items {
		live = append(live, jobs.Items[i].Name)
	}
	for _, e := range evs {
		run, ok := runFromEvent(job, e)
		if !ok {
			continue
		}
		live = append(live, run.K8sJob)
		if _, known := byK8sJob[run.K8sJob]; known {
			continue
		}
		run.JobID = job.ID
		if _, err := r.Store.Jobs().RecordRun(ctx, run); err != nil {
			return nil, err
		}
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

// deriveRun is DeriveRun with the Job's pods and (while it runs) their events.
// A Job that is being deleted while still active was cancelled.
func (r *Reconciler) deriveRun(ctx context.Context, job *batchv1.Job, now time.Time) (model.Run, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(job.Namespace), client.MatchingLabels{LabelJobName: job.Name}); err != nil {
		return model.Run{}, err
	}
	var evs []corev1.Event
	if !Finished(job) {
		for _, p := range pods.Items {
			e, err := r.eventsAbout(ctx, job.Namespace, "Pod", p.Name)
			if err != nil {
				return model.Run{}, err
			}
			evs = append(evs, e...)
		}
	}
	run := jobstatus.DeriveRun(job, pods.Items, evs, now)
	if !job.DeletionTimestamp.IsZero() && run.Outcome == model.RunOutcomeRunning {
		run.Outcome, run.Reason, run.Message = model.RunOutcomeCancelled, string(reasons.RunCancelled), ""
		run.FinishTime = job.DeletionTimestamp.Time
	}
	return run, nil
}

// Finished reports whether the Job has completed or failed.
func Finished(job *batchv1.Job) bool {
	return slices.ContainsFunc(job.Status.Conditions, func(c batchv1.JobCondition) bool {
		return c.Status == corev1.ConditionTrue && (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed)
	})
}

func (r *Reconciler) eventsAbout(ctx context.Context, ns, kind, name string) ([]corev1.Event, error) {
	var list corev1.EventList
	if err := r.List(ctx, &list, client.InNamespace(ns), client.MatchingFields{indexEventObject: kind + "/" + name}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (r *Reconciler) cronJobEvents(ctx context.Context, job *model.Job) ([]corev1.Event, error) {
	var list corev1.EventList
	if err := r.CronJobEvents.List(ctx, &list, client.InNamespace(job.Namespace),
		client.MatchingFields{indexEventObject: "CronJob/" + cronJobName(job)}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// runFromEvent maps the CronJob controller's overlap and missed-start events
// to runs. Such a run has no Kubernetes Job, so its K8sJob is a key derived
// from what the event says, and the same event always names the same run.
//
// The CronJob controller does not advance lastScheduleTime on a skip, so it
// emits JobAlreadyActive again on every later sync, and the recorder folds the
// repeats into one Event with a newer lastTimestamp. The skip is therefore
// keyed by the slot that was skipped: the latest cron time at or before the
// event's firstTimestamp.
func runFromEvent(job *model.Job, e corev1.Event) (model.Run, bool) {
	at := eventTime(e)
	switch e.Reason {
	case eventJobAlreadyActive:
		at = skippedSlot(job, e, at)
		return model.Run{
			K8sJob: fmt.Sprintf("skipped-%d", at.Unix()), Trigger: TriggerSchedule, ScheduledTime: at, FinishTime: at,
			Outcome: model.RunOutcomeSkipped, Reason: string(reasons.RunSkipped),
		}, true
	case eventMissSchedule:
		if sched, err := time.Parse(time.RFC1123Z, strings.TrimPrefix(e.Message, eventMissPrefix)); err == nil {
			at = sched
		}
		at = at.UTC()
		return model.Run{
			K8sJob: fmt.Sprintf("missed-%d", at.Unix()), Trigger: TriggerSchedule, ScheduledTime: at, FinishTime: at,
			Outcome: model.RunOutcomeMissed, Reason: string(reasons.RunMissed),
		}, true
	}
	return model.Run{}, false
}

// skippedSlot is the cron slot a JobAlreadyActive event is about, or the event
// time when the job has no schedule to work it out from.
func skippedSlot(job *model.Job, e corev1.Event, fallback time.Time) time.Time {
	if job.Spec.Schedule == nil {
		return fallback
	}
	first := e.FirstTimestamp.Time
	if first.IsZero() {
		first = fallback
	}
	slot, err := schedule.Previous(job.Spec.Schedule.Cron, job.Spec.Schedule.TimeZone, first)
	if err != nil || slot.IsZero() {
		return fallback
	}
	return slot
}

// eventTime is when the event last happened.
func eventTime(e corev1.Event) time.Time {
	for _, t := range []time.Time{e.LastTimestamp.Time, e.EventTime.Time, e.FirstTimestamp.Time, e.CreationTimestamp.Time} {
		if !t.IsZero() {
			return t.UTC()
		}
	}
	return time.Time{}
}

func (r *Reconciler) addFinalizer(ctx context.Context, job *batchv1.Job) error {
	base := job.DeepCopy()
	controllerutil.AddFinalizer(job, FinalizerRecordRun)
	return r.Patch(ctx, job, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

func (r *Reconciler) removeFinalizer(ctx context.Context, job *batchv1.Job) error {
	if !controllerutil.ContainsFinalizer(job, FinalizerRecordRun) {
		return nil
	}
	base := job.DeepCopy()
	controllerutil.RemoveFinalizer(job, FinalizerRecordRun)
	return client.IgnoreNotFound(r.Patch(ctx, job, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})))
}

// jobsOf lists the job's Jobs, scheduled and manual.
func (r *Reconciler) jobsOf(ctx context.Context, job *model.Job) (batchv1.JobList, error) {
	var jobs batchv1.JobList
	err := r.List(ctx, &jobs, client.InNamespace(job.Namespace), client.MatchingLabels{model.LabelJob: job.ID})
	return jobs, err
}

// finalize removes what a deleted job left in the cluster, then the row. Its
// runs go with the row, so a Job held by the record-run finalizer has nowhere
// to be recorded and is released first.
func (r *Reconciler) finalize(ctx context.Context, job *model.Job) error {
	var cronJobs batchv1.CronJobList
	if err := r.List(ctx, &cronJobs, client.InNamespace(job.Namespace), client.MatchingLabels{model.LabelJob: job.ID}); err != nil {
		return err
	}
	for i := range cronJobs.Items {
		if err := r.Delete(ctx, &cronJobs.Items[i]); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	jobs, err := r.jobsOf(ctx, job)
	if err != nil {
		return err
	}
	for i := range jobs.Items {
		k := &jobs.Items[i]
		if err := r.removeFinalizer(ctx, k); err != nil {
			return err
		}
		if err := r.Delete(ctx, k, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
			return err
		}
		// Its owner takes it too; deleting it here does not wait for that.
		if err := r.deleteSecret(ctx, job.Namespace, RunSecretName(k.Name)); err != nil {
			return err
		}
	}
	if err := r.deleteSecret(ctx, job.Namespace, ScheduledSecretName(job)); err != nil {
		return err
	}
	if err := r.dropToken(ctx, job); err != nil {
		return err
	}
	err = r.Store.Jobs().Purge(ctx, system, job.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

// writeStatus updates the status columns only when they differ. Conditions
// that keep their status keep their transition time, and conditions this
// reconciler does not own (Notified) are kept, so a repeat reconcile writes
// nothing.
func (r *Reconciler) writeStatus(ctx context.Context, job *model.Job, v verdict, cj *batchv1.CronJob, now time.Time) error {
	next := job.Status
	next.Phase, next.ObservedVersion = v.phase, job.Version
	next.NextRunTimes, next.LastScheduleTime = nil, time.Time{}
	for _, t := range v.next {
		next.NextRunTimes = append(next.NextRunTimes, t.UTC())
	}
	if cj != nil && cj.Status.LastScheduleTime != nil {
		next.LastScheduleTime = cj.Status.LastScheduleTime.UTC()
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
	default:
		out = append(out, cond(model.ConditionScheduled, metav1.ConditionTrue, reasons.Ready))
	}
	return out
}

// Sweep deletes the CronJobs and Jobs that carry a job id no row has any
// more: what a job left behind when its row was removed without the
// controller running. See sweep.Orphans for what it touches.
func (r *Reconciler) Sweep(ctx context.Context) error {
	return sweep.Orphans{
		Client: r.Client, Label: model.LabelJob, ManagedOnly: r.ManagedNamespacesOnly,
		// A Job holds the record-run finalizer and its pods outlive it unless
		// the delete cascades; finalize does the same for a job being deleted.
		Release: func(ctx context.Context, obj client.Object) error {
			if k, ok := obj.(*batchv1.Job); ok {
				return r.removeFinalizer(ctx, k)
			}
			return nil
		},
		DeleteOptions: []client.DeleteOption{client.PropagationPolicy(metav1.DeletePropagationBackground)},
		Live: func(ctx context.Context) (map[string]bool, error) {
			jobs, err := r.Store.Jobs().ListAll(ctx)
			live := make(map[string]bool, len(jobs))
			for _, j := range jobs {
				live[j.ID] = true
			}
			return live, err
		},
	}.Delete(ctx, &batchv1.CronJobList{}, &batchv1.JobList{})
}

// SetupWithManager wires the controller: job rows (and the run requests,
// templates and people they depend on) come from the database through feed;
// CronJobs, Jobs and Events about CronJobs come from the cluster and map back
// to their job by the id label. It also adds the orphan sweep. The caller
// runs feed.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	if r.Store == nil || r.Reader == nil || r.Access == nil || r.Now == nil || r.RunnerImage.Ref == "" || r.CronJobEvents == nil ||
		r.Storage.Homes == nil || r.Storage.Folders == nil || r.Secrets == nil {
		return errors.New("jobs reconciler needs Store, Reader, Access, Now, RunnerImage, CronJobEvents, Storage and Secrets")
	}
	ctx := context.Background()
	eventKey := func(o client.Object) []string {
		e := o.(*corev1.Event)
		return []string{e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name}
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &corev1.Event{}, indexEventObject, eventKey); err != nil {
		return err
	}
	if err := r.CronJobEvents.IndexField(ctx, &corev1.Event{}, indexEventObject, eventKey); err != nil {
		return err
	}

	byJobLabel := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		return dbsource.Request(o.GetLabels()[model.LabelJob])
	})
	cronJobEvents := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		e := o.(*corev1.Event)
		cj := &batchv1.CronJob{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: e.Namespace, Name: e.InvolvedObject.Name}, cj); err != nil {
			return nil
		}
		return dbsource.Request(cj.Labels[model.LabelJob])
	})
	isCronJobEvent := predicate.NewPredicateFuncs(func(o client.Object) bool {
		e, ok := o.(*corev1.Event)
		return ok && e.InvolvedObject.Kind == "CronJob" && (e.Reason == eventJobAlreadyActive || e.Reason == eventMissSchedule)
	})
	if err := mgr.Add(&sweep.Runner{What: "jobs", Sweep: r.Sweep, Every: r.ResyncEvery, Changes: feed.Subscribe()}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).Named("job").
		WatchesRawSource(dbsource.For(feed.Subscribe(), "jobs", r.ResyncEvery, r.allJobs)).
		WatchesRawSource(dbsource.Fan(feed.Subscribe(), "job_runs", r.jobOfRun)).
		WatchesRawSource(dbsource.Fan(feed.Subscribe(), "templates", r.jobsOfTemplate)).
		WatchesRawSource(dbsource.Fan(feed.Subscribe(), "people", r.jobsOfPerson)).
		Watches(&batchv1.Job{}, byJobLabel, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
			_, ok := o.GetLabels()[model.LabelJob]
			return ok
		}))).
		Watches(&batchv1.CronJob{}, byJobLabel).
		WatchesRawSource(source.Kind[client.Object](r.CronJobEvents, &corev1.Event{}, cronJobEvents, isCronJobEvent)).
		Complete(r)
}

func jobID(j *model.Job) string { return j.ID }

// allJobs lists every job row, also those marked deleted: they still have
// objects to remove.
func (r *Reconciler) allJobs(ctx context.Context) ([]reconcile.Request, error) {
	jobs, err := r.Store.Jobs().ListAll(ctx)
	return dbsource.Requests(jobs, jobID, nil), err
}

// jobOfRun is the job a changed run row belongs to: a new request or cancel
// wakes it.
func (r *Reconciler) jobOfRun(ctx context.Context, runID string) ([]reconcile.Request, error) {
	run, err := r.Store.Jobs().Run(ctx, runID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return dbsource.Request(run.JobID), err
}

// jobsOfTemplate is every job that runs on the template, which just changed.
func (r *Reconciler) jobsOfTemplate(ctx context.Context, name string) ([]reconcile.Request, error) {
	jobs, err := r.Store.Jobs().ListAll(ctx)
	return dbsource.Requests(jobs, jobID, func(j *model.Job) bool { return j.Spec.Template == name }), err
}

// jobsOfPerson is the jobs of the person whose row just changed: a new uid or
// a new groups snapshot can unblock, or block, them.
func (r *Reconciler) jobsOfPerson(ctx context.Context, id string) ([]reconcile.Request, error) {
	person, err := r.Store.People(store.IDRange{}).ByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	jobs, err := r.Store.Jobs().List(ctx, person.Name)
	return dbsource.Requests(jobs, jobID, nil), err
}

// apiSecretName is the Secret holding the API token of job.
func apiSecretName(job *model.Job) string { return cronJobName(job) + "-api" }

// syncToken gives a job whose template gives an API token (as for apps,
// model.DefaultAPIAccess) its owner's token, made once and kept in a Secret
// beside its CronJob; a job whose template gives none has its token revoked.
func (r *Reconciler) syncToken(ctx context.Context, job *model.Job, tpl *model.Template) (API, error) {
	scope := token.ScopeFor(model.DefaultAPIAccess(tpl))
	if scope == "" {
		return API{}, r.dropToken(ctx, job)
	}
	if r.Tokens == nil {
		return API{}, errors.New("the template gives jobs an API token, but the controller has no token store")
	}
	name := apiSecretName(job)
	sec := &corev1.Secret{}
	err := r.Reader.Get(ctx, client.ObjectKey{Namespace: job.Namespace, Name: name}, sec)
	switch {
	case apierrors.IsNotFound(err):
		sec = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: job.Namespace}}
	case err != nil:
		return API{}, err
	default:
		id := sec.Annotations[model.AnnotationTokenID]
		if id != "" && sec.Annotations[model.AnnotationTokenScope] == string(scope) && len(sec.Data[APITokenKey]) > 0 {
			// A Secret can name a token whose record is gone (`javo user remove`),
			// or one made before tokens recorded their job, which is not kept
			// from the owner's secrets: both are made again.
			switch tok, err := r.Tokens.Get(ctx, id); {
			case errors.Is(err, token.ErrNotFound):
			case err != nil:
				return API{}, err
			case tok.Job == job.ID:
				return API{URL: r.APIURL, Internal: r.APIInternalURL, Secret: name}, nil
			}
		}
	}
	text, tok, err := r.Tokens.Create(ctx, system, token.Token{Name: "job " + job.Name, Owner: job.Owner, Scope: scope, Job: job.ID})
	if err != nil {
		return API{}, fmt.Errorf("make the job's API token: %w", err)
	}
	if old := sec.Annotations[model.AnnotationTokenID]; old != "" {
		if _, err := r.Tokens.Revoke(ctx, system, job.Owner, old); err != nil && !errors.Is(err, token.ErrNotFound) {
			return API{}, err
		}
	}
	sec.Labels = labels(job, TriggerSchedule)
	metav1.SetMetaDataAnnotation(&sec.ObjectMeta, model.AnnotationTokenID, tok.ID)
	metav1.SetMetaDataAnnotation(&sec.ObjectMeta, model.AnnotationTokenScope, string(scope))
	sec.Data = map[string][]byte{APITokenKey: []byte(text)}
	if sec.ResourceVersion == "" {
		err = r.Create(ctx, sec)
	} else {
		err = r.Update(ctx, sec)
	}
	if err != nil {
		return API{}, fmt.Errorf("store the job's API token: %w", err)
	}
	return API{URL: r.APIURL, Internal: r.APIInternalURL, Secret: name}, nil
}

// dropToken revokes a job's API token and deletes its Secret, if it has one.
func (r *Reconciler) dropToken(ctx context.Context, job *model.Job) error {
	sec := &corev1.Secret{}
	err := r.Reader.Get(ctx, client.ObjectKey{Namespace: job.Namespace, Name: apiSecretName(job)}, sec)
	if apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	if sec.Labels[model.LabelJob] != job.ID {
		return nil
	}
	if id := sec.Annotations[model.AnnotationTokenID]; id != "" && r.Tokens != nil {
		if _, err := r.Tokens.Revoke(ctx, system, job.Owner, id); err != nil && !errors.Is(err, token.ErrNotFound) {
			return err
		}
	}
	return client.IgnoreNotFound(r.Delete(ctx, sec))
}

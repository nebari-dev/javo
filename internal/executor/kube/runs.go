package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/nebari-dev/javo/internal/controller/namespace"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podread"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/workload"
)

const (
	// LabelTrigger says on a run's Job and pods what started it.
	LabelTrigger    = model.LabelTrigger
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"

	// FinalizerRecordRun holds a run's Job until Stop lets go of it: until
	// its caller has recorded how the run ended.
	FinalizerRecordRun = model.Group + "/record-run"

	// runTTL is how long the Job of a run that ended is kept, for its
	// pod's log.
	runTTL = time.Hour
)

// Runs runs each run (workload.Batch) as a Job, named and labelled after
// its Ref, whose ID is the job's. It reads and writes through its
// Executor's clients and applies its isolation.
type Runs struct{ e *Executor }

// Runs is the executor for runs on the cluster e runs apps on.
func (e *Executor) Runs() Runs { return Runs{e} }

var _ executor.Executor = Runs{}

// RenderRun is the Job that runs s once: its pod as Render makes it, held
// by FinalizerRecordRun.
func RenderRun(s workload.Spec, iso podspec.Isolation) *batchv1.Job {
	pod := Render(s, iso)
	labels := func() map[string]string {
		l := map[string]string{model.LabelOwner: s.Ref.Owner, model.LabelJob: s.Ref.ID, LabelTrigger: TriggerManual}
		if !s.Batch.Slot.IsZero() {
			l[LabelTrigger] = TriggerSchedule
		}
		return l
	}
	annotations := map[string]string{annotationInstance: "manual"}
	if !s.Batch.Slot.IsZero() {
		annotations = map[string]string{annotationScheduled: s.Batch.Slot.UTC().Format(time.RFC3339)}
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: s.Ref.Name, Namespace: s.Ref.Namespace,
			Labels: labels(), Annotations: annotations,
			Finalizers: []string{FinalizerRecordRun},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(s.Batch.Retries),
			TTLSecondsAfterFinished: ptr.To(int32(runTTL.Seconds())),
			PodFailurePolicy: &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{
				{
					Action: batchv1.PodFailurePolicyActionIgnore,
					OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{
						{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue},
					},
				},
				{
					Action: batchv1.PodFailurePolicyActionFailJob,
					OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{
						ContainerName: ptr.To(podspec.ContainerName),
						Operator:      batchv1.PodFailurePolicyOnExitCodesOpIn,
						Values:        s.Batch.FailNow,
					},
				},
			}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels(), Annotations: pod.Annotations},
				Spec:       pod.Spec,
			},
		},
	}
	if t := s.Batch.Timeout; t > 0 {
		job.Spec.ActiveDeadlineSeconds = ptr.To(int64(t.Seconds()))
	}
	return job
}

// runKey keys the Jobs Start made that the cache has not shown yet, apart
// from the Pods of apps.
type runKey client.ObjectKey

// Start creates the Job that runs s.
func (r Runs) Start(ctx context.Context, s workload.Spec) error {
	if s.Batch == nil {
		return fmt.Errorf("%s/%s is not a run", s.Ref.Namespace, s.Ref.Name)
	}
	if err := r.e.Client.Create(ctx, RenderRun(s, r.e.Isolation)); client.IgnoreAlreadyExists(err) != nil {
		return err
	}
	// Made now, or by an earlier Start the cache lagged behind.
	r.e.started.Store(runKey(key(s.Ref)), struct{}{})
	return nil
}

// job returns the run's Job, or nil. The cache may not show one Start
// just made, so the cluster has the last word on it. A Job of that name
// that carries another job's id (or none) is an error wrapping
// executor.ErrForeign.
func (r Runs) job(ctx context.Context, ref workload.Ref) (*batchv1.Job, error) {
	job := &batchv1.Job{}
	err := r.e.Client.Get(ctx, key(ref), job)
	if _, made := r.e.started.Load(runKey(key(ref))); made {
		if apierrors.IsNotFound(err) {
			err = r.e.Reader.Get(ctx, key(ref), job)
		}
		if err == nil || apierrors.IsNotFound(err) {
			// The cache shows it now, or it is gone again.
			r.e.started.Delete(runKey(key(ref)))
		}
	}
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, err
	case job.Labels[model.LabelJob] != ref.ID:
		return nil, fmt.Errorf("job %s/%s %w %s", job.Namespace, job.Name, executor.ErrForeign, ref.ID)
	}
	return job, nil
}

func finished(job *batchv1.Job) bool {
	return jobCond(job, batchv1.JobComplete) != nil || jobCond(job, batchv1.JobFailed) != nil
}

// hold changes whether the Job carries FinalizerRecordRun.
func (r Runs) hold(ctx context.Context, job *batchv1.Job, held bool) error {
	if controllerutil.ContainsFinalizer(job, FinalizerRecordRun) == held {
		return nil
	}
	base := job.DeepCopy()
	if held {
		controllerutil.AddFinalizer(job, FinalizerRecordRun)
	} else {
		controllerutil.RemoveFinalizer(job, FinalizerRecordRun)
	}
	return r.e.Client.Patch(ctx, job, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

// Stop deletes the Job of a run that is going, which stays, held, to be
// observed as stopped. A run that has ended is let go of: its Job goes
// when it was deleted, or else at the end of its time to live. Its pods
// go with the Job.
func (r Runs) Stop(ctx context.Context, ref workload.Ref) error {
	job, err := r.job(ctx, ref)
	if errors.Is(err, executor.ErrForeign) || (err == nil && job == nil) {
		return nil
	}
	if err != nil {
		return err
	}
	if finished(job) || !job.DeletionTimestamp.IsZero() {
		return client.IgnoreNotFound(r.hold(ctx, job, false))
	}
	return client.IgnoreNotFound(r.e.Client.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)))
}

// Observe reads the run's state off its Job, the pod of each attempt and,
// while it is going, the events about them. A Job that is going is held
// from here on, also one Start did not make.
func (r Runs) Observe(ctx context.Context, ref workload.Ref) (*workload.State, error) {
	if r.e.ManagedOnly {
		if managed, err := namespace.IsManaged(ctx, r.e.Client, ref.Namespace); err != nil {
			return nil, err
		} else if !managed {
			return nil, fmt.Errorf("namespace %s %w", ref.Namespace, executor.ErrUnmanaged)
		}
	}
	job, err := r.job(ctx, ref)
	if err != nil || job == nil {
		return nil, err
	}
	seen := SeenRun{Job: job}
	var pods corev1.PodList
	if err := r.e.Client.List(ctx, &pods, client.InNamespace(job.Namespace), client.MatchingLabels{LabelJobName: job.Name}); err != nil {
		return nil, err
	}
	seen.Pods = pods.Items
	if !finished(job) {
		if job.DeletionTimestamp.IsZero() {
			if err := r.hold(ctx, job, true); err != nil {
				return nil, err
			}
		}
		for _, p := range pods.Items {
			var evs corev1.EventList
			if err := r.e.Client.List(ctx, &evs, client.InNamespace(p.Namespace), client.MatchingFields{indexEventPod: p.Name}); err != nil {
				return nil, err
			}
			for _, ev := range evs.Items {
				if ev.InvolvedObject.Kind == "Pod" {
					seen.Events = append(seen.Events, ev)
				}
			}
		}
	}
	return ObserveRun(seen), nil
}

// List names every run that has a Job. With ManagedOnly, those in other
// namespaces are left out.
func (r Runs) List(ctx context.Context) ([]workload.Ref, error) {
	return r.e.list(ctx, model.LabelJob, &batchv1.JobList{})
}

// Watch reports the run of every Job that changes.
func (r Runs) Watch(ctx context.Context, changed func(workload.Ref)) error {
	return r.e.inform(ctx, watched{&batchv1.Job{}, labelled(model.LabelJob, changed)})
}

// Addr is an error: a run serves nothing.
func (r Runs) Addr(_ context.Context, ref workload.Ref, _ int32) (string, error) {
	return "", fmt.Errorf("run %s/%s has no address", ref.Namespace, ref.Name)
}

// Logs opens the log of the container o.Unit (the main one when empty) of
// the run's newest attempt, or returns nil while that has none to read.
func (r Runs) Logs(ctx context.Context, ref workload.Ref, o executor.LogOptions) (io.ReadCloser, error) {
	var pods corev1.PodList
	if err := r.e.Client.List(ctx, &pods, client.InNamespace(ref.Namespace), client.MatchingLabels{
		LabelJobName: ref.Name, model.LabelOwner: ref.Owner, model.LabelJob: ref.ID,
	}); err != nil {
		return nil, err
	}
	if o.Unit == "" {
		o.Unit = podspec.ContainerName
	}
	pod := lastPod(pods.Items)
	if pod == nil || !podread.HasLog(podread.ContainerStatus(pod, o.Unit), o.Previous) {
		return nil, nil
	}
	if r.e.PodLogs == nil {
		return nil, errors.New("no pod log reader configured")
	}
	return r.e.PodLogs.Stream(ctx, pod.Namespace, pod.Name, logOptions(o))
}

// What earlier versions kept for a job in Secrets called after it: the
// secrets its scheduled runs read (scheduledSecretsSuffix, and
// legacySecretsSuffix before that, which also ended the name of the Secret
// of a run started by hand), and its API token (legacyAPISuffix).
const (
	scheduledSecretsSuffix = ".job-secrets"
	legacySecretsSuffix    = "-" + model.ProductName + "-secrets"
	legacyAPISuffix        = "-api"
)

// Tidy deletes the Secrets a job, or one of its runs, had under those
// names; the caller makes sure no run of the job reads them any more.
// The id of the API token the old token Secret names goes to revoke first.
func (r Runs) Tidy(ctx context.Context, ref workload.Ref, revoke func(ctx context.Context, tokenID string) error) error {
	for _, suffix := range []string{scheduledSecretsSuffix, legacySecretsSuffix, legacyAPISuffix} {
		sec, err := ownedSecret(ctx, r.e.Reader, model.LabelJob, ref, ref.Name+suffix)
		if errors.Is(err, executor.ErrForeign) || (err == nil && sec == nil) {
			continue
		}
		if err != nil {
			return err
		}
		if id := sec.Annotations[model.AnnotationTokenID]; suffix == legacyAPISuffix && id != "" {
			if err := revoke(ctx, id); err != nil {
				return err
			}
		}
		if err := r.e.Client.Delete(ctx, sec, client.Preconditions{UID: &sec.UID}); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

// Schedules names the jobs whose schedule a CronJob of an earlier version
// still fires.
func (r Runs) Schedules(ctx context.Context) ([]workload.Ref, error) {
	return r.e.list(ctx, model.LabelJob, &batchv1.CronJobList{})
}

// Retire removes the CronJob that fired the job's schedule in earlier
// versions, without ending a run it started. It stops the CronJob firing,
// takes it away as owner of its Jobs (which then go at the end of their
// time to live, like the runs Start makes) and of the Secret their pods
// read, hands keep the time up to which it started the schedule's slots
// (the last one it fired, or else when it was made) and only then deletes
// it. A job with no CronJob is left as it is and keep is not called.
func (r Runs) Retire(ctx context.Context, ref workload.Ref, keep func(through time.Time) error) error {
	cj := &batchv1.CronJob{}
	if mine, err := r.e.own(ctx, model.LabelJob, ref, cj); err != nil || !mine {
		return err
	}
	if !ptr.Deref(cj.Spec.Suspend, false) {
		base := cj.DeepCopy()
		cj.Spec.Suspend = ptr.To(true)
		if err := r.e.Client.Patch(ctx, cj, client.MergeFrom(base)); err != nil {
			return err
		}
	}
	through := cj.CreationTimestamp.Time
	if last := cj.Status.LastScheduleTime; last != nil {
		through = last.Time
	}
	var jobs batchv1.JobList
	if err := r.e.Client.List(ctx, &jobs, client.InNamespace(ref.Namespace), client.MatchingLabels{model.LabelJob: ref.ID}); err != nil {
		return err
	}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		base := job.DeepCopy()
		if !disown(job, cj.UID) {
			continue
		}
		// One it made a moment ago may not be in its status yet.
		if slot, err := time.Parse(time.RFC3339, job.Annotations[annotationScheduled]); err == nil && slot.After(through) {
			through = slot
		}
		if job.Spec.TTLSecondsAfterFinished == nil {
			job.Spec.TTLSecondsAfterFinished = ptr.To(int32(runTTL.Seconds()))
		}
		if err := r.e.Client.Patch(ctx, job, client.MergeFrom(base)); err != nil {
			return err
		}
	}
	sec, err := ownedSecret(ctx, r.e.Reader, model.LabelJob, ref, ref.Name+scheduledSecretsSuffix)
	if err != nil && !errors.Is(err, executor.ErrForeign) {
		return err
	}
	if err == nil && sec != nil {
		base := sec.DeepCopy()
		if disown(sec, cj.UID) {
			if err := r.e.Client.Patch(ctx, sec, client.MergeFrom(base)); err != nil {
				return err
			}
		}
	}
	if err := keep(through); err != nil {
		return err
	}
	return client.IgnoreNotFound(r.e.Client.Delete(ctx, cj, client.Preconditions{UID: &cj.UID},
		client.PropagationPolicy(metav1.DeletePropagationBackground)))
}

// disown takes the owner uid off obj and reports whether it was one.
func disown(obj client.Object, uid apitypes.UID) bool {
	refs := obj.GetOwnerReferences()
	kept := slices.DeleteFunc(slices.Clone(refs), func(o metav1.OwnerReference) bool { return o.UID == uid })
	obj.SetOwnerReferences(kept)
	return len(kept) != len(refs)
}

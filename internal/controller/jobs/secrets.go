package jobs

import (
	"context"
	"errors"
	"fmt"
	"slices"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/controller/secretenv"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// A job's runs get the owner's secrets as variables read by
// reference from a Secret, never from the CronJob or Job spec. Scheduled
// runs share one Secret the CronJob owns, rewritten when a value or binding
// changes, so the next run gets the change; a run started by hand gets one of
// its own, written with the values at its start and owned by its Job.
// Kubernetes deletes each with its owner.

// prepareSecrets puts the secrets the job's runs get in v. For a schedule
// that may run, it opens the values when the Secret its runs read is missing
// or out of date; values that cannot be opened block the job
// (SecretsUnreadable) rather than run it without them.
func (r *Reconciler) prepareSecrets(ctx context.Context, job *model.Job, v *verdict) error {
	all, err := r.Store.Secrets().ForJob(ctx, job.ID)
	if err != nil {
		return err
	}
	v.secrets = secretenv.For(ctx, all, authz.Object{Job: job})
	if job.Spec.Schedule == nil {
		return nil
	}
	if v.scheduled, err = r.jobSecret(ctx, job.Namespace, job.ID, ScheduledSecretName(job)); err != nil {
		return err
	}
	fp := v.secrets.Fingerprint
	if v.code != "" || fp == "" || (v.scheduled != nil && v.scheduled.Annotations[model.AnnotationSecrets] == fp) {
		return nil
	}
	data, unreadable := v.secrets.Open(ctx, r.SecretKeys)
	if unreadable != "" {
		*v = verdict{phase: model.JobPhaseBlocked, code: reasons.SecretsUnreadable, secrets: v.secrets, scheduled: v.scheduled}
		return nil
	}
	v.scheduledData = data
	return nil
}

// syncScheduledSecret makes the Secret the CronJob's runs read match v: the
// values prepareSecrets opened, owned by cj; none when the runs get no
// secret. A blocked job's Secret is left as it is.
func (r *Reconciler) syncScheduledSecret(ctx context.Context, job *model.Job, v verdict, cj *batchv1.CronJob) error {
	got := v.scheduled
	switch {
	case job.Spec.Schedule == nil:
		return nil
	case v.secrets.Fingerprint == "":
		if got == nil {
			return nil
		}
		return client.IgnoreNotFound(r.Secrets.Delete(ctx, got))
	case v.code != "" || cj == nil || cj.UID == "":
		return nil
	case v.scheduledData != nil:
		return r.putSecret(ctx, job, ScheduledSecretName(job), TriggerSchedule, v.secrets.Fingerprint, v.scheduledData, ownerOf(cj, "CronJob"))
	}
	// Up to date; a CronJob made again since needs to own it.
	return r.setOwner(ctx, got, ownerOf(cj, "CronJob"))
}

// startSecret writes the Secret the run whose Job will be k8sJob reads, with
// the values now. unreadable is why they could not be opened (the run then
// does not start). A run whose job gets no secret has none.
func (r *Reconciler) startSecret(ctx context.Context, job *model.Job, v verdict, k8sJob string) (unreadable string, err error) {
	if v.secrets.Fingerprint == "" {
		return "", nil
	}
	data, unreadable := v.secrets.Open(ctx, r.SecretKeys)
	if unreadable != "" {
		return unreadable, nil
	}
	return "", r.putSecret(ctx, job, RunSecretName(k8sJob), TriggerManual, v.secrets.Fingerprint, data, nil)
}

// ownRunSecret hands the run's Secret to its Job, so it goes with the run.
func (r *Reconciler) ownRunSecret(ctx context.Context, v verdict, k *batchv1.Job) error {
	if v.secrets.Fingerprint == "" {
		return nil
	}
	if k.UID == "" {
		if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(k), k); err != nil {
			return err
		}
	}
	sec, err := r.jobSecret(ctx, k.Namespace, k.Labels[model.LabelJob], RunSecretName(k.Name))
	if err != nil || sec == nil {
		return err
	}
	return r.setOwner(ctx, sec, ownerOf(k, "Job"))
}

// errForeignSecret means a Secret at one of a job's names carries another
// job's id, or none: it is never read, written or deleted for the job.
var errForeignSecret = errors.New("exists and does not belong to the job")

// jobSecret reads the Secret name live (Secrets are never cached): nil when
// there is none, an error wrapping errForeignSecret when it is not labelled
// as the job jobID's.
func (r *Reconciler) jobSecret(ctx context.Context, namespace, jobID, name string) (*corev1.Secret, error) {
	sec := &corev1.Secret{}
	err := r.Secrets.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, sec)
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, err
	case jobID == "" || sec.Labels[model.LabelJob] != jobID:
		return nil, fmt.Errorf("secret %s/%s %w %s", namespace, name, errForeignSecret, jobID)
	}
	return sec, nil
}

// putSecret creates or replaces the Secret name with data, labelled as the
// job's and marked with the fingerprint (never a value), owned by owner when
// there is one. No error holds a value: the API server's errors name the
// object, never its data.
func (r *Reconciler) putSecret(ctx context.Context, job *model.Job, name, trigger, fingerprint string, data map[string][]byte, owner *metav1.OwnerReference) error {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: job.Namespace, Labels: labels(job, trigger),
			Annotations: map[string]string{model.AnnotationSecrets: fingerprint}},
		Type: corev1.SecretTypeOpaque, Data: data,
	}
	if owner != nil {
		sec.OwnerReferences = []metav1.OwnerReference{*owner}
	}
	err := r.Secrets.Create(ctx, sec)
	if apierrors.IsAlreadyExists(err) {
		got, err := r.jobSecret(ctx, job.Namespace, job.ID, name)
		if err != nil {
			return err
		}
		if got == nil {
			return fmt.Errorf("secret %s/%s vanished while it was written", job.Namespace, name)
		}
		got.Labels, got.Annotations, got.Type, got.Data = sec.Labels, sec.Annotations, sec.Type, sec.Data
		if owner != nil {
			got.OwnerReferences = sec.OwnerReferences
		}
		err = r.Secrets.Update(ctx, got)
	}
	if err != nil {
		return fmt.Errorf("store the job's secrets: %w", err)
	}
	return nil
}

// setOwner makes owner the one owner of sec.
func (r *Reconciler) setOwner(ctx context.Context, sec *corev1.Secret, owner *metav1.OwnerReference) error {
	if len(sec.OwnerReferences) == 1 && sec.OwnerReferences[0].UID == owner.UID {
		return nil
	}
	base := sec.DeepCopy()
	sec.OwnerReferences = []metav1.OwnerReference{*owner}
	return client.IgnoreNotFound(r.Secrets.Patch(ctx, sec, client.MergeFrom(base)))
}

func ownerOf(o client.Object, kind string) *metav1.OwnerReference {
	return &metav1.OwnerReference{APIVersion: batchv1.SchemeGroupVersion.String(), Kind: kind, Name: o.GetName(), UID: o.GetUID()}
}

// deleteSecret removes the job's Secret name if it is there. One that is not
// the job's is left alone.
func (r *Reconciler) deleteSecret(ctx context.Context, job *model.Job, name string) error {
	sec, err := r.jobSecret(ctx, job.Namespace, job.ID, name)
	if errors.Is(err, errForeignSecret) {
		log.FromContext(ctx).Info("not deleting a Secret of another owner", "secret", name)
		return nil
	}
	if err != nil || sec == nil {
		return err
	}
	return client.IgnoreNotFound(r.Secrets.Delete(ctx, sec, client.Preconditions{UID: &sec.UID}))
}

// deleteRunSecret removes the Secret of the run whose Kubernetes Job is
// k8sJob, under its name and the one it had before RunSecretName.
func (r *Reconciler) deleteRunSecret(ctx context.Context, job *model.Job, k8sJob string) error {
	for _, name := range []string{RunSecretName(k8sJob), k8sJob + legacySecretsSuffix} {
		if err := r.deleteSecret(ctx, job, name); err != nil {
			return err
		}
	}
	return nil
}

// dropLegacySecrets removes the job's Secrets under their names before
// ScheduledSecretName and apiSecretName once no run of the job is still
// going (a run's pod may still be waiting to read them), revoking the token
// the old API Secret held. A run's own old Secret goes with its Job.
func (r *Reconciler) dropLegacySecrets(ctx context.Context, job *model.Job) error {
	jobs, err := r.jobsOf(ctx, job)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(jobs.Items, func(k batchv1.Job) bool { return !finished(&k) }) {
		return nil
	}
	return r.removeLegacySecrets(ctx, job)
}

// removeLegacySecrets deletes the job's old scheduled and API Secrets.
func (r *Reconciler) removeLegacySecrets(ctx context.Context, job *model.Job) error {
	if err := r.deleteSecret(ctx, job, cronJobName(job)+legacySecretsSuffix); err != nil {
		return err
	}
	return r.dropTokenSecret(ctx, job, legacyAPISecretName(job))
}

// finished is true once the Job has completed or failed for good.
func finished(k *batchv1.Job) bool {
	for _, c := range k.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

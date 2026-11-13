package jobs

import (
	"context"
	"fmt"
	"slices"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/controller/secretenv"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/workload"
)

// A job's runs get the owner's secrets as variables read by reference from
// a bundle (executor.Secrets), never from the run's Spec. Scheduled runs
// share one bundle that goes with the CronJob, rewritten when a value or
// binding changes, so the next run gets the change; a run started by hand
// gets one of its own, written with the values at its start, that goes with
// the run.

// prepareSecrets puts the secrets the job's runs get in v. For a schedule
// that may run, it opens the values when the bundle its runs read is missing
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
	if v.scheduled, err = r.Secrets.Get(ctx, jobRef(job), ScheduledSecretName(job)); err != nil {
		return err
	}
	fp := v.secrets.Fingerprint
	if v.code != "" || fp == "" || (v.scheduled != nil && v.scheduled.Meta[model.AnnotationSecrets] == fp) {
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

// syncScheduledSecret makes the bundle the CronJob's runs read match v: the
// values prepareSecrets opened, going with cj; none when the runs get no
// secret. A blocked job's bundle is left as it is.
func (r *Reconciler) syncScheduledSecret(ctx context.Context, job *model.Job, v verdict, cj *batchv1.CronJob) error {
	name := ScheduledSecretName(job)
	switch {
	case job.Spec.Schedule == nil:
		return nil
	case v.secrets.Fingerprint == "":
		if v.scheduled == nil {
			return nil
		}
		return r.Secrets.Delete(ctx, jobRef(job), name)
	case v.code != "" || cj == nil || cj.UID == "":
		return nil
	case v.scheduledData != nil:
		return r.putSecrets(ctx, jobRef(job), name, v.secrets.Fingerprint, v.scheduledData)
	}
	// Up to date; a CronJob made again since needs to own it.
	return r.putSecrets(ctx, jobRef(job), name, v.secrets.Fingerprint, v.scheduled.Data)
}

// startSecret writes the bundle the run ref reads, with the values now.
// unreadable is why they could not be opened (the run then does not start).
// A run whose job gets no secret has none. It is written before the run
// starts, so the run never starts without it, and again after, so it goes
// with the run.
func (r *Reconciler) startSecret(ctx context.Context, v verdict, ref workload.Ref) (unreadable string, err error) {
	if v.secrets.Fingerprint == "" {
		return "", nil
	}
	data, unreadable := v.secrets.Open(ctx, r.SecretKeys)
	if unreadable != "" {
		return unreadable, nil
	}
	return "", r.putSecrets(ctx, ref, RunSecretName(ref.Name), v.secrets.Fingerprint, data)
}

// putSecrets makes the bundle name of ref hold data, marked with the
// fingerprint (never a value). It can be made again, so it goes with what
// ref names. No error holds a value.
func (r *Reconciler) putSecrets(ctx context.Context, ref workload.Ref, name, fingerprint string, data map[string][]byte) error {
	err := r.Secrets.Put(ctx, ref, name, executor.Bundle{
		Data: data, Meta: map[string]string{model.AnnotationSecrets: fingerprint}, Rebuildable: true,
	})
	if err != nil {
		return fmt.Errorf("store the job's secrets: %w", err)
	}
	return nil
}

// going is true for a run the executor still has to end or let go of: one
// not done, or stopped and not yet recorded.
func going(st *workload.State) bool { return !st.Run.Done || st.Run.Stopped }

// dropLegacySecrets removes what the job kept under names no longer used
// once no run of the job is still going (a run may still be waiting to read
// it), revoking the token found there.
func (r *Reconciler) dropLegacySecrets(ctx context.Context, job *model.Job, seen []seenRun) error {
	if slices.ContainsFunc(seen, func(s seenRun) bool { return s.st != nil && going(s.st) }) {
		return nil
	}
	return r.tidy(ctx, job, jobRef(job))
}

// tidy removes what earlier versions kept for ref, the job or one of its
// runs, under names no longer used. A token found there is revoked when
// its record is the job's.
func (r *Reconciler) tidy(ctx context.Context, job *model.Job, ref workload.Ref) error {
	if r.Legacy == nil {
		return nil
	}
	return r.Legacy.Tidy(ctx, ref, func(ctx context.Context, id string) error { return r.revokeOwn(ctx, job, id) })
}

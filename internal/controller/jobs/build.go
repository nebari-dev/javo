// Package jobs renders job rows into the Kubernetes CronJobs and Jobs that
// run them and records every run in the database.
package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/nebari-dev/javo/internal/controller/secretenv"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/executor/kube"
	"github.com/nebari-dev/javo/internal/jobs/runner"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/podfiles"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/workload"
)

const (
	LabelTrigger = model.Group + "/trigger"
	// FinalizerRecordRun holds a Job until its run is written to the
	// database. Manual Jobs carry it from creation; scheduled Jobs get it from
	// the reconciler, because the CronJob controller copies only labels and
	// annotations from the jobTemplate.
	FinalizerRecordRun = model.Group + "/record-run"

	TriggerSchedule = "schedule"
	TriggerManual   = "manual"

	// cronJobNameMax is the Kubernetes limit for a CronJob name: its Jobs
	// append 11 characters to it.
	cronJobNameMax = 52
	manualJobTTL   = time.Hour

	// runnerDir holds the javo binary the init container copies for the app
	// container. It is not /javo: that path is the binary inside the image.
	runnerDir    = "/javo-run"
	runnerVolume = "javo-run"

	// LabelJobName is the label the Job controller puts on a Job's pods.
	LabelJobName = kube.LabelJobName
)

var (
	// ErrJobsNotAllowed means the template does not allow jobs.
	ErrJobsNotAllowed = errors.New("template does not allow jobs")
	// ErrEngineNotInTemplate means the template does not list the job's engine.
	ErrEngineNotInTemplate = errors.New("engine is not allowed by the template")
	// ErrKernelNotInTemplate means the template does not list the job's kernel.
	ErrKernelNotInTemplate = errors.New("kernel is not allowed by the template")
	// ErrParameterNotDeclared means a run overrides a parameter the job does not declare.
	ErrParameterNotDeclared = errors.New("parameter is not declared by the job")
)

// Inputs is everything needed to render a job. Layout (M3) joins it when the
// storage layout lands; until then the M1 pod builder supplies the home
// volume.
type Inputs struct {
	Job         *model.Job
	Template    *model.Template
	RunnerImage podspec.Image
	// Layout is what a run mounts and runs as: the owner's home, group
	// folders and scratch (podstorage), and the owner's uid.
	Layout podstorage.Layout
	// Secrets are the owner's secrets each run gets as variables, read by
	// reference from the run's Secret (ScheduledSecretName or RunSecretName).
	Secrets secretenv.Set
	// Size is what each run's container gets as CPU and memory requests and
	// limits; the zero Size keeps the template's resources.
	Size sizes.Size
	// API is the owner's API token for a job whose template gives one; the
	// zero value gives none.
	API API
	// Environment is the files of the picked environment each run installs
	// with PixiImage (internal/pixienv); nil runs the template's own
	// environment, if it has one.
	Environment *model.EnvironmentFiles
	PixiImage   string
	// MaxTimeout is the platform's longest run (limits.jobMaxTimeout); zero
	// leaves the job's and the template's time limits as they are.
	MaxTimeout time.Duration
	// Isolation is applied to every run's pod.
	Isolation podspec.Isolation
}

// A job's Secrets are named "<CronJob name>.job-<use>", a manual run's
// "<Job name>.run-secrets". Kubernetes names made from a person's pick are
// DNS labels and never hold a ".", so the part after it keeps apps' Secrets
// (".app-", workspace.SecretName), a job's and a run's apart whatever the
// names. legacySecretsSuffix ended both job and run Secrets before.
const legacySecretsSuffix = "-" + model.ProductName + "-secrets"

// ScheduledSecretName is the Secret the runs of the job's CronJob read their
// secrets from. The CronJob owns it, so it goes with the CronJob.
func ScheduledSecretName(j *model.Job) string { return cronJobName(j) + ".job-secrets" }

// RunSecretName is the Secret the run whose Kubernetes Job is k8sJob reads
// its secrets from: a run started by hand has its own, which that Job owns.
func RunSecretName(k8sJob string) string { return k8sJob + ".run-secrets" }

// API is where a run reaches the javo API and the Secret holding the
// owner's token for it (key APITokenKey). URL is the public address (the
// Host the API answers to); Internal, when set, is the gateway's address
// inside the cluster, for when pods cannot reach the public one.
type API struct {
	URL, Internal, Secret string
}

// APITokenKey is the key of the token in a job's API Secret.
const APITokenKey = "token"

// exitCodesFailNow are the runner exit codes (and 137, the kernel's OOM
// kill) that a retry cannot fix.
var exitCodesFailNow = []int32{
	runner.ExitUserError, runner.ExitKernelDied, runner.ExitTemplateError,
	runner.ExitSourceMissing, runner.ExitOutputWrite, runner.ExitParams, 137,
}

func cronJobName(j *model.Job) string {
	return naming.ObjectNameMax(j.Owner, j.Name, cronJobNameMax)
}

// ManualJobName is the name of the Kubernetes Job that runs the run row
// runID: the CronJob's name and a hash of the id. The same run always gets
// the same name, so creating it twice makes one Job. Pods copy the name into
// a label, so it stays within 63 characters.
func ManualJobName(j *model.Job, runID string) string {
	sum := sha256.Sum256([]byte(runID))
	return cronJobName(j) + "-" + hex.EncodeToString(sum[:])[:10]
}

func labels(j *model.Job, trigger string) map[string]string {
	return map[string]string{
		model.LabelOwner: j.Owner,
		model.LabelJob:   j.ID,
		LabelTrigger:     trigger,
	}
}

// Validate says whether the template allows the job: jobs on, engine and
// kernel listed. The errors wrap the Err* values above.
func Validate(in Inputs) error {
	return ValidateFor(in.Template, &in.Job.Spec)
}

// ValidateFor is Validate for a template row and a job's spec.
func ValidateFor(tpl *model.Template, spec *model.JobSpec) error {
	j := tpl.Spec.Jobs
	if j == nil {
		return fmt.Errorf("template %q: %w", tpl.Name, ErrJobsNotAllowed)
	}
	if !slices.Contains(j.Engines, spec.Engine) {
		return fmt.Errorf("template %q, engine %q: %w", tpl.Name, spec.Engine, ErrEngineNotInTemplate)
	}
	if k := spec.Kernel; k != "" && !slices.Contains(j.Kernels, k) {
		return fmt.Errorf("template %q, kernel %q: %w", tpl.Name, k, ErrKernelNotInTemplate)
	}
	return nil
}

// CheckOverrides refuses a run that overrides a parameter the job does not
// declare.
func CheckOverrides(declared []model.JobParameter, override map[string]string) error {
	for name := range override {
		if !slices.ContainsFunc(declared, func(p model.JobParameter) bool { return p.Name == name }) {
			return fmt.Errorf("parameter %q: %w", name, ErrParameterNotDeclared)
		}
	}
	return nil
}

// params applies overrides to the declared defaults; an override of an
// undeclared name is an error.
func params(j *model.Job, override map[string]string) ([]runner.Param, error) {
	if err := CheckOverrides(j.Spec.Parameters, override); err != nil {
		return nil, err
	}
	out := make([]runner.Param, 0, len(j.Spec.Parameters))
	for _, p := range j.Spec.Parameters {
		v, ok := override[p.Name]
		if !ok {
			v = p.Default
		}
		out = append(out, runner.Param{Name: p.Name, Type: p.Type, Value: v})
	}
	return out, nil
}

// timeout is the longest a run may take: the shortest of the job's, the
// template's and the platform's limits; zero when there is none.
func timeout(in Inputs) time.Duration {
	t := time.Duration(0)
	if r := in.Job.Spec.Run.Timeout; r != nil {
		t = r.Duration
	}
	if m := in.Template.Spec.Jobs.MaxTimeout; m != nil && (t == 0 || m.Duration < t) {
		t = m.Duration
	}
	if m := in.MaxTimeout; m > 0 && (t == 0 || m < t) {
		t = m
	}
	return t
}

// deadline is t in whole seconds for a Job, nil for no limit.
func deadline(t time.Duration) *int64 {
	if t == 0 {
		return nil
	}
	return ptr.To(int64(t.Seconds()))
}

// planRun is the workload of one run of the job: the runner (the platform's
// binary, copied in by a step) in the template's image, on the owner's
// storage, with run (runner.Spec as JSON) as JAVO_RUN and the job's secrets
// read from the bundle secretName.
func planRun(in Inputs, run, secretName string) (workload.Spec, error) {
	d, t := in.Job, in.Template
	s := workload.Spec{
		Ref:       workload.Ref{Namespace: d.Namespace, Name: cronJobName(d), ID: d.ID, Owner: d.Owner},
		Resources: t.Spec.Resources,
		Size:      in.Size,
		Main:      workspace.MainUnit(d.Owner, t.Spec.RunImage(in.PixiImage)),
		Batch:     &workload.Batch{Retries: d.Spec.Run.Retries, Timeout: timeout(in), FailNow: exitCodesFailNow},
	}
	workspace.AddLayout(&s, in.Layout)
	s.Volumes = append(s.Volumes, workload.Volume{Name: runnerVolume})
	runnerMount := workload.Mount{Volume: runnerVolume, Path: runnerDir}
	s.Steps = []workload.Unit{{
		Name: "runner-install", Image: in.RunnerImage.Ref, ReadOnlyRoot: true,
		Command: []string{"/javo"},
		Args:    []string{"runner", "install", "--to", runnerDir + "/javo"},
		Mounts:  []workload.Mount{runnerMount},
	}}
	s.Pull(in.RunnerImage.PullSecrets)
	app := &s.Main
	app.Command = []string{runnerDir + "/javo"}
	app.Args = []string{"runner"}
	app.Mounts = append(app.Mounts, runnerMount)
	workspace.AddEnv(app, d.Spec.Env)
	for _, b := range in.Secrets.Bindings {
		app.SetEnv(workload.Env{Name: b.Env, From: &workload.SecretRef{Bundle: secretName, Key: b.Env}})
	}
	app.Env = append(app.Env, workload.Env{Name: "JAVO_RUN", Value: run}, workload.Env{Name: "JAVO_RUN_ID", RunID: true})
	if in.API.Secret != "" {
		// The token is never in the Spec.
		app.Env = append(app.Env,
			workload.Env{Name: "JAVO_URL", Value: in.API.URL},
			workload.Env{Name: "JAVO_API_URL", Value: in.API.Internal},
			workload.Env{Name: workload.EnvToken, From: &workload.SecretRef{Bundle: in.API.Secret, Key: APITokenKey}},
		)
	}
	if t.Spec.UsesPodFiles() {
		podfiles.Add(&s, in.RunnerImage)
	}
	if err := pixienv.Add(&s, in.PixiImage, in.RunnerImage, pixienv.Source{Files: in.Environment, Default: t.Spec.Environment}); err != nil {
		return workload.Spec{}, err
	}
	return s, nil
}

// BuildJobTemplate renders the Job that one scheduled run of the job uses,
// with params overriding the declared defaults.
func BuildJobTemplate(in Inputs, override map[string]string) (batchv1.JobTemplateSpec, error) {
	return buildJobTemplate(in, override, ScheduledSecretName(in.Job))
}

// buildJobTemplate is BuildJobTemplate for a run that reads its secrets from
// the Secret secretName.
func buildJobTemplate(in Inputs, override map[string]string, secretName string) (batchv1.JobTemplateSpec, error) {
	if err := Validate(in); err != nil {
		return batchv1.JobTemplateSpec{}, err
	}
	ps, err := params(in.Job, override)
	if err != nil {
		return batchv1.JobTemplateSpec{}, err
	}
	d := in.Job
	spec := runner.Spec{
		Definition: d.Name, Engine: d.Spec.Engine, Source: d.Spec.Source.Path,
		Kernel: d.Spec.Kernel, Command: d.Spec.Command, Params: ps,
		KeepRuns: int(d.Spec.History.KeepRuns),
	}
	if c := d.Spec.Run.CellTimeout; c != nil {
		spec.CellTimeout = c.Duration
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return batchv1.JobTemplateSpec{}, err
	}

	run, err := planRun(in, string(raw), secretName)
	if err != nil {
		return batchv1.JobTemplateSpec{}, err
	}
	runPod := kube.Render(run, in.Isolation)

	return batchv1.JobTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: labels(d, TriggerSchedule)},
		Spec: batchv1.JobSpec{
			BackoffLimit:          ptr.To(run.Batch.Retries),
			ActiveDeadlineSeconds: deadline(run.Batch.Timeout),
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
						Values:        run.Batch.FailNow,
					},
				},
			}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels(d, TriggerSchedule), Annotations: runPod.Annotations},
				Spec:       runPod.Spec,
			},
		},
	}, nil
}

// BuildCronJob renders the CronJob for a scheduled job.
func BuildCronJob(in Inputs) (*batchv1.CronJob, error) {
	s := in.Job.Spec.Schedule
	if s == nil {
		return nil, errors.New("job has no schedule")
	}
	jt, err := BuildJobTemplate(in, nil)
	if err != nil {
		return nil, err
	}
	h := in.Job.Spec.History
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name: cronJobName(in.Job), Namespace: in.Job.Namespace,
			Labels: labels(in.Job, TriggerSchedule),
		},
		Spec: batchv1.CronJobSpec{
			Schedule:                   s.Cron,
			TimeZone:                   ptr.To(s.TimeZone),
			ConcurrencyPolicy:          batchv1.ConcurrencyPolicy(s.Concurrency),
			Suspend:                    ptr.To(s.Paused),
			SuccessfulJobsHistoryLimit: h.KeepSucceeded,
			FailedJobsHistoryLimit:     h.KeepFailed,
			JobTemplate:                jt,
		},
	}
	if s.StartingDeadline != nil {
		cj.Spec.StartingDeadlineSeconds = ptr.To(int64(s.StartingDeadline.Seconds()))
	}
	return cj, nil
}

// BuildManualJob renders the Job for the run started by hand whose row is
// runID.
func BuildManualJob(in Inputs, runID string, override map[string]string) (*batchv1.Job, error) {
	name := ManualJobName(in.Job, runID)
	jt, err := buildJobTemplate(in, override, RunSecretName(name))
	if err != nil {
		return nil, err
	}
	jt.Labels[LabelTrigger] = TriggerManual
	jt.Spec.Template.Labels[LabelTrigger] = TriggerManual
	jt.Spec.TTLSecondsAfterFinished = ptr.To(int32(manualJobTTL.Seconds()))
	jt.Annotations = map[string]string{"cronjob.kubernetes.io/instantiate": "manual"}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: in.Job.Namespace,
			Labels:    jt.Labels, Annotations: jt.Annotations,
			Finalizers: []string{FinalizerRecordRun},
		},
		Spec: jt.Spec,
	}, nil
}

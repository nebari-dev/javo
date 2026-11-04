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
	"github.com/nebari-dev/javo/internal/jobs/runner"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/podfiles"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
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
	LabelJobName = "batch.kubernetes.io/job-name"
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

func timeout(in Inputs) *int64 {
	t := time.Duration(0)
	if r := in.Job.Spec.Run.Timeout; r != nil {
		t = r.Duration
	}
	if m := in.Template.Spec.Jobs.MaxTimeout; m != nil && (t == 0 || m.Duration < t) {
		t = m.Duration
	}
	if t == 0 {
		return nil
	}
	return ptr.To(int64(t.Seconds()))
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

	runPod := &corev1.Pod{Spec: podspec.Base(d.Owner, in.Template.Spec.RunImage(in.PixiImage), in.Template.Spec.Resources)}
	in.Layout.Apply(runPod, &runPod.Spec.Containers[0])
	pod := runPod.Spec
	pod.RestartPolicy = corev1.RestartPolicyNever
	pod.Volumes = append(pod.Volumes, corev1.Volume{Name: runnerVolume,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
	runnerMount := corev1.VolumeMount{Name: runnerVolume, MountPath: runnerDir}
	pod.InitContainers = []corev1.Container{{
		Name:            "runner-install",
		Image:           in.RunnerImage.Ref,
		Command:         []string{"/javo"},
		Args:            []string{"runner", "install", "--to", runnerDir + "/javo"},
		SecurityContext: podspec.RestrictedContext(),
		VolumeMounts:    []corev1.VolumeMount{runnerMount},
	}}
	in.RunnerImage.Pull(&pod)
	app := &pod.Containers[0]
	in.Size.Apply(&pod, app, in.Template.Spec.Resources)
	app.Command = []string{runnerDir + "/javo"}
	app.Args = []string{"runner"}
	app.VolumeMounts = append(app.VolumeMounts, runnerMount)
	for _, e := range d.Spec.Env {
		podspec.SetEnv(app, corev1.EnvVar{Name: e.Name, Value: e.Value})
	}
	podspec.SetEnv(app, in.Secrets.Env(secretName)...)
	app.Env = append(app.Env,
		corev1.EnvVar{Name: "JAVO_RUN", Value: string(raw)},
		corev1.EnvVar{Name: "JAVO_RUN_ID", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{
			FieldPath: "metadata.labels['" + LabelJobName + "']"}}},
	)
	if in.API.Secret != "" {
		// The token is never in the pod spec in clear.
		app.Env = append(app.Env,
			corev1.EnvVar{Name: "JAVO_URL", Value: in.API.URL},
			corev1.EnvVar{Name: "JAVO_API_URL", Value: in.API.Internal},
			corev1.EnvVar{Name: "JAVO_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: in.API.Secret}, Key: APITokenKey}}},
		)
	}

	if in.Template.Spec.UsesPodFiles() {
		podfiles.Apply(&pod, in.RunnerImage)
	}
	if err := pixienv.Apply(&pod, in.PixiImage, in.RunnerImage, pixienv.Source{Files: in.Environment, Default: in.Template.Spec.Environment}); err != nil {
		return batchv1.JobTemplateSpec{}, err
	}

	return batchv1.JobTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: labels(d, TriggerSchedule)},
		Spec: batchv1.JobSpec{
			BackoffLimit:          ptr.To(d.Spec.Run.Retries),
			ActiveDeadlineSeconds: timeout(in),
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
						Values:        exitCodesFailNow,
					},
				},
			}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels(d, TriggerSchedule), Annotations: runPod.Annotations},
				Spec:       pod,
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

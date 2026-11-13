// Package jobs starts the runs of job rows, by hand or as their schedule
// comes due, through an executor, and records every run in the database.
package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

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
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/workload"
)

const (
	// LabelTrigger says on a run what started it.
	LabelTrigger    = kube.LabelTrigger
	TriggerSchedule = store.TriggerSchedule
	TriggerManual   = store.TriggerManual

	// jobNameMax keeps room after the job's name for what a run adds to it
	// (RunName), within the 63 characters a run's name may have.
	jobNameMax = 52

	// runnerDir holds the javo binary a step copies for the main unit. It
	// is not /javo: that path is the binary inside the image.
	runnerDir    = "/javo-run"
	runnerVolume = "javo-run"
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
	// reference from the run's bundle (RunSecretName).
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
	// Host is the machine the run runs on as plain processes; nil plans it
	// for a container. Such a run is planned with the machine's folders,
	// as an app is (workspace.PlanRunOnHost): the runner is copied to a
	// folder of the run's own, runs in the owner's home folder, and its
	// environment is installed by the machine's pixi.
	Host *workload.Host
}

// A job's bundles are named "<job's name>.job-<use>", a run's
// "<run's name>.run-secrets". Names made from a person's pick are DNS
// labels and never hold a ".", so the part after it keeps apps' bundles
// (".app-", workspace.SecretName), a job's and a run's apart whatever the
// names.

// RunSecretName is the bundle the run the executor knows as handle reads
// its secrets from: each run has its own, which goes with the run.
func RunSecretName(handle string) string { return handle + ".run-secrets" }

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

// jobName is the name the job's bundles and runs are called after.
func jobName(j *model.Job) string {
	return naming.ObjectNameMax(j.Owner, j.Name, jobNameMax)
}

// RunName is the name of the run whose row is runID: the job's name and a
// hash of the id. The same run always gets the same name, so starting it
// twice makes one run. It stays within 63 characters.
func RunName(j *model.Job, runID string) string {
	sum := sha256.Sum256([]byte(runID))
	return jobName(j) + "-" + hex.EncodeToString(sum[:])[:10]
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

// jobRef names the job where its runs run: what its bundles are kept for.
func jobRef(j *model.Job) workload.Ref {
	return workload.Ref{Namespace: j.Namespace, Name: jobName(j), ID: j.ID, Owner: j.Owner}
}

// runRef names the job's run that the executor knows as handle.
func runRef(j *model.Job, handle string) workload.Ref {
	ref := jobRef(j)
	ref.Name = handle
	return ref
}

// planRun is the workload of the job's run called name: the runner (the
// platform's binary, copied in by a step) in the template's image, on the
// owner's storage, with run (runner.Spec as JSON) as JAVO_RUN and the job's
// secrets read from the bundle secretName.
func planRun(in Inputs, name, run, secretName string) (workload.Spec, error) {
	d, t := in.Job, in.Template
	// Where the run finds its home and the runner: the fixed paths of a
	// container, or the folders of the machine it runs on as processes.
	home, runnerAt, layout, pixi := podspec.HomeDir, runnerDir, in.Layout, in.PixiImage
	var host *workspace.RunOnHost
	if in.Host != nil {
		on, err := workspace.PlanRunOnHost(in.Host, in.Layout, t, in.Environment != nil)
		if err != nil {
			return workload.Spec{}, err
		}
		if in.RunnerImage.Ref == "" {
			return workload.Spec{}, errors.New("the platform's own image is not named (the controller's --runner-image): its steps run as this binary under that name")
		}
		// Nothing runs the pixi image: the units run on the machine.
		host, home, runnerAt, layout, pixi = &on, on.Home(), on.Volume(runnerVolume), on.Layout(), ""
	}
	s := workload.Spec{
		Ref:       runRef(d, name),
		Resources: t.Spec.Resources,
		Size:      in.Size,
		Main:      workspace.MainUnitAt(d.Owner, t.Spec.RunImage(pixi), home),
		Batch:     &workload.Batch{Retries: d.Spec.Run.Retries, Timeout: timeout(in), FailNow: exitCodesFailNow},
	}
	layout.AddTo(&s)
	s.Volumes = append(s.Volumes, workload.Volume{Name: runnerVolume})
	runnerMount := workload.Mount{Volume: runnerVolume, Path: runnerAt}
	s.Steps = []workload.Unit{{
		Name: "runner-install", Image: in.RunnerImage.Ref, ReadOnlyRoot: true,
		Command: []string{"/javo"},
		Args:    []string{"runner", "install", "--to", runnerAt + "/javo"},
		Mounts:  []workload.Mount{runnerMount},
	}}
	s.Pull(in.RunnerImage.PullSecrets)
	app := &s.Main
	app.Command = []string{runnerAt + "/javo"}
	app.Args = []string{"runner"}
	if host != nil {
		// The runner takes a container's home unless told.
		app.Args = append(app.Args, "--home", home)
	}
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
	src := pixienv.Source{Files: in.Environment, Default: t.Spec.Environment}
	if host != nil {
		// A host has the platform's files in a folder of its own.
		if err := pixienv.AddAt(&s, "", in.RunnerImage, src, host.Environment()); err != nil {
			return workload.Spec{}, err
		}
		host.Finish(&s)
		return s, nil
	}
	if t.Spec.UsesPodFiles() {
		podfiles.Add(&s, in.RunnerImage)
	}
	if err := pixienv.Add(&s, in.PixiImage, in.RunnerImage, src); err != nil {
		return workload.Spec{}, err
	}
	return s, nil
}

// plan is the workload of the job's run called name, with override over the
// declared parameters, reading its secrets from the bundle secretName.
func plan(in Inputs, name string, override map[string]string, secretName string) (workload.Spec, error) {
	if err := Validate(in); err != nil {
		return workload.Spec{}, err
	}
	ps, err := params(in.Job, override)
	if err != nil {
		return workload.Spec{}, err
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
		return workload.Spec{}, err
	}
	return planRun(in, name, string(raw), secretName)
}

// PlanRun is the workload of the run whose row is runID, with override
// over the declared parameters. slot is the time a schedule starts it for;
// zero for a run a person started.
func PlanRun(in Inputs, runID string, override map[string]string, slot time.Time) (workload.Spec, error) {
	name := RunName(in.Job, runID)
	s, err := plan(in, name, override, RunSecretName(name))
	if err != nil {
		return workload.Spec{}, err
	}
	s.Batch.Slot = slot
	return s, nil
}

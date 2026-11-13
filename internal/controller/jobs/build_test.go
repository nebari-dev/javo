package jobs

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/nebari-dev/javo/internal/jobs/runner"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
)

func testInputs(mutate func(*model.Job)) Inputs {
	job := &model.Job{
		ID: "job-1", Namespace: "javo-apps", Name: "nightly", Owner: "alice-1a2b3c4d",
		Spec: model.JobSpec{
			Template: "lab",
			Engine:   "papermill",
			Kernel:   "python3",
			Source:   model.JobSource{Path: "reports/nightly.ipynb"},
			Parameters: []model.JobParameter{
				{Name: "region", Type: "string", Default: "emea"},
				{Name: "days", Type: "integer", Default: "7"},
			},
			Env: []model.EnvVar{{Name: "REPORT_MODE", Value: "full"}},
			Schedule: &model.JobSchedule{
				Cron: "0 6 * * 1-5", TimeZone: "Europe/Berlin", Concurrency: "Forbid",
				StartingDeadline: &metav1.Duration{Duration: 10 * time.Minute},
			},
			History: model.JobHistory{KeepRuns: 20, KeepSucceeded: ptr.To[int32](3), KeepFailed: ptr.To[int32](5)},
			Run:     model.JobRunPolicy{Retries: 2, CellTimeout: &metav1.Duration{Duration: time.Hour}},
		},
	}
	if mutate != nil {
		mutate(job)
	}
	return Inputs{
		Job: job,
		Template: &model.Template{
			Name: "lab",
			Spec: model.TemplateSpec{
				Image: "example.com/lab:1",
				Jobs: &model.TemplateJobs{
					Engines:    []string{"papermill", "command"},
					Kernels:    []string{"python3"},
					MaxTimeout: &metav1.Duration{Duration: 24 * time.Hour},
				},
			},
		},
		RunnerImage: podspec.Image{Ref: "example.com/javo:9"},
		Layout:      podstorage.Build(job.Owner, podstorage.Input{UID: podspec.FixedUID, GID: podspec.FixedGID}),
	}
}

func runSpec(t *testing.T, c corev1.Container) runner.Spec {
	t.Helper()
	for _, e := range c.Env {
		if e.Name == "JAVO_RUN" {
			var s runner.Spec
			if err := json.Unmarshal([]byte(e.Value), &s); err != nil {
				t.Fatalf("JAVO_RUN is not JSON: %v", err)
			}
			return s
		}
	}
	t.Fatal("no JAVO_RUN env")
	return runner.Spec{}
}

func TestBuildCronJobRendersSchedule(t *testing.T) {
	t.Parallel()
	in := testInputs(nil)
	cj, err := BuildCronJob(in)
	if err != nil {
		t.Fatal(err)
	}
	slug := "alice-1a2b3c4d"
	if len(cj.Name) > 52 || cj.Name != naming.ObjectNameMax(slug, "nightly", 52) {
		t.Fatalf("name = %q", cj.Name)
	}
	if cj.Namespace != "javo-apps" {
		t.Fatalf("namespace = %q", cj.Namespace)
	}
	s := cj.Spec
	if s.Schedule != "0 6 * * 1-5" || *s.TimeZone != "Europe/Berlin" || s.ConcurrencyPolicy != batchv1.ForbidConcurrent {
		t.Fatalf("schedule = %q tz=%v policy=%q", s.Schedule, s.TimeZone, s.ConcurrencyPolicy)
	}
	if *s.StartingDeadlineSeconds != 600 || *s.SuccessfulJobsHistoryLimit != 3 || *s.FailedJobsHistoryLimit != 5 {
		t.Fatalf("deadline/history = %v %v %v", *s.StartingDeadlineSeconds, *s.SuccessfulJobsHistoryLimit, *s.FailedJobsHistoryLimit)
	}
	if s.Suspend != nil && *s.Suspend {
		t.Fatal("suspended without paused")
	}
	if cj.Labels[model.LabelOwner] != slug || cj.Labels[model.LabelJob] != "job-1" {
		t.Fatalf("labels = %v", cj.Labels)
	}
	jt := s.JobTemplate
	if jt.Spec.TTLSecondsAfterFinished != nil {
		t.Fatal("scheduled jobs are pruned by history limits, not a ttl")
	}
	// The CronJob controller copies only labels and annotations from the
	// template into the Job, so a finalizer here would be dead config. The
	// job reconciler adds it to each Job it sees instead.
	if len(jt.Finalizers) != 0 {
		t.Fatalf("jobTemplate finalizers are ignored by the CronJob controller, got %v", jt.Finalizers)
	}
	if jt.Labels[LabelTrigger] != "schedule" {
		t.Fatalf("trigger label = %q", jt.Labels[LabelTrigger])
	}
}

func TestBuildCronJobPausedSuspends(t *testing.T) {
	t.Parallel()
	cj, err := BuildCronJob(testInputs(func(d *model.Job) { d.Spec.Schedule.Paused = true }))
	if err != nil {
		t.Fatal(err)
	}
	if cj.Spec.Suspend == nil || !*cj.Spec.Suspend {
		t.Fatal("paused definition must suspend the CronJob")
	}
}

func TestBuildCronJobNeedsSchedule(t *testing.T) {
	t.Parallel()
	if _, err := BuildCronJob(testInputs(func(d *model.Job) { d.Spec.Schedule = nil })); err == nil {
		t.Fatal("want error for a definition without schedule")
	}
}

func TestBuildCronJobLongNameStaysUnder52(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("n", 60)
	cj, err := BuildCronJob(testInputs(func(d *model.Job) { d.Name = long }))
	if err != nil {
		t.Fatal(err)
	}
	if len(cj.Name) > 52 {
		t.Fatalf("len(%q) = %d", cj.Name, len(cj.Name))
	}
	if errs := validation.NameIsDNSSubdomain(cj.Name, false); len(errs) > 0 {
		t.Fatal(errs)
	}
}

func TestTimeoutIsCappedByTemplateMax(t *testing.T) {
	t.Parallel()
	in := testInputs(func(d *model.Job) {
		d.Spec.Run.Timeout = &metav1.Duration{Duration: 48 * time.Hour}
	})
	jt, err := BuildJobTemplate(in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := *jt.Spec.ActiveDeadlineSeconds; got != 86400 {
		t.Fatalf("activeDeadlineSeconds = %d, want 86400", got)
	}
	if *jt.Spec.BackoffLimit != 2 {
		t.Fatalf("backoffLimit = %d, want retries (2)", *jt.Spec.BackoffLimit)
	}
}

func TestPodFailurePolicyOrder(t *testing.T) {
	t.Parallel()
	jt, err := BuildJobTemplate(testInputs(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	rules := jt.Spec.PodFailurePolicy.Rules
	if len(rules) != 2 {
		t.Fatalf("rules = %+v", rules)
	}
	if rules[0].Action != batchv1.PodFailurePolicyActionIgnore ||
		len(rules[0].OnPodConditions) != 1 || rules[0].OnPodConditions[0].Type != corev1.DisruptionTarget {
		t.Fatalf("rules[0] = %+v, want Ignore on DisruptionTarget", rules[0])
	}
	r := rules[1]
	if r.Action != batchv1.PodFailurePolicyActionFailJob || r.OnExitCodes == nil ||
		r.OnExitCodes.Operator != batchv1.PodFailurePolicyOnExitCodesOpIn ||
		r.OnExitCodes.ContainerName == nil || *r.OnExitCodes.ContainerName != podspec.ContainerName {
		t.Fatalf("rules[1] = %+v", r)
	}
	for _, code := range []int32{runner.ExitUserError, runner.ExitKernelDied, runner.ExitTemplateError,
		runner.ExitSourceMissing, runner.ExitOutputWrite, runner.ExitParams, 137} {
		if !slices.Contains(r.OnExitCodes.Values, code) {
			t.Errorf("exit code %d missing from %v", code, r.OnExitCodes.Values)
		}
	}
}

func TestJobPodShape(t *testing.T) {
	t.Parallel()
	in := testInputs(nil)
	folder := model.SharedFolder{
		Spec:   model.SharedFolderSpec{Group: "research"},
		Status: model.SharedFolderStatus{ClaimName: "folder-research", GID: 5001},
	}
	in.Layout = podstorage.Build(in.Job.Owner, podstorage.Input{
		Home: &model.Home{Status: model.HomeStatus{ClaimName: "home-alice-1a2b3c4d-2"}}, Folders: []model.SharedFolder{folder},
		CoLocate: true, UID: 70001, GID: podspec.FixedGID,
	})
	jt, err := BuildJobTemplate(in, nil)
	if err != nil {
		t.Fatal(err)
	}
	pod := jt.Spec.Template.Spec
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatal("job pod must not mount a service account token")
	}
	if got := *pod.SecurityContext.RunAsUser; got != 70001 {
		t.Fatalf("job pod runs as uid %d, want the owner's 70001", got)
	}
	// A run mounts what the owner's apps mount: the home's current claim,
	// the group folders, scratch; next to the owner's other pods, never with
	// fsGroup.
	if pod.SecurityContext.FSGroup != nil || pod.Affinity == nil || len(pod.SecurityContext.SupplementalGroups) != 1 {
		t.Fatalf("pod security context %+v, affinity %+v", pod.SecurityContext, pod.Affinity)
	}
	if !slices.ContainsFunc(pod.Volumes, func(v corev1.Volume) bool {
		return v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "home-alice-1a2b3c4d-2"
	}) {
		t.Fatalf("volumes = %+v", pod.Volumes)
	}
	if pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("restartPolicy = %q", pod.RestartPolicy)
	}
	app := pod.Containers[0]
	if app.Name != podspec.ContainerName || app.Image != "example.com/lab:1" {
		t.Fatalf("app container = %s %s", app.Name, app.Image)
	}
	if !slices.ContainsFunc(app.VolumeMounts, func(m corev1.VolumeMount) bool {
		return m.Name == podstorage.HomeVolume && m.MountPath == podspec.HomeDir

	}) {
		t.Fatalf("home not mounted: %+v", app.VolumeMounts)
	}
	if len(pod.InitContainers) != 1 || pod.InitContainers[0].Image != "example.com/javo:9" {
		t.Fatalf("init containers = %+v", pod.InitContainers)
	}
	init := pod.InitContainers[0]
	// It only copies the runner into its volume: its root is read-only.
	if init.SecurityContext == nil || *init.SecurityContext.AllowPrivilegeEscalation ||
		init.SecurityContext.ReadOnlyRootFilesystem == nil || !*init.SecurityContext.ReadOnlyRootFilesystem {
		t.Fatalf("init security context = %+v", init.SecurityContext)
	}
	if len(app.Command) != 1 || !strings.HasSuffix(app.Command[0], "/javo") || !slices.Equal(app.Args, []string{"runner"}) {
		t.Fatalf("command = %v args = %v", app.Command, app.Args)
	}
	var runID *corev1.EnvVar
	for i := range app.Env {
		if app.Env[i].Name == "JAVO_RUN_ID" {
			runID = &app.Env[i]
		}
	}
	if runID == nil || runID.ValueFrom == nil ||
		runID.ValueFrom.FieldRef.FieldPath != "metadata.labels['batch.kubernetes.io/job-name']" {
		t.Fatalf("JAVO_RUN_ID = %+v", runID)
	}
	if !slices.ContainsFunc(app.Env, func(e corev1.EnvVar) bool { return e.Name == "REPORT_MODE" && e.Value == "full" }) {
		t.Fatalf("definition env missing: %+v", app.Env)
	}
	if jt.Labels[model.LabelOwner] != in.Job.Owner || jt.Labels[model.LabelJob] != "job-1" {
		t.Fatalf("labels = %v", jt.Labels)
	}
}

func TestJAVORunRoundTripsDefinition(t *testing.T) {
	t.Parallel()
	jt, err := BuildJobTemplate(testInputs(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := runSpec(t, jt.Spec.Template.Spec.Containers[0])
	if s.Engine != "papermill" || s.Source != "reports/nightly.ipynb" || s.Kernel != "python3" ||
		s.Definition != "nightly" || s.CellTimeout != time.Hour || s.KeepRuns != 20 {
		t.Fatalf("spec = %+v", s)
	}
	want := []runner.Param{{Name: "region", Type: "string", Value: "emea"}, {Name: "days", Type: "integer", Value: "7"}}
	if !slices.Equal(s.Params, want) {
		t.Fatalf("params = %+v", s.Params)
	}
}

// A job whose template gives an API token gets the owner's token from its
// Secret as JAVO_TOKEN, never in clear, and the API address as JAVO_URL; one
// without gets neither.
func TestBuildJobGivesAnAPITokenOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	env := func(in Inputs) []corev1.EnvVar {
		t.Helper()
		j, err := BuildManualJob(in, "run-1", nil)
		if err != nil {
			t.Fatal(err)
		}
		return j.Spec.Template.Spec.Containers[0].Env
	}
	find := func(env []corev1.EnvVar, name string) *corev1.EnvVar {
		if i := slices.IndexFunc(env, func(e corev1.EnvVar) bool { return e.Name == name }); i >= 0 {
			return &env[i]
		}
		return nil
	}
	if e := env(testInputs(nil)); find(e, "JAVO_TOKEN") != nil || find(e, "JAVO_URL") != nil {
		t.Fatalf("a job without API access got %v", e)
	}
	in := testInputs(nil)
	in.API = API{URL: "https://javo.example.com", Internal: "http://javo-gateway.javo-system.svc:80", Secret: "nightly-javo-api"}
	e := env(in)
	if u := find(e, "JAVO_URL"); u == nil || u.Value != "https://javo.example.com" {
		t.Fatalf("JAVO_URL = %+v", u)
	}
	// The address inside the cluster, for a public address pods cannot reach.
	if u := find(e, "JAVO_API_URL"); u == nil || u.Value != "http://javo-gateway.javo-system.svc:80" {
		t.Fatalf("JAVO_API_URL = %+v", u)
	}
	tok := find(e, "JAVO_TOKEN")
	if tok == nil || tok.Value != "" || tok.ValueFrom == nil || tok.ValueFrom.SecretKeyRef == nil || tok.ValueFrom.SecretKeyRef.Name != "nightly-javo-api" {
		t.Fatalf("JAVO_TOKEN = %+v", tok)
	}
}

func TestBuildManualJobOverridesParams(t *testing.T) {
	t.Parallel()
	job, err := BuildManualJob(testInputs(nil), "run-1", map[string]string{"region": "apac"})
	if err != nil {
		t.Fatal(err)
	}
	if job.GenerateName != "" || job.Name != ManualJobName(testInputs(nil).Job, "run-1") {
		t.Fatalf("name = %q generateName = %q", job.Name, job.GenerateName)
	}
	if job.Annotations["cronjob.kubernetes.io/instantiate"] != "manual" || job.Labels[LabelTrigger] != "manual" {
		t.Fatalf("annotations = %v labels = %v", job.Annotations, job.Labels)
	}
	if got := *job.Spec.TTLSecondsAfterFinished; got != 3600 {
		t.Fatalf("ttl = %d, want 3600", got)
	}
	if !slices.Contains(job.Finalizers, FinalizerRecordRun) {
		t.Fatalf("finalizers = %v", job.Finalizers)
	}
	spec := runSpec(t, job.Spec.Template.Spec.Containers[0])
	if !slices.Contains(spec.Params, runner.Param{Name: "region", Type: "string", Value: "apac"}) ||
		slices.Contains(spec.Params, runner.Param{Name: "region", Type: "string", Value: "emea"}) {
		t.Fatalf("params = %+v", spec.Params)
	}
}

func TestBuildManualJobRejectsUndeclaredParam(t *testing.T) {
	t.Parallel()
	if _, err := BuildManualJob(testInputs(nil), "run-1", map[string]string{"nope": "x"}); err == nil {
		t.Fatal("want error for an undeclared parameter")
	}
}

func TestTemplateMustAllowTheJob(t *testing.T) {
	t.Parallel()
	noJobs := testInputs(nil)
	noJobs.Template.Spec.Jobs = nil
	if _, err := BuildJobTemplate(noJobs, nil); !errors.Is(err, ErrJobsNotAllowed) {
		t.Fatalf("err = %v, want ErrJobsNotAllowed", err)
	}
	noEngine := testInputs(func(d *model.Job) { d.Spec.Engine = "quarto" })
	if _, err := BuildCronJob(noEngine); !errors.Is(err, ErrEngineNotInTemplate) {
		t.Fatalf("err = %v, want ErrEngineNotInTemplate", err)
	}
	if _, err := BuildManualJob(noEngine, "run-1", nil); !errors.Is(err, ErrEngineNotInTemplate) {
		t.Fatalf("err = %v, want ErrEngineNotInTemplate", err)
	}
	noKernel := testInputs(func(d *model.Job) { d.Spec.Kernel = "ir" })
	if _, err := BuildCronJob(noKernel); !errors.Is(err, ErrKernelNotInTemplate) {
		t.Fatalf("err = %v, want ErrKernelNotInTemplate", err)
	}
	if _, err := BuildManualJob(noKernel, "run-1", nil); !errors.Is(err, ErrKernelNotInTemplate) {
		t.Fatalf("err = %v, want ErrKernelNotInTemplate", err)
	}
}

// A run request is turned into a Job named from the run's id, so asking
// twice (a resync, a restart) cannot make a second Job, and the name is a
// valid Job name however long the job's own name is.
func TestManualJobNameIsStablePerRunAndFits(t *testing.T) {
	t.Parallel()
	in := testInputs(func(j *model.Job) { j.Name = strings.Repeat("n", 60) })
	a, b := ManualJobName(in.Job, "run-1"), ManualJobName(in.Job, "run-2")
	if a == b || a != ManualJobName(in.Job, "run-1") {
		t.Fatalf("names %q and %q: want one stable name per run", a, b)
	}
	if len(a) > 63 {
		t.Fatalf("len(%q) = %d, want at most 63 (it is also a pod label value)", a, len(a))
	}
	if errs := validation.NameIsDNSSubdomain(a, false); len(errs) > 0 {
		t.Fatal(errs)
	}
}

// Every run, scheduled or started by hand, gets the job's size as the run
// container's CPU and memory requests and limits; the zero Size keeps the
// template's resources.
func TestRunsGetTheJobsSize(t *testing.T) {
	t.Parallel()
	in := testInputs(nil)
	in.Template.Spec.Resources = corev1.ResourceRequirements{Requests: corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
	}}
	in.Size = sizes.Custom(resource.MustParse("2"), resource.MustParse("4Gi"))
	cj, err := BuildCronJob(in)
	if err != nil {
		t.Fatal(err)
	}
	manual, err := BuildManualJob(in, "run-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, pod := range map[string]corev1.PodSpec{"scheduled": cj.Spec.JobTemplate.Spec.Template.Spec, "manual": manual.Spec.Template.Spec} {
		r := pod.Containers[0].Resources
		for _, l := range []corev1.ResourceList{r.Requests, r.Limits} {
			if l.Cpu().Cmp(resource.MustParse("2")) != 0 || l.Memory().Cmp(resource.MustParse("4Gi")) != 0 {
				t.Errorf("%s run: resources %+v, want 2 CPU and 4Gi as requests and limits", name, r)
			}
		}
		if q := r.Requests[corev1.ResourceEphemeralStorage]; q.Cmp(resource.MustParse("1Gi")) != 0 {
			t.Errorf("%s run: the template's other requests were dropped: %+v", name, r)
		}
	}

	in.Size = sizes.Size{Name: "gpu", DisplayName: "GPU", CPU: resource.MustParse("4"), Memory: resource.MustParse("16Gi"),
		GPU: 1, GPUResource: sizes.DefaultGPUResource, NodeSelector: map[string]string{"pool": "gpu"},
		Tolerations: []corev1.Toleration{{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}}
	if manual, err = BuildManualJob(in, "run-2", nil); err != nil {
		t.Fatal(err)
	}
	if pod := manual.Spec.Template.Spec; pod.NodeSelector["pool"] != "gpu" || len(pod.Tolerations) != 1 {
		t.Errorf("gpu run: selector %v, tolerations %+v", pod.NodeSelector, pod.Tolerations)
	} else if g := pod.Containers[0].Resources.Requests[sizes.DefaultGPUResource]; g.Cmp(resource.MustParse("1")) != 0 {
		t.Errorf("gpu run: gpu request %s", g.String())
	}

	in.Size = sizes.Size{}
	cj, err = BuildCronJob(in)
	if err != nil {
		t.Fatal(err)
	}
	if r := cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Resources; r.Requests.Cpu().Cmp(resource.MustParse("250m")) != 0 || r.Limits != nil {
		t.Fatalf("no size: resources %+v, want the template's", r)
	}
}

// Every job pod runs the platform image (the runner install), so a private
// one is pulled with its Secrets; a public one adds none.
func TestJobPodPullsAPrivatePlatformImageWithItsSecrets(t *testing.T) {
	t.Parallel()
	in := testInputs(nil)
	in.RunnerImage.PullSecrets = []string{"ghcr"}
	cj, err := BuildCronJob(in)
	if err != nil {
		t.Fatal(err)
	}
	got := cj.Spec.JobTemplate.Spec.Template.Spec.ImagePullSecrets
	if len(got) != 1 || got[0].Name != "ghcr" {
		t.Fatalf("imagePullSecrets = %v, want [ghcr]", got)
	}

	cj, err = BuildCronJob(testInputs(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := cj.Spec.JobTemplate.Spec.Template.Spec.ImagePullSecrets; got != nil {
		t.Fatalf("public image: imagePullSecrets = %v, want none", got)
	}
}

// Programs in a run print colours as in a terminal, unless the job says otherwise.
func TestJobPodAsksForColourUnlessTheJobSetsItsOwn(t *testing.T) {
	t.Parallel()
	effective := func(t *testing.T, mutate func(*model.Job), name string) string {
		t.Helper()
		jt, err := BuildJobTemplate(testInputs(mutate), nil)
		if err != nil {
			t.Fatal(err)
		}
		var values []string
		for _, e := range jt.Spec.Template.Spec.Containers[0].Env {
			if e.Name == name {
				values = append(values, e.Value)
			}
		}
		if len(values) != 1 {
			t.Fatalf("%s defined %d times: %v", name, len(values), values)
		}
		return values[0]
	}
	for _, n := range []string{"FORCE_COLOR", "CLICOLOR_FORCE", "PY_COLORS"} {
		if v := effective(t, nil, n); v != "1" {
			t.Errorf("%s = %q, want 1", n, v)
		}
	}
	own := func(j *model.Job) { j.Spec.Env = append(j.Spec.Env, model.EnvVar{Name: "FORCE_COLOR", Value: "0"}) }
	if v := effective(t, own, "FORCE_COLOR"); v != "0" {
		t.Fatalf("FORCE_COLOR = %q, want the job's own 0", v)
	}
}

// The platform's limits.jobMaxTimeout caps every run: one with no time limit
// of its own or its template's gets it, and a longer one is cut to it.
func TestTimeoutIsCappedByThePlatformMax(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		job  *metav1.Duration
		want int64
	}{
		{"no limit set", nil, 3600},
		{"a longer limit", &metav1.Duration{Duration: 5 * time.Hour}, 3600},
		{"a shorter limit", &metav1.Duration{Duration: 30 * time.Minute}, 1800},
	} {
		in := testInputs(func(d *model.Job) { d.Spec.Run.Timeout = tc.job })
		in.Template.Spec.Jobs.MaxTimeout = nil
		in.MaxTimeout = time.Hour
		jt, err := BuildJobTemplate(in, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := jt.Spec.ActiveDeadlineSeconds; got == nil || *got != tc.want {
			t.Errorf("%s: activeDeadlineSeconds = %v, want %d", tc.name, got, tc.want)
		}
	}
}

// Job runs get the isolation the admin chose for people's pods.
func TestJobRunsGetTheIsolation(t *testing.T) {
	t.Parallel()
	in := testInputs(nil)
	in.Isolation = podspec.Isolation{UserNamespace: true, RuntimeClassName: "gvisor"}
	jt, err := BuildJobTemplate(in, nil)
	if err != nil {
		t.Fatal(err)
	}
	pod := jt.Spec.Template.Spec
	if pod.HostUsers == nil || *pod.HostUsers || pod.RuntimeClassName == nil || *pod.RuntimeClassName != "gvisor" {
		t.Fatalf("hostUsers %v, runtimeClass %v", pod.HostUsers, pod.RuntimeClassName)
	}
}

// The API refuses a variable the platform sets. One written straight into
// the database is left out of the run, as it is for an app: it never
// replaces the platform's own.
func TestARunNeverGetsAReservedVariableFromItsJob(t *testing.T) {
	t.Parallel()
	in := testInputs(func(d *model.Job) {
		d.Spec.Env = []model.EnvVar{{Name: "HOME", Value: "/elsewhere"}, {Name: "JAVO_RUN", Value: "x"}, {Name: "TZ", Value: "UTC"}}
	})
	jt, err := BuildJobTemplate(in, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, e := range jt.Spec.Template.Spec.Containers[0].Env {
		seen[e.Name]++
		if (e.Name == "HOME" && e.Value != "/home/user") || (e.Name == "JAVO_RUN" && e.Value == "x") || e.Name == "TZ" {
			t.Errorf("%s = %q", e.Name, e.Value)
		}
	}
	if seen["HOME"] != 1 || seen["JAVO_RUN"] != 1 {
		t.Errorf("HOME is set %d times and JAVO_RUN %d", seen["HOME"], seen["JAVO_RUN"])
	}
}

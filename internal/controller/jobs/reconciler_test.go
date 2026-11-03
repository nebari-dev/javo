// Event reasons the Kubernetes CronJob controller emits (read from
// pkg/controller/cronjob/cronjob_controllerv2.go at v1.37.0, the version envtest
// and kind use):
//
//	"JobAlreadyActive" (Normal): a start was skipped because the previous run is
//	  still active and concurrencyPolicy is Forbid. The message names no time.
//	"MissSchedule" (Warning): a start was later than startingDeadlineSeconds.
//	  The message ends with the scheduled time in RFC1123Z.
//
// Both go through a core/v1 recorder, so repeats of an identical message are
// folded into one Event with a higher count and a newer lastTimestamp.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/manager"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

const owner = "alice-1a2b3c4d"

var (
	bg         = context.Background()
	actor      = model.Actor{Person: owner}
	k8s        client.Client
	restConfig *rest.Config
	scheme     = runtime.NewScheme()
	// fixedNow keeps the next 06:00 Berlin run days away, so nextRunTimes does
	// not move while a test runs.
	fixedNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	nsCount  atomic.Int32
)

func TestMain(m *testing.M) {
	env := &envtest.Environment{}
	rest, err := env.Start()
	if err != nil {
		panic(err)
	}
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	k8s, err = client.New(rest, client.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}
	restConfig = rest
	code := m.Run()
	_ = env.Stop()
	os.Exit(code)
}

// access is the test Access: it answers allow for every owner.
type access struct{ allow atomic.Bool }

func (a *access) MayUse(context.Context, string, *model.Template) (bool, error) {
	return a.allow.Load(), nil
}

// harness is one namespace and one database with their own manager, so tests
// do not see each other's objects.
type harness struct {
	t      *testing.T
	ns     string
	db     *store.Store
	access *access
	rec    *Reconciler
	stop   func()
	// configure changes the reconciler before it starts.
	configure func(*Reconciler)
}

func newHarness(t *testing.T) *harness { return newHarnessWith(t, nil) }

// newHarnessWith is newHarness with the reconciler changed by configure
// before it starts.
func newHarnessWith(t *testing.T, configure func(*Reconciler)) *harness {
	t.Helper()
	h := &harness{t: t, ns: fmt.Sprintf("jobs-%d", nsCount.Add(1)), db: storetest.New(t), access: &access{}, configure: configure}
	h.access.allow.Store(true)
	if err := k8s.Create(bg, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: h.ns}}); err != nil {
		t.Fatal(err)
	}
	h.start()
	t.Cleanup(h.stop)
	return h
}

// newReconciler builds a reconciler over the harness's database and the
// manager's clients.
func (h *harness) newReconciler(c client.Client, reader client.Reader) *Reconciler {
	r := &Reconciler{
		Client: c, Store: h.db, Reader: reader, RunnerImage: podspec.Image{Ref: "example.com/javo:9"}, Secrets: k8s,
		Storage: podstorage.Resolver{Homes: h.db.Homes(), Folders: h.db.SharedFolders()},
		Access:  h.access, Now: func() time.Time { return fixedNow },
		// A short resync stands in for announcements the tests make by
		// writing to the database behind the store's back.
		ResyncEvery: 200 * time.Millisecond,
	}
	if h.configure != nil {
		h.configure(r)
	}
	return r
}

// start runs a manager and returns when it is stopped by h.stop.
func (h *harness) start() {
	h.t.Helper()
	// The controller's own cache rules (manager.Options), so a run is judged
	// from exactly what the controller can see.
	opts := manager.Options(scheme, []string{h.ns}, "0", "0")
	opts.LeaderElection = false
	opts.Controller = ctrlconfig.Controller{SkipNameValidation: ptr.To(true)}
	mgr, err := ctrl.NewManager(restConfig, opts)
	if err != nil {
		h.t.Fatal(err)
	}
	events, err := manager.CronJobEvents(restConfig, scheme, []string{h.ns})
	if err != nil {
		h.t.Fatal(err)
	}
	if err := mgr.Add(events); err != nil {
		h.t.Fatal(err)
	}
	r := h.newReconciler(mgr.GetClient(), mgr.GetAPIReader())
	r.CronJobEvents = events
	feed := dbsource.NewFeed(h.db.Changes())
	if err := r.SetupWithManager(mgr, feed); err != nil {
		h.t.Fatal(err)
	}
	h.rec = r
	ctx, cancel := context.WithCancel(bg)
	if err := h.db.StartFeed(ctx); err != nil {
		h.t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); feed.Run(ctx) }()
	go func() {
		defer wg.Done()
		if err := mgr.Start(ctx); err != nil {
			h.t.Error(err)
		}
	}()
	h.stop = func() { cancel(); wg.Wait() }
	// Tests call Reconcile at once: wait until the cache it reads is filled.
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		h.t.Fatal("the controller's cache did not sync")
	}
}

func (h *harness) tplName() string { return "lab" }

func (h *harness) template(mutate func(*model.TemplateSpec)) model.Template {
	h.t.Helper()
	spec := model.TemplateSpec{
		DisplayName: "Lab", Kind: "jupyterlab", Image: "example.com/lab:1", Port: 8888,
		Command: []string{"lab"}, Args: []string{"--base-url", "{{.BaseURL}}"},
		Jobs: &model.TemplateJobs{Engines: []string{"papermill", "command"}, Kernels: []string{"python3"}},
	}
	if mutate != nil {
		mutate(&spec)
	}
	tpl, err := h.db.Templates().Put(bg, actor, model.Template{Name: h.tplName(), Source: store.SourceAdmin, Spec: spec}, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	return tpl
}

func (h *harness) definition(name string, mutate func(*model.Job)) model.Job {
	h.t.Helper()
	j := &model.Job{
		Namespace: h.ns, Name: name, Owner: owner,
		Spec: model.JobSpec{
			Template: h.tplName(), Engine: "papermill", Kernel: "python3",
			Source:   model.JobSource{Path: "reports/nightly.ipynb"},
			Schedule: &model.JobSchedule{Cron: "0 6 * * 1-5", TimeZone: "Europe/Berlin", Concurrency: "Forbid"},
		},
	}
	if mutate != nil {
		mutate(j)
	}
	got, err := h.db.Jobs().Create(bg, model.Actor{Person: j.Owner}, j)
	if err != nil {
		h.t.Fatal(err)
	}
	return got
}

func (h *harness) cronJob(j model.Job) *batchv1.CronJob {
	return &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: cronJobName(&j), Namespace: h.ns}}
}

func (h *harness) job(id string) model.Job {
	j, err := h.db.Jobs().Get(bg, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return j
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (h *harness) waitPhase(j model.Job, phase string) model.Job {
	h.t.Helper()
	var got model.Job
	eventually(h.t, j.Name+" phase "+phase, func() bool {
		got = h.job(j.ID)
		return got.Status.Phase == phase
	})
	return got
}

func condReason(j model.Job, typ string) string {
	for _, c := range j.Status.Conditions {
		if c.Type == typ {
			return c.Reason
		}
	}
	return ""
}

func (h *harness) suspended(j model.Job) bool {
	cj := h.cronJob(j)
	if err := k8s.Get(bg, client.ObjectKeyFromObject(cj), cj); err != nil {
		return false
	}
	return ptr.Deref(cj.Spec.Suspend, false)
}

func (h *harness) cronJobExists(j model.Job) bool {
	cj := h.cronJob(j)
	err := k8s.Get(bg, client.ObjectKeyFromObject(cj), cj)
	return err == nil
}

// scheduledJob is a Job as the CronJob controller makes it: labels and
// template from the CronJob's jobTemplate, no finalizer.
func (h *harness) scheduledJob(j model.Job, tpl model.Template, name string) *batchv1.Job {
	h.t.Helper()
	return h.scheduledJobAt(j, tpl, name, fixedNow)
}

// scheduledJobAt is scheduledJob for the slot at.
func (h *harness) scheduledJobAt(j model.Job, tpl model.Template, name string, at time.Time) *batchv1.Job {
	h.t.Helper()
	jt, err := BuildJobTemplate(Inputs{Job: &j, Template: &tpl, RunnerImage: podspec.Image{Ref: "example.com/javo:9"}}, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	k := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns, Labels: jt.Labels, Annotations: map[string]string{
			"batch.kubernetes.io/cronjob-scheduled-timestamp": at.Format(time.RFC3339),
		}},
		Spec: jt.Spec,
	}
	if err := k8s.Create(bg, k); err != nil {
		h.t.Fatal(err)
	}
	return k
}

// requestRun asks for a run as the gateway does and waits until the
// controller has made its Job.
func (h *harness) requestRun(j model.Job, params map[string]string) (model.Run, *batchv1.Job) {
	h.t.Helper()
	run, err := h.db.Jobs().RequestRun(bg, actor, j.ID, params)
	if err != nil {
		h.t.Fatal(err)
	}
	run = h.waitRunWhere(j, run.ID, " to start", func(r model.Run) bool { return r.K8sJob != "" })
	k := &batchv1.Job{}
	eventually(h.t, "the run's Job", func() bool {
		return k8s.Get(bg, client.ObjectKey{Namespace: h.ns, Name: run.K8sJob}, k) == nil
	})
	return run, k
}

// failJob stands in for the Job controller (envtest runs none): it adds a pod
// whose app container ended with the given termination message, then marks the
// Job Failed.
func (h *harness) failJob(j *batchv1.Job, message string) {
	h.t.Helper()
	// The Job controller gives a pod the Job template's labels plus job-name.
	podLabels := map[string]string{LabelJobName: j.Name}
	maps.Copy(podLabels, j.Spec.Template.Labels)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: j.Name + "-pod", Namespace: h.ns, Labels: podLabels},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: podspec.ContainerName, Image: "example.com/lab:1"}}},
	}
	if err := k8s.Create(bg, pod); err != nil {
		h.t.Fatal(err)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: podspec.ContainerName, State: corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{ExitCode: 65, Reason: "Error", Message: message}}}}
	if err := k8s.Status().Update(bg, pod); err != nil {
		h.t.Fatal(err)
	}
	start := metav1.NewTime(fixedNow.Add(-2 * time.Minute))
	end := metav1.NewTime(fixedNow.Add(-time.Minute))
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		got := &batchv1.Job{}
		if err := k8s.Get(bg, client.ObjectKeyFromObject(j), got); err != nil {
			return err
		}
		got.Status.StartTime = &start
		got.Status.Failed = 1
		got.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "PodFailurePolicy", LastTransitionTime: end},
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "PodFailurePolicy", LastTransitionTime: end},
		}
		return k8s.Status().Update(bg, got)
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) hasFinalizer(name string) (found, exists bool) {
	j := &batchv1.Job{}
	err := k8s.Get(bg, client.ObjectKey{Namespace: h.ns, Name: name}, j)
	if apierrors.IsNotFound(err) {
		return false, false
	}
	for _, f := range j.Finalizers {
		if f == FinalizerRecordRun {
			return true, true
		}
	}
	return false, true
}

func (h *harness) runs(j model.Job) []model.Run {
	runs, err := h.db.Jobs().Runs(bg, j.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return runs
}

// runOfK8sJob is the run recorded for the Kubernetes Job of that name.
func (h *harness) runOfK8sJob(j model.Job, k8sJob string) (model.Run, bool) {
	for _, r := range h.runs(j) {
		if r.K8sJob == k8sJob {
			return r, true
		}
	}
	return model.Run{}, false
}

// waitRun returns the run of the Kubernetes Job as soon as it is recorded,
// Running or not.
func (h *harness) waitRun(j model.Job, k8sJob string) model.Run {
	h.t.Helper()
	var got model.Run
	eventually(h.t, "run of "+k8sJob+" in "+j.Name, func() bool {
		var ok bool
		got, ok = h.runOfK8sJob(j, k8sJob)
		return ok
	})
	return got
}

// waitFinalRun returns the run once its outcome is final. The controller can
// record a Job as Running before failJob marks it Failed.
func (h *harness) waitFinalRun(j model.Job, k8sJob string) model.Run {
	h.t.Helper()
	var got model.Run
	eventually(h.t, "run of "+k8sJob+" in "+j.Name+" to finish", func() bool {
		var ok bool
		got, ok = h.runOfK8sJob(j, k8sJob)
		return ok && final(got.Outcome)
	})
	return got
}

func (h *harness) waitRunWhere(j model.Job, runID, what string, ok func(model.Run) bool) model.Run {
	h.t.Helper()
	var got model.Run
	eventually(h.t, "run "+runID+" in "+j.Name+what, func() bool {
		r, err := h.db.Jobs().Run(bg, runID)
		got = r
		return err == nil && ok(r)
	})
	return got
}

const cellErrorMessage = `{"reason":"CellError","message":"Cell 7 raised KeyError: 'region'","outputPath":"jobs/nightly/run"}`

func TestScheduledJobCreatesLabelledCronJob(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", nil)

	got := h.waitPhase(d, model.JobPhaseReady)
	cj := h.cronJob(d)
	if err := k8s.Get(bg, client.ObjectKeyFromObject(cj), cj); err != nil {
		t.Fatal(err)
	}
	if cj.Labels[model.LabelJob] != d.ID || len(cj.OwnerReferences) != 0 {
		t.Fatalf("cronjob labels = %v, owner refs = %+v; want the job label and no owner reference", cj.Labels, cj.OwnerReferences)
	}
	if cj.Spec.Schedule != "0 6 * * 1-5" || ptr.Deref(cj.Spec.Suspend, false) {
		t.Fatalf("cronjob spec = %+v", cj.Spec)
	}
	if len(got.Status.NextRunTimes) != 3 {
		t.Fatalf("nextRunTimes = %v, want 3", got.Status.NextRunTimes)
	}
	if condReason(got, model.ConditionValid) != string(reasons.Ready) {
		t.Fatalf("conditions = %+v", got.Status.Conditions)
	}
	if got.Status.ObservedVersion != got.Version {
		t.Fatalf("observed version %d, want %d", got.Status.ObservedVersion, got.Version)
	}
}

func TestPausedJobSuspendsCronJob(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", func(j *model.Job) { j.Spec.Schedule.Paused = true })

	got := h.waitPhase(d, model.JobPhasePaused)
	if !h.suspended(d) {
		t.Fatal("cronjob is not suspended")
	}
	if len(got.Status.NextRunTimes) != 0 {
		t.Fatalf("paused job shows next runs %v", got.Status.NextRunTimes)
	}
	if condReason(got, model.ConditionScheduled) != string(reasons.SchedulePaused) {
		t.Fatalf("conditions = %+v", got.Status.Conditions)
	}
}

func TestJobWithoutScheduleHasNoCronJob(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("once", func(j *model.Job) { j.Spec.Schedule = nil })

	h.waitPhase(d, model.JobPhaseReady)
	if h.cronJobExists(d) {
		t.Fatal("cronjob for an on-demand job")
	}
}

func TestRemovingScheduleDeletesCronJob(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)

	if _, err := h.db.Jobs().UpdateSpec(bg, actor, d.ID, h.job(d.ID).Version, func(j *model.Job) error {
		j.Spec.Schedule = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "cronjob deleted", func() bool { return !h.cronJobExists(d) })
}

func TestTemplateMissingBlocksAndSuspendsThenRecovers(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)

	if err := h.db.Templates().Delete(bg, actor, h.tplName()); err != nil {
		t.Fatal(err)
	}
	got := h.waitPhase(d, model.JobPhaseBlocked)
	if condReason(got, model.ConditionValid) != string(reasons.TemplateMissing) {
		t.Fatalf("conditions = %+v", got.Status.Conditions)
	}
	eventually(t, "cronjob suspended", func() bool { return h.suspended(d) })

	h.template(nil)
	h.waitPhase(d, model.JobPhaseReady)
	eventually(t, "cronjob resumed", func() bool { return !h.suspended(d) })
}

func TestOwnerLosingAccessBlocksAndSuspends(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	h.access.allow.Store(false)
	d := h.definition("nightly", nil)

	got := h.waitPhase(d, model.JobPhaseBlocked)
	if condReason(got, model.ConditionValid) != string(reasons.OwnerLostAccess) {
		t.Fatalf("conditions = %+v", got.Status.Conditions)
	}
	if h.cronJobExists(d) {
		t.Fatal("a blocked job that never ran must not get a cronjob")
	}
}

func TestTemplateProblemsMakeJobInvalid(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*model.TemplateSpec)
		def    func(*model.Job)
		want   reasons.Code
	}{
		"no jobs":        {mutate: func(s *model.TemplateSpec) { s.Jobs = nil }, want: reasons.JobsNotAllowed},
		"engine missing": {def: func(j *model.Job) { j.Spec.Engine, j.Spec.Kernel = "quarto", "" }, want: reasons.EngineMissing},
		"kernel missing": {def: func(j *model.Job) { j.Spec.Kernel = "ir" }, want: reasons.KernelMissing},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.template(tc.mutate)
			d := h.definition("nightly", tc.def)
			got := h.waitPhase(d, model.JobPhaseInvalid)
			if condReason(got, model.ConditionValid) != string(tc.want) {
				t.Fatalf("conditions = %+v, want reason %s", got.Status.Conditions, tc.want)
			}
		})
	}
}

// The API refuses a bad schedule, so one can only be stored by hand (or by a
// time zone database that no longer knows the zone); the controller then
// marks the job invalid instead of making a CronJob.
func TestInvalidScheduleMakesJobInvalid(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	h.access.allow.Store(false) // keeps the job quiet until its spec is bad
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseBlocked)
	bad := d.Spec
	bad.Schedule = &model.JobSchedule{Cron: "not a cron", TimeZone: "Europe/Berlin", Concurrency: "Forbid"}
	spec, err := json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	storetest.Exec(t, h.db, "UPDATE jobs SET spec = ? WHERE id = ?", string(spec), d.ID)
	h.access.allow.Store(true)

	got := h.waitPhase(d, model.JobPhaseInvalid)
	if condReason(got, model.ConditionValid) != string(reasons.ScheduleInvalid) {
		t.Fatalf("conditions = %+v", got.Status.Conditions)
	}
	if h.cronJobExists(d) {
		t.Fatal("cronjob created for an invalid schedule")
	}
}

func TestRequestedRunBecomesAJobAndItsFailureIsRecorded(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)
	req, k := h.requestRun(d, nil)
	if f, _ := h.hasFinalizer(k.Name); !f {
		t.Fatal("a manual Job must carry the record-run finalizer from creation")
	}
	h.failJob(k, cellErrorMessage)

	run := h.waitFinalRun(d, k.Name)
	if run.ID != req.ID {
		t.Fatalf("the Job's outcome went to run %s, want the requested row %s", run.ID, req.ID)
	}
	if run.Outcome != model.RunOutcomeFailed || run.Reason != string(reasons.CellError) || run.Trigger != "manual" {
		t.Fatalf("run = %+v", run)
	}
	if run.Message != "Cell 7 raised KeyError: 'region'" || run.OutputPath != "jobs/nightly/run" {
		t.Fatalf("run = %+v", run)
	}
	eventually(t, "finalizer removed", func() bool { f, exists := h.hasFinalizer(k.Name); return exists && !f })
}

func TestRunningJobKeepsItsFinalizer(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)
	_, k := h.requestRun(d, nil)

	run := h.waitRun(d, k.Name)
	if run.Outcome != model.RunOutcomeRunning {
		t.Fatalf("run = %+v", run)
	}
	if f, _ := h.hasFinalizer(k.Name); !f {
		t.Fatal("finalizer removed from a Job that has not finished")
	}
}

func TestScheduledJobGetsFinalizerAndARun(t *testing.T) {
	h := newHarness(t)
	tpl := h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)
	k := h.scheduledJob(d, tpl, "nightly-29")

	eventually(t, "finalizer added", func() bool { f, _ := h.hasFinalizer(k.Name); return f })
	if run := h.waitRun(d, k.Name); run.Trigger != "schedule" || run.ScheduledTime.IsZero() {
		t.Fatalf("run = %+v", run)
	}
}

// The CronJob history limit deletes finished Jobs; the run
// must already be in the database by then.
func TestRunRecordedBeforeJobDeleted(t *testing.T) {
	h := newHarness(t)
	tpl := h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)
	k := h.scheduledJob(d, tpl, "nightly-29")
	eventually(t, "finalizer added", func() bool { f, _ := h.hasFinalizer(k.Name); return f })

	h.failJob(k, cellErrorMessage)
	if err := k8s.Delete(bg, k, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
		t.Fatal(err)
	}

	run := h.waitFinalRun(d, k.Name)
	if run.Reason != string(reasons.CellError) {
		t.Fatalf("run = %+v", run)
	}
	eventually(t, "job gone", func() bool { _, exists := h.hasFinalizer(k.Name); return !exists })
}

// A Job that fails and is deleted while the controller is
// down is held by its finalizer until the next controller records it.
func TestRunRecordedAfterControllerRestart(t *testing.T) {
	h := newHarness(t)
	tpl := h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)
	k := h.scheduledJob(d, tpl, "nightly-29")
	eventually(t, "finalizer added", func() bool { f, _ := h.hasFinalizer(k.Name); return f })

	h.stop()
	h.failJob(k, cellErrorMessage)
	if err := k8s.Delete(bg, k, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
		t.Fatal(err)
	}
	if _, exists := h.hasFinalizer(k.Name); !exists {
		t.Fatal("job vanished while the controller was down")
	}
	h.start()

	if run := h.waitFinalRun(d, k.Name); run.Reason != string(reasons.CellError) {
		t.Fatalf("run = %+v", run)
	}
	eventually(t, "job gone", func() bool { _, exists := h.hasFinalizer(k.Name); return !exists })
}

// Deleting a job removes its CronJob and Jobs (a Job held by the finalizer
// is released first: its run has nowhere to be recorded) and then the row.
func TestDeletingAJobRemovesItsObjectsAndRow(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)
	_, k := h.requestRun(d, nil)
	eventually(t, "the cronjob", func() bool { return h.cronJobExists(d) })

	if err := h.db.Jobs().MarkDeleted(bg, actor, d.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "row purged", func() bool {
		_, err := h.db.Jobs().Get(bg, d.ID)
		return err == store.ErrNotFound
	})
	if h.cronJobExists(d) {
		t.Fatal("cronjob left behind")
	}
	if _, exists := h.hasFinalizer(k.Name); exists {
		t.Fatal("Job left behind")
	}
}

// A job's runs mount its owner's home, so a job of someone with no app yet
// gets the home made, as an app would.
func TestAJobGivesItsOwnerAHome(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)
	if _, err := h.db.Homes().ByOwner(bg, h.ns, owner); err != nil {
		t.Fatalf("no home for the job's owner: %v", err)
	}
}

// A job from a template that gives an API token runs with its owner's token,
// made once and kept in a Secret; deleting the job revokes it.
func TestAJobFromATokenTemplateGetsItsOwnersToken(t *testing.T) {
	tokens := &token.DBStore{}
	h := newHarnessWith(t, func(r *Reconciler) {
		tokens.Tokens = r.Store.Tokens()
		r.Tokens, r.APIURL = tokens, "https://javo.example.com"
	})
	h.template(func(s *model.TemplateSpec) { s.APIToken = true })
	// A token is its owner's, so the owner is a person.
	p, err := h.db.People(store.IDRange{}).Ensure(bg, store.Claims{Issuer: "local", Subject: "alice", Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	d := h.definition("nightly", func(j *model.Job) { j.Owner = p.Name })
	h.waitPhase(d, model.JobPhaseReady)
	cj := h.cronJob(d)
	if err := k8s.Get(bg, client.ObjectKeyFromObject(cj), cj); err != nil {
		t.Fatal(err)
	}
	env := cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Env
	i := slices.IndexFunc(env, func(e corev1.EnvVar) bool { return e.Name == "JAVO_TOKEN" })
	if i < 0 || env[i].ValueFrom == nil || env[i].ValueFrom.SecretKeyRef == nil {
		t.Fatalf("env = %+v", env)
	}
	sec := &corev1.Secret{}
	if err := k8s.Get(bg, client.ObjectKey{Namespace: h.ns, Name: env[i].ValueFrom.SecretKeyRef.Name}, sec); err != nil {
		t.Fatal(err)
	}
	got, err := tokens.Authenticate(bg, string(sec.Data[APITokenKey]))
	if err != nil || got.Owner != p.Name || got.Scope != token.ScopeReadWrite {
		t.Fatalf("token = %+v, %v", got, err)
	}

	if err := h.db.Jobs().MarkDeleted(bg, actor, d.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "token revoked", func() bool {
		ok, err := tokens.Exists(bg, got.ID)
		return err == nil && !ok
	})
	eventually(t, "secret gone", func() bool {
		return apierrors.IsNotFound(k8s.Get(bg, client.ObjectKeyFromObject(sec), &corev1.Secret{}))
	})
}

func (h *harness) cronJobEvent(d model.Job, name, reason, message string, at time.Time) {
	h.t.Helper()
	cj := h.cronJob(d)
	if err := k8s.Get(bg, client.ObjectKeyFromObject(cj), cj); err != nil {
		h.t.Fatal(err)
	}
	ev := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns},
		InvolvedObject: corev1.ObjectReference{
			Kind: "CronJob", APIVersion: "batch/v1", Name: cj.Name, Namespace: h.ns, UID: cj.UID,
		},
		Reason: reason, Message: message, Type: corev1.EventTypeNormal,
		FirstTimestamp: metav1.NewTime(at), LastTimestamp: metav1.NewTime(at), Count: 1,
	}
	if err := k8s.Create(bg, ev); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) runsWith(j model.Job, outcome string) []model.Run {
	var out []model.Run
	for _, r := range h.runs(j) {
		if r.Outcome == outcome {
			out = append(out, r)
		}
	}
	return out
}

// A skip is recorded at the slot that was skipped (the latest cron time at or
// before the event), not at the time the event happened.
func TestJobAlreadyActiveEventIsASkippedRunAtTheSkippedSlot(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)
	h.cronJobEvent(d, "skip-1", "JobAlreadyActive", "Not starting job because prior execution is running and concurrency policy is Forbid", fixedNow.Add(-time.Hour))

	eventually(t, "skipped run", func() bool { return len(h.runsWith(d, model.RunOutcomeSkipped)) > 0 })
	run := h.runsWith(d, model.RunOutcomeSkipped)[0]
	slot := time.Date(2026, 10, 29, 5, 0, 0, 0, time.UTC) // 06:00 Berlin, Thursday
	if run.Reason != string(reasons.RunSkipped) || run.Trigger != "schedule" || !run.ScheduledTime.Equal(slot) {
		t.Fatalf("run = %+v, want scheduled %v", run, slot)
	}
}

// The recorder folds repeats of one skip into one Event with a newer
// lastTimestamp; that must stay one run.
func TestRepeatedJobAlreadyActiveEventIsOneSkippedRun(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)
	h.cronJobEvent(d, "skip-1", "JobAlreadyActive", "Not starting job because prior execution is running and concurrency policy is Forbid", fixedNow.Add(-time.Hour))
	eventually(t, "skipped run", func() bool { return len(h.runsWith(d, model.RunOutcomeSkipped)) == 1 })

	ev := &corev1.Event{}
	if err := k8s.Get(bg, client.ObjectKey{Namespace: h.ns, Name: "skip-1"}, ev); err != nil {
		t.Fatal(err)
	}
	ev.Count, ev.LastTimestamp = 2, metav1.NewTime(fixedNow.Add(-10*time.Minute))
	if err := k8s.Update(bg, ev); err != nil {
		t.Fatal(err)
	}
	// Events reach the reconciler in order, so once this later one is recorded
	// the update above has been handled too.
	scheduled := time.Date(2026, 10, 28, 4, 0, 0, 0, time.UTC)
	h.cronJobEvent(d, "miss-1", "MissSchedule", "Missed scheduled time to start a job: "+scheduled.Format(time.RFC1123Z), fixedNow)
	h.waitRun(d, fmt.Sprintf("missed-%d", scheduled.Unix()))

	if n := len(h.runsWith(d, model.RunOutcomeSkipped)); n != 1 {
		t.Fatalf("skipped runs = %d, want 1: %+v", n, h.runs(d))
	}
}

func TestMissScheduleEventIsAMissedRunAtTheScheduledTime(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)
	scheduled := time.Date(2026, 10, 29, 4, 0, 0, 0, time.UTC)
	h.cronJobEvent(d, "miss-1", "MissSchedule", "Missed scheduled time to start a job: "+scheduled.Format(time.RFC1123Z), fixedNow)

	eventually(t, "missed run", func() bool { return len(h.runsWith(d, model.RunOutcomeMissed)) > 0 })
	run := h.runsWith(d, model.RunOutcomeMissed)[0]
	if run.Reason != string(reasons.RunMissed) || !run.ScheduledTime.Equal(scheduled) {
		t.Fatalf("run = %+v", run)
	}
}

func TestReconcilingTwiceWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)
	_, k := h.requestRun(d, nil)
	h.failJob(k, cellErrorMessage)
	h.waitFinalRun(d, k.Name)
	eventually(t, "finalizer removed", func() bool { f, _ := h.hasFinalizer(k.Name); return !f })

	state := func() string {
		cj := h.cronJob(d)
		if err := k8s.Get(bg, client.ObjectKeyFromObject(cj), cj); err != nil {
			t.Fatal(err)
		}
		job := &batchv1.Job{}
		if err := k8s.Get(bg, client.ObjectKeyFromObject(k), job); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal([]any{h.job(d.ID), h.runs(d), cj.ResourceVersion, job.ResourceVersion})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	req := ctrl.Request{NamespacedName: client.ObjectKey{Name: d.ID}}
	// The first call may meet a cache that is a moment behind the server.
	eventually(t, "a clean reconcile", func() bool {
		_, err := h.rec.Reconcile(bg, req)
		return err == nil
	})
	before := state()
	if _, err := h.rec.Reconcile(bg, req); err != nil {
		t.Fatal(err)
	}
	if after := state(); after != before {
		t.Fatalf("a repeat reconcile changed state:\n%s\n%s", before, after)
	}
}

func TestRunWhoseJobVanishedWhileRunningIsLost(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)
	_, k := h.requestRun(d, nil)
	h.waitRun(d, k.Name)

	// Deleted by someone who did not honour the finalizer.
	got := &batchv1.Job{}
	if err := k8s.Get(bg, client.ObjectKeyFromObject(k), got); err != nil {
		t.Fatal(err)
	}
	got.Finalizers = nil
	if err := k8s.Update(bg, got); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Delete(bg, got, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
		t.Fatal(err)
	}

	eventually(t, "lost run", func() bool {
		r, ok := h.runOfK8sJob(d, k.Name)
		return ok && r.Outcome == model.RunOutcomeLost && r.Reason == string(reasons.RunLost) && !r.FinishTime.IsZero()
	})
}

// Once a run is recorded as final it stays as recorded, even if the pods that
// explained it are gone before the Job is.
func TestFinalRunSurvivesLossOfItsPods(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)
	_, k := h.requestRun(d, nil)
	h.failJob(k, cellErrorMessage)
	h.waitFinalRun(d, k.Name)

	// Drop the pod while the Job still exists.
	if err := k8s.Delete(bg, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: k.Name + "-pod", Namespace: h.ns}}); err != nil {
		t.Fatal(err)
	}
	// A later, unrelated run forces a reconcile after the pod is gone.
	_, k2 := h.requestRun(d, nil)
	h.failJob(k2, cellErrorMessage)
	h.waitFinalRun(d, k2.Name)

	run := h.waitFinalRun(d, k.Name)
	if run.Outcome != model.RunOutcomeFailed || run.Reason != string(reasons.CellError) || run.OutputPath != "jobs/nightly/run" {
		t.Fatalf("run = %+v, want the recorded CellError failure", run)
	}
}

// Cancelling a running run deletes its Job; the run ends Cancelled and the
// Job, released from its finalizer, goes.
func TestCancelStopsARunningJob(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)
	req, k := h.requestRun(d, nil)
	h.waitRun(d, k.Name)

	if err := h.db.Jobs().RequestCancel(bg, actor, req.ID); err != nil {
		t.Fatal(err)
	}
	run := h.waitFinalRun(d, k.Name)
	if run.Outcome != model.RunOutcomeCancelled || run.Reason != string(reasons.RunCancelled) {
		t.Fatalf("run = %+v", run)
	}
	eventually(t, "job gone", func() bool { _, exists := h.hasFinalizer(k.Name); return !exists })
}

// A reconciler with no manager: the requests are handled by calling
// startRequested, so what it does with each one is not racing a running
// controller.
func (h *harness) direct() *Reconciler { return h.newReconciler(k8s, k8s) }

func (h *harness) readyVerdict(j model.Job) verdict {
	h.t.Helper()
	tpl := h.template(nil)
	return verdict{phase: model.JobPhaseReady, tpl: &tpl}
}

func TestCancelBeforeTheStartEndsTheRunWithoutAJob(t *testing.T) {
	h := newHarness(t)
	h.stop()
	d := h.definition("nightly", nil)
	v := h.readyVerdict(d)
	run, err := h.db.Jobs().RequestRun(bg, actor, d.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.db.Jobs().RequestCancel(bg, actor, run.ID); err != nil {
		t.Fatal(err)
	}

	if err := h.direct().startRequested(bg, &d, v, fixedNow); err != nil {
		t.Fatal(err)
	}
	got, _ := h.db.Jobs().Run(bg, run.ID)
	if got.Outcome != model.RunOutcomeCancelled || got.Reason != string(reasons.RunCancelled) || got.FinishTime.IsZero() || got.K8sJob != "" {
		t.Fatalf("run = %+v", got)
	}
	var jobs batchv1.JobList
	if err := k8s.List(bg, &jobs, client.InNamespace(h.ns)); err != nil || len(jobs.Items) != 0 {
		t.Fatalf("jobs = %d, %v; want none", len(jobs.Items), err)
	}
}

func TestRequestOnABlockedJobFailsWithTheBlockingReason(t *testing.T) {
	h := newHarness(t)
	h.stop()
	d := h.definition("nightly", nil)
	run, err := h.db.Jobs().RequestRun(bg, actor, d.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	blocked := verdict{phase: model.JobPhaseBlocked, code: reasons.OwnerLostAccess}
	if err := h.direct().startRequested(bg, &d, blocked, fixedNow); err != nil {
		t.Fatal(err)
	}
	got, _ := h.db.Jobs().Run(bg, run.ID)
	if got.Outcome != model.RunOutcomeFailed || got.Reason != string(reasons.OwnerLostAccess) {
		t.Fatalf("run = %+v", got)
	}
}

// A parameter the job stopped declaring after the request was made fails the
// run instead of retrying for ever.
func TestRequestWithAnUndeclaredParameterFails(t *testing.T) {
	h := newHarness(t)
	h.stop()
	d := h.definition("nightly", nil)
	v := h.readyVerdict(d)
	run, err := h.db.Jobs().RequestRun(bg, actor, d.ID, map[string]string{"region": "apac"})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.direct().startRequested(bg, &d, v, fixedNow); err != nil {
		t.Fatal(err)
	}
	got, _ := h.db.Jobs().Run(bg, run.ID)
	if got.Outcome != model.RunOutcomeFailed || got.Reason != string(reasons.ParametersInvalid) {
		t.Fatalf("run = %+v", got)
	}
}

// The Job's name comes from the run row, so starting the same request twice
// (a retry after a crash between creating the Job and recording it) finds the
// Job it made and makes no second one.
func TestRunRequestCreatesOneJob(t *testing.T) {
	h := newHarness(t)
	h.stop()
	d := h.definition("nightly", nil)
	v := h.readyVerdict(d)
	run, err := h.db.Jobs().RequestRun(bg, actor, d.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := h.direct()
	if err := r.startRequested(bg, &d, v, fixedNow); err != nil {
		t.Fatal(err)
	}
	// The crash: the Job exists, the row forgot it.
	storetest.Exec(t, h.db, "UPDATE job_runs SET k8s_job = '', outcome = '', reason = '' WHERE id = ?", run.ID)
	if err := r.startRequested(bg, &d, v, fixedNow); err != nil {
		t.Fatal(err)
	}

	var jobs batchv1.JobList
	if err := k8s.List(bg, &jobs, client.InNamespace(h.ns)); err != nil || len(jobs.Items) != 1 {
		t.Fatalf("jobs = %d, %v; want one", len(jobs.Items), err)
	}
	got, _ := h.db.Jobs().Run(bg, run.ID)
	if got.K8sJob != jobs.Items[0].Name || got.Outcome != model.RunOutcomeRunning {
		t.Fatalf("run = %+v, job %s", got, jobs.Items[0].Name)
	}
}

// Only the newest keepRuns finished runs are kept; the cap is the job's own
// setting, not a fixed list length.
func TestPruneToKeepRuns(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", func(j *model.Job) { j.Spec.History.KeepRuns = 2 })
	h.waitPhase(d, model.JobPhaseReady)
	for i := range 4 {
		if _, err := h.db.Jobs().RecordRun(bg, model.Run{
			JobID: d.ID, Trigger: "schedule", K8sJob: fmt.Sprintf("old-%d", i),
			Outcome: model.RunOutcomeSucceeded, FinishTime: fixedNow.Add(-time.Duration(4-i) * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}

	eventually(t, "two runs kept", func() bool { return len(h.runs(d)) == 2 })
	for _, r := range h.runs(d) {
		if r.K8sJob != "old-3" && r.K8sJob != "old-2" {
			t.Fatalf("kept %s, want the newest two", r.K8sJob)
		}
	}
}

// Finished Jobs the cluster still keeps are never pruned from the history,
// even above keepRuns: pruning one would make the next reconcile record it
// again as a new run, newer than genuinely newer ones, and notify it again.
func TestPruneKeepsRunsOfJobsThatStillExist(t *testing.T) {
	h := newHarness(t)
	tpl := h.template(nil)
	d := h.definition("nightly", func(j *model.Job) { j.Spec.History.KeepRuns = 2 })
	h.waitPhase(d, model.JobPhaseReady)
	names := []string{"nightly-1", "nightly-2", "nightly-3"}
	for i, name := range names {
		k := h.scheduledJobAt(d, tpl, name, fixedNow.Add(-time.Duration(len(names)-i)*time.Hour))
		h.failJob(k, cellErrorMessage)
	}
	ids := func() map[string]string {
		out := map[string]string{}
		for _, r := range h.runs(d) {
			if final(r.Outcome) {
				out[r.K8sJob] = r.ID
			}
		}
		return out
	}
	var first map[string]string
	eventually(t, "all three runs final", func() bool { first = ids(); return len(first) == 3 })

	// Several reconciles later the same rows are there: none was pruned and
	// re-recorded under a new id.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := ids(); !maps.Equal(got, first) {
			t.Fatalf("runs changed from %v to %v: a pruned run came back", first, got)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A skipped start older than the kept runs stays while its CronJob Event is
// listed: pruning it would make every reconcile record it again from the
// Event under a new id, and audit a prune nobody asked for each time.
func TestPruneKeepsSkippedRunWhileItsEventIsListed(t *testing.T) {
	h := newHarness(t)
	tpl := h.template(nil)
	d := h.definition("nightly", func(j *model.Job) { j.Spec.History.KeepRuns = 1 })
	h.waitPhase(d, model.JobPhaseReady)
	h.cronJobEvent(d, "skip-1", "JobAlreadyActive", "Not starting job because prior execution is running and concurrency policy is Forbid", fixedNow.Add(-time.Hour))
	eventually(t, "skipped run", func() bool { return len(h.runsWith(d, model.RunOutcomeSkipped)) == 1 })
	var newer []*batchv1.Job
	for _, name := range []string{"nightly-1", "nightly-2"} {
		k := h.scheduledJobAt(d, tpl, name, fixedNow)
		h.failJob(k, cellErrorMessage)
		newer = append(newer, k)
	}
	eventually(t, "both newer runs final", func() bool { return len(h.runsWith(d, model.RunOutcomeFailed)) == 2 })
	skipped := h.runsWith(d, model.RunOutcomeSkipped)

	prunes := func() int {
		events, err := h.db.Audit().List(bg, 1000)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range events {
			if e.Action == "job.prune_runs" && e.ObjectID == d.ID {
				n++
			}
		}
		return n
	}
	before := prunes()
	// Each touch of a Job is another reconcile.
	for i := range 5 {
		for _, k := range newer {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				got := &batchv1.Job{}
				if err := k8s.Get(bg, client.ObjectKeyFromObject(k), got); err != nil {
					return err
				}
				metav1.SetMetaDataAnnotation(&got.ObjectMeta, "example.com/touch", fmt.Sprint(i))
				return k8s.Update(bg, got)
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	got := h.runsWith(d, model.RunOutcomeSkipped)
	if len(got) != 1 || len(skipped) != 1 || got[0].ID != skipped[0].ID {
		t.Fatalf("skipped run changed from %+v to %+v: it was pruned and recorded again", skipped, got)
	}
	if after := prunes(); after != before {
		t.Fatalf("prune audit rows went from %d to %d", before, after)
	}
}

// The controller writes status and never the spec: an edit made through the
// store keeps its version, and the status catches up with it.
func TestStatusWriteKeepsSpecEdit(t *testing.T) {
	h := newHarness(t)
	h.template(nil)
	d := h.definition("nightly", nil)
	h.waitPhase(d, model.JobPhaseReady)
	edited, err := h.db.Jobs().UpdateSpec(bg, actor, d.ID, h.job(d.ID).Version, func(j *model.Job) error {
		j.Spec.Schedule.Paused = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	got := h.waitPhase(d, model.JobPhasePaused)
	if got.Version != edited.Version || got.Status.ObservedVersion != edited.Version || !got.Spec.Schedule.Paused {
		t.Fatalf("version %d observed %d paused %v, want the edit's version %d kept",
			got.Version, got.Status.ObservedVersion, got.Spec.Schedule.Paused, edited.Version)
	}
}

func (h *harness) runAsUser(j model.Job) int64 {
	cj := h.cronJob(j)
	if err := k8s.Get(bg, client.ObjectKeyFromObject(cj), cj); err != nil {
		return 0
	}
	return ptr.Deref(cj.Spec.JobTemplate.Spec.Template.Spec.SecurityContext.RunAsUser, 0)
}

// With allocated uids, a job runs as its owner, as the owner's apps do; until
// the owner has a uid it is Blocked with nothing scheduled, and it is
// scheduled as soon as the uid is allocated.
func TestJobsRunAsTheOwnersAllocatedUID(t *testing.T) {
	h := newHarnessWith(t, func(r *Reconciler) { r.UIDMode = config.UIDModeAllocated })
	h.template(nil)
	person, err := h.db.People(store.IDRange{}).Ensure(bg, store.Claims{Issuer: "https://idp.example.com", Subject: "dora", Username: "dora"})
	if err != nil {
		t.Fatal(err)
	}
	d := h.definition("nightly", func(j *model.Job) { j.Owner = person.Name })

	got := h.waitPhase(d, model.JobPhaseBlocked)
	if condReason(got, model.ConditionValid) != string(reasons.IdentityPending) {
		t.Fatalf("conditions = %+v", got.Status.Conditions)
	}
	if h.runAsUser(d) != 0 {
		t.Fatal("a job whose owner has no uid must not be scheduled")
	}

	storetest.Exec(t, h.db, "UPDATE people SET uid = ? WHERE id = ?", 70001, person.ID)
	eventually(t, "cronjob runs as the owner's uid", func() bool { return h.runAsUser(d) == 70001 })
	h.waitPhase(d, model.JobPhaseReady)
}

// Where the controller sees more than the one workspace namespace, it builds
// jobs only in namespaces it manages: elsewhere there are no Pod Security
// labels and no gateway-only policy. It looks again later, so a namespace
// labelled after the job was made is picked up.
func TestJobsOnlyInManagedNamespaces(t *testing.T) {
	h := newHarnessWith(t, func(r *Reconciler) { r.ManagedNamespacesOnly = true })
	h.template(nil)
	d := h.definition("nightly", nil)
	req := ctrl.Request{NamespacedName: client.ObjectKey{Name: d.ID}}

	res, err := h.rec.Reconcile(bg, req)
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("unmanaged namespace: result %+v, err %v; want a later look", res, err)
	}
	if h.runAsUser(d) != 0 || h.job(d.ID).Status.Phase != "" {
		t.Fatal("a job in an unmanaged namespace must be left alone")
	}

	ns := &corev1.Namespace{}
	if err := k8s.Get(bg, client.ObjectKey{Name: h.ns}, ns); err != nil {
		t.Fatal(err)
	}
	ns.Labels = map[string]string{model.LabelManaged: "true"}
	if err := k8s.Update(bg, ns); err != nil {
		t.Fatal(err)
	}
	eventually(t, "cronjob once the namespace is managed", func() bool {
		_, _ = h.rec.Reconcile(bg, req)
		return h.runAsUser(d) == podspec.FixedUID
	})
}

// A CronJob or Job whose job row is gone is deleted; one of a live row is
// kept (a row removed while the controller was down must not
// leave its CronJob firing).
func TestSweepDeletesOrphanCronJobsAndJobsOnly(t *testing.T) {
	h := newHarness(t)
	h.stop()
	tpl := h.template(nil)
	live := h.definition("live", nil)
	gone := h.definition("gone", nil)
	cronJobOf := func(j model.Job) *batchv1.CronJob {
		cj, err := BuildCronJob(Inputs{Job: &j, Template: &tpl, RunnerImage: podspec.Image{Ref: "example.com/javo:9"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := k8s.Create(bg, cj); err != nil {
			t.Fatal(err)
		}
		return cj
	}
	jobOf := func(j model.Job, name string) *batchv1.Job {
		return h.scheduledJob(j, tpl, name)
	}
	keepCJ, dropCJ := cronJobOf(live), cronJobOf(gone)
	keepJob, dropJob := jobOf(live, "live-1"), jobOf(gone, "gone-1")
	// A running run's Job carries the finalizer; with the row gone nothing
	// else would ever release it.
	if err := h.direct().addFinalizer(bg, dropJob); err != nil {
		t.Fatal(err)
	}
	// A job of someone else, without the label, is never touched.
	other := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: h.ns}, Spec: dropCJ.Spec}
	if err := k8s.Create(bg, other); err != nil {
		t.Fatal(err)
	}
	// The row goes without the controller: purged directly, as after a restore.
	if err := h.db.Jobs().MarkDeleted(bg, actor, gone.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.db.Jobs().Purge(bg, actor, gone.ID); err != nil {
		t.Fatal(err)
	}

	if err := h.direct().Sweep(bg); err != nil {
		t.Fatal(err)
	}
	// Gone, not merely deleting: the finalizer is released and the pods go
	// with the Job (a foreground or orphan delete would leave it behind).
	for _, o := range []client.Object{dropCJ, dropJob} {
		if err := k8s.Get(bg, client.ObjectKeyFromObject(o), o); !apierrors.IsNotFound(err) {
			t.Errorf("%T %s of a removed job still exists: %v (finalizers %v)", o, o.GetName(), err, o.GetFinalizers())
		}
	}
	for _, o := range []client.Object{keepCJ, keepJob, other} {
		if err := k8s.Get(bg, client.ObjectKeyFromObject(o), o); err != nil {
			t.Errorf("%T %s was deleted: %v", o, o.GetName(), err)
		}
	}
}

package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/cmdline"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/jobs"
	"github.com/nebari-dev/javo/internal/features/placementperuser"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	jobsapi "github.com/nebari-dev/javo/internal/gateway/jobs"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func TestOtherUsersJobIsNotFound(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{}, apitest.JobObj(apitest.AliceID, "nightly-sales"))
	for _, p := range []struct{ method, path string }{
		{"GET", "/api/jobs/nightly-sales"},
		{"POST", "/api/jobs/nightly-sales/run"},
		{"POST", "/api/jobs/nightly-sales/pause"},
		{"POST", "/api/jobs/nightly-sales/resume"},
		{"POST", "/api/jobs/nightly-sales/runs/r1/cancel"},
		{"GET", "/api/jobs/nightly-sales/runs/r1/log"},
		{"DELETE", "/api/jobs/nightly-sales"},
	} {
		w := f.Do(p.method, p.path, "", f.Session(apitest.BobID))
		if w.Code != http.StatusNotFound || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgJobNotFound) {
			t.Errorf("%s %s: status %d: %s", p.method, p.path, w.Code, w.Body)
		}
	}
	if w := f.Do("GET", "/api/jobs", "", f.Session(apitest.BobID)); !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Errorf("bobID's list: %s", w.Body)
	}
}

// A schedule the model rejects is shown on the schedule field it is about.
func TestCreateMapsScheduleErrorsToScheduleFields(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, cron, zone, field string
		want                    func(string) bool
	}{
		{"cron", "0 25 * * *", "UTC", "body.schedule.cron", func(m string) bool { return strings.HasPrefix(m, "This schedule is not valid") }},
		{"zone", "0 6 * * *", "Mars/Base", "body.schedule.timeZone", func(m string) bool { return m == reasons.Msg(reasons.MsgTimeZoneInvalid, "Mars/Base") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := apitest.JobFixture(t, apitest.Options{})
			body := `{"displayName":"Nightly sales","template":"lab","engine":"papermill","source":"reports/sales.ipynb",` +
				`"schedule":{"cron":"` + tc.cron + `","timeZone":"` + tc.zone + `"}}`
			w := f.Do("POST", "/api/jobs", body, f.Session(apitest.AliceID))
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			p := apitest.Decode[apitest.Problem](t, w)
			if len(p.Errors) != 1 || p.Errors[0].Location != tc.field {
				t.Fatalf("errors %+v, want one at %s", p.Errors, tc.field)
			}
			if !tc.want(p.Errors[0].Message) {
				t.Errorf("message %q", p.Errors[0].Message)
			}
			if n := len(f.Jobs()); n != 0 {
				t.Errorf("%d jobs created", n)
			}
		})
	}
}

func TestCreateChecksFileEngineAndKernelAgainstTemplate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, extra, body, loc, msg string }{
		{"file for the wrong engine", "", `{"displayName":"A","template":"lab","engine":"marimo","source":"reports/sales.ipynb"}`,
			"body.source", reasons.Msg(reasons.MsgFileWrongEngine, ".py")},
		{"kernel not in template", "", `{"displayName":"A","template":"lab","engine":"papermill","source":"a.ipynb","kernel":"julia-1.11"}`,
			"body.kernel", reasons.Msg(reasons.MsgKernelNotInTemplate, "lab", "julia-1.11")},
		{"engine not in template", "", `{"displayName":"A","template":"lab","engine":"quarto","source":"a.qmd"}`,
			"body.engine", reasons.Msg(reasons.MsgEngineNotInTemplate, "lab", "quarto")},
		{"template without jobs", "", `{"displayName":"A","template":"plain","engine":"papermill","source":"a.ipynb"}`,
			"body.template", reasons.Msg(reasons.MsgJobsNotOnTemplate)},
		{"timeout over the template maximum", "", `{"displayName":"A","template":"lab","engine":"papermill","source":"a.ipynb","run":{"timeout":"7h"}}`,
			"body.run.timeout", reasons.Msg(reasons.MsgTimeoutTooLong, "6h0m0s")},
		{"source outside the home", "", `{"displayName":"A","template":"lab","engine":"papermill","source":"../a.ipynb"}`,
			"body.source", reasons.Msg(reasons.MsgFileInvalid)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := apitest.JobFixture(t, apitest.Options{}, gatewaytest.Template("plain", "streamlit"))
			w := g.Do("POST", "/api/jobs", tc.body, g.Session(apitest.AliceID))
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage(tc.loc); !ok || msg != tc.msg {
				t.Fatalf("got %q (found %v), want %q at %s: %s", msg, ok, tc.msg, tc.loc, w.Body)
			}
			if n := len(g.Jobs()); n != 0 {
				t.Errorf("%d jobs created", n)
			}
		})
	}
}

func TestCreateStoresTheDefinitionAndShowsItBack(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{})
	body := `{"displayName":"Nightly sales","template":"lab","engine":"papermill","source":"reports/sales.ipynb","kernel":"python3",` +
		`"parameters":[{"name":"region","type":"string","default":"emea"}],"run":{"timeout":"2h","retries":2},` +
		`"schedule":{"cron":"0 6 * * *","timeZone":"Europe/Berlin"}}`
	w := f.Do("POST", "/api/jobs", body, f.Session(apitest.AliceID))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := apitest.Decode[struct {
		Name, Engine, Source, Template, Phase string
		Schedule                              struct {
			Cron, TimeZone, Concurrency string
			Paused                      bool
		}
		Run struct {
			Timeout string
			Retries int
		}
		Parameters []struct{ Name, Type, Default string }
	}](t, w)
	if got.Name != "nightly-sales" || got.Engine != "papermill" || got.Schedule.Cron != "0 6 * * *" ||
		got.Schedule.TimeZone != "Europe/Berlin" || got.Schedule.Concurrency != "Forbid" ||
		got.Run.Timeout != "2h0m0s" || got.Run.Retries != 2 || len(got.Parameters) != 1 || got.Phase != "Pending" {
		t.Fatalf("view %+v", got)
	}
	defs := f.Jobs()
	if len(defs) != 1 || defs[0].Owner != apitest.AliceID || defs[0].Name != "nightly-sales" || defs[0].Namespace != gatewaytest.Namespace ||
		defs[0].Spec.Run.Retries != 2 || defs[0].Spec.Schedule.TimeZone != "Europe/Berlin" {
		t.Fatalf("stored %+v", defs)
	}
	if w := f.Do("POST", "/api/jobs", body, f.Session(apitest.AliceID)); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("same name again: status %d", w.Code)
	}
	// An update keeps what the form does not carry: history and env.
	if _, err := f.Db.Jobs().UpdateSpec(context.Background(), gatewaytest.Actor, defs[0].ID, defs[0].Version, func(j *model.Job) error {
		j.Spec.History.KeepRuns = 7
		j.Spec.Env = []model.EnvVar{{Name: "A", Value: "b"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	up := strings.Replace(body, `"retries":2`, `"retries":1`, 1)
	if w := f.Do("PUT", "/api/jobs/nightly-sales", up, f.Session(apitest.AliceID)); w.Code != http.StatusOK {
		t.Fatalf("update status %d: %s", w.Code, w.Body)
	}
	if d := f.Jobs()[0]; d.Spec.Run.Retries != 1 || d.Spec.History.KeepRuns != 7 || len(d.Spec.Env) != 1 {
		t.Errorf("after update %+v", d.Spec)
	}
}

// Running a job now is a request the controller acts on: the gateway adds a
// run with the parameter values and writes nothing to Kubernetes.
func TestRunNowCreatesRequest(t *testing.T) {
	t.Parallel()
	def := apitest.JobObj(apitest.AliceID, "nightly-sales")
	def.Spec.Parameters = []model.JobParameter{{Name: "region", Type: "string", Default: "emea"}}
	f := apitest.JobFixture(t, apitest.Options{}, def)

	w := f.Do("POST", "/api/jobs/nightly-sales/run", `{"parameters":{"region":"apac"}}`, f.Session(apitest.AliceID))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	run := apitest.Decode[struct {
		ID, Trigger, Outcome string
		Parameters           map[string]string
	}](t, w)
	runs := f.RunsOf(apitest.AliceID, "nightly-sales")
	if len(runs) != 1 || runs[0].ID != run.ID || runs[0].Trigger != store.TriggerManual || runs[0].Outcome != "" ||
		runs[0].K8sJob != "" || runs[0].Parameters["region"] != "apac" || runs[0].RequestedAt.IsZero() {
		t.Fatalf("response %+v, stored runs %+v", run, runs)
	}
	// Until the controller starts it the run shows as running.
	if run.Trigger != "manual" || run.Outcome != model.RunOutcomeRunning || run.Parameters["region"] != "apac" {
		t.Errorf("response %+v", run)
	}

	w = f.Do("POST", "/api/jobs/nightly-sales/run", `{"parameters":{"nope":"x"}}`, f.Session(apitest.AliceID))
	if w.Code != http.StatusUnprocessableEntity || len(f.RunsOf(apitest.AliceID, "nightly-sales")) != 1 {
		t.Errorf("unknown parameter: status %d, %d runs: %s", w.Code, len(f.RunsOf(apitest.AliceID, "nightly-sales")), w.Body)
	}
	if w := f.Do("POST", "/api/jobs/nightly-sales/run", "", f.Session(apitest.AliceID)); w.Code != http.StatusCreated {
		t.Errorf("run without a body: status %d: %s", w.Code, w.Body)
	}
	// One-off values have the same bound as a saved default.
	n := len(f.RunsOf(apitest.AliceID, "nightly-sales"))
	w = f.Do("POST", "/api/jobs/nightly-sales/run", `{"parameters":{"region":"`+strings.Repeat("x", 1025)+`"}}`, f.Session(apitest.AliceID))
	if w.Code != http.StatusUnprocessableEntity || len(f.RunsOf(apitest.AliceID, "nightly-sales")) != n {
		t.Errorf("oversized value: status %d, %d runs: %s", w.Code, len(f.RunsOf(apitest.AliceID, "nightly-sales")), w.Body)
	}
	if w := f.Do("POST", "/api/jobs/nightly-sales/run", `{"parameters":{"region":"`+strings.Repeat("x", 1024)+`"}}`, f.Session(apitest.AliceID)); w.Code != http.StatusCreated {
		t.Errorf("value at the bound: status %d: %s", w.Code, w.Body)
	}
}

func TestPausedJobDetailWarnsWhenAScheduledRunWasMissed(t *testing.T) {
	t.Parallel()
	def := apitest.JobObj(apitest.AliceID, "nightly-sales")
	def.Spec.Schedule = &model.JobSchedule{Cron: "* * * * *", TimeZone: "UTC", Paused: true}
	last := time.Now().Add(-3 * time.Hour)
	def.Status.LastScheduleTime = last
	f := apitest.JobFixture(t, apitest.Options{}, def)

	w := f.Do("GET", "/api/jobs/nightly-sales", "", f.Session(apitest.AliceID))
	if n := apitest.Decode[struct{ ResumeNotice string }](t, w).ResumeNotice; n != reasons.Msg(reasons.MsgResumeStartsMissedRun) {
		t.Errorf("resumeNotice %q", n)
	}
	if w := f.Do("POST", "/api/jobs/nightly-sales/resume", "", f.Session(apitest.AliceID)); w.Code != http.StatusOK {
		t.Fatalf("resume status %d: %s", w.Code, w.Body)
	}
	if f.Jobs()[0].Spec.Schedule.Paused {
		t.Error("still paused")
	}
	// Running schedules have nothing to warn about.
	if n := apitest.Decode[struct{ ResumeNotice string }](t, f.Do("GET", "/api/jobs/nightly-sales", "", f.Session(apitest.AliceID))).ResumeNotice; n != "" {
		t.Errorf("resumeNotice on a running schedule: %q", n)
	}

	// With a starting deadline the missed run is dropped, so no notice.
	def2 := apitest.JobObj(apitest.AliceID, "weekly")
	def2.Spec.Schedule = &model.JobSchedule{Cron: "* * * * *", TimeZone: "UTC", Paused: true,
		StartingDeadline: &metav1.Duration{Duration: time.Minute}}
	def2.Status.LastScheduleTime = last
	g := apitest.JobFixture(t, apitest.Options{}, def2)
	if n := apitest.Decode[struct{ ResumeNotice string }](t, g.Do("GET", "/api/jobs/weekly", "", g.Session(apitest.AliceID))).ResumeNotice; n != "" {
		t.Errorf("resumeNotice with a starting deadline: %q", n)
	}
}

func TestJobDetailRunsCarryTheirParameters(t *testing.T) {
	t.Parallel()
	def := gatewaytest.WithRuns{Job: apitest.JobObj(apitest.AliceID, "nightly-sales"), Runs: []model.Run{
		{Trigger: "manual", Outcome: "Failed", Parameters: map[string]string{"region": "apac"}}}}
	f := apitest.JobFixture(t, apitest.Options{}, def)
	got := apitest.Decode[struct {
		Runs []struct{ Parameters map[string]string }
	}](t, f.Do("GET", "/api/jobs/nightly-sales", "", f.Session(apitest.AliceID)))
	if len(got.Runs) != 1 || got.Runs[0].Parameters["region"] != "apac" {
		t.Errorf("runs %+v", got.Runs)
	}
}

func TestPauseSetsThePausedFlag(t *testing.T) {
	t.Parallel()
	def := apitest.JobObj(apitest.AliceID, "nightly-sales")
	def.Spec.Schedule = &model.JobSchedule{Cron: "0 6 * * *", TimeZone: "UTC"}
	f := apitest.JobFixture(t, apitest.Options{}, def)
	if w := f.Do("POST", "/api/jobs/nightly-sales/pause", "", f.Session(apitest.AliceID)); w.Code != http.StatusOK || !f.Jobs()[0].Spec.Schedule.Paused {
		t.Fatalf("status %d, stored %+v", w.Code, f.Jobs()[0].Spec.Schedule)
	}
}

func TestLogOfARunWhosePodIsGoneIsTheSavedLogFromTheRunFolder(t *testing.T) {
	t.Parallel()
	home := &apitest.StubFilesLister{Content: "Sat 31 Oct 10:00:00 UTC 2026\n"}
	f := apitest.JobFixture(t, apitest.Options{Files: home}, apitest.Sales29())
	run := f.RunsOf(apitest.AliceID, "nightly-sales")[0]

	w := f.Do("GET", "/api/jobs/nightly-sales/runs/"+run.ID+"/log", "", f.Session(apitest.AliceID))
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("status %d, type %q: %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	if got := w.Header().Get("X-Log-Source"); got != "saved" {
		t.Errorf("X-Log-Source %q, want saved", got)
	}
	if w.Body.String() != "Sat 31 Oct 10:00:00 UTC 2026\n" {
		t.Errorf("body %q", w.Body)
	}
	if want := apitest.AliceID + ":jobs/nightly-sales/nightly-sales-29/run.log"; len(home.Opened) != 1 || home.Opened[0] != want {
		t.Errorf("opened %v, want %s", home.Opened, want)
	}
}

func TestLogOfARunWithNeitherPodNorSavedLogIs410InPlainWords(t *testing.T) {
	t.Parallel()
	for name, o := range map[string]apitest.Options{
		"file missing": {Files: &apitest.StubFilesLister{Err: files.ErrNotFound}},
		"no homes":     {},
	} {
		t.Run(name, func(t *testing.T) {
			f := apitest.JobFixture(t, o, apitest.Sales29())
			run := f.RunsOf(apitest.AliceID, "nightly-sales")[0]
			w := f.Do("GET", "/api/jobs/nightly-sales/runs/"+run.ID+"/log", "", f.Session(apitest.AliceID))
			if w.Code != http.StatusGone {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			title := apitest.Decode[apitest.Problem](t, w).Title
			if title != reasons.Msg(reasons.MsgLogsGone) || strings.Contains(title, "jobs/") {
				t.Errorf("title %q", title)
			}
			if w := f.Do("GET", "/api/jobs/nightly-sales/runs/unknown/log", "", f.Session(apitest.AliceID)); w.Code != http.StatusNotFound ||
				apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgRunNotFound) {
				t.Errorf("unknown run: status %d: %s", w.Code, w.Body)
			}
		})
	}
}

func TestSavedLogWhileTheHomeIsStartingAsksAgain(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{Files: &apitest.StubFilesLister{Err: files.ErrHomeStarting}}, apitest.Sales29())
	run := f.RunsOf(apitest.AliceID, "nightly-sales")[0]
	w := f.Do("GET", "/api/jobs/nightly-sales/runs/"+run.ID+"/log", "", f.Session(apitest.AliceID))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d, retry %q: %s", w.Code, w.Header().Get("Retry-After"), w.Body)
	}
}

func TestLogStreamsTheRunPodAsPlainText(t *testing.T) {
	t.Parallel()
	def := gatewaytest.WithRuns{Job: apitest.JobObj(apitest.AliceID, "nightly-sales"), Runs: []model.Run{
		{K8sJob: "nightly-sales-30", Trigger: "schedule", Outcome: model.RunOutcomeRunning}}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly-sales-30-abc", Namespace: gatewaytest.Namespace, Labels: map[string]string{
			jobs.LabelJobName: "nightly-sales-30", model.LabelOwner: apitest.AliceID}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}
	logs := apitest.NewPipeLogs()
	f := apitest.JobFixture(t, apitest.Options{Logs: logs}, def)
	f.AddRunPod(apitest.AliceID, "nightly-sales", pod)
	run := f.RunsOf(apitest.AliceID, "nightly-sales")[0]
	srv := httptest.NewServer(f.Srv)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/jobs/nightly-sales/runs/"+run.ID+"/log", nil)
	req.AddCookie(f.Session(apitest.AliceID))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("status %d, type %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if got := res.Header.Get("X-Log-Source"); got != "live" {
		t.Errorf("X-Log-Source %q, want live", got)
	}
	if opts := <-logs.Opts; !opts.Follow || opts.Container != "app" {
		t.Errorf("options %+v", opts)
	}
	io.WriteString(logs.W, "cell 1 done\n")
	buf := make([]byte, len("cell 1 done\n"))
	if _, err := io.ReadFull(res.Body, buf); err != nil || string(buf) != "cell 1 done\n" {
		t.Fatalf("read %q, %v", buf, err)
	}
}

// Cancelling is a request the controller acts on: the gateway marks the run
// and deletes nothing in Kubernetes. A finished run is left alone, and a run
// of another job is not found.
func TestCancelAsksTheControllerToStopTheRun(t *testing.T) {
	t.Parallel()
	def := gatewaytest.WithRuns{Job: apitest.JobObj(apitest.AliceID, "nightly-sales"), Runs: []model.Run{
		{K8sJob: "nightly-sales-1", Trigger: "schedule", Outcome: model.RunOutcomeRunning},
		{K8sJob: "nightly-sales-2", Trigger: "schedule", Outcome: model.RunOutcomeSucceeded},
	}}
	other := gatewaytest.WithRuns{Job: apitest.JobObj(apitest.AliceID, "weekly"), Runs: []model.Run{
		{K8sJob: "weekly-1", Trigger: "schedule", Outcome: model.RunOutcomeRunning},
	}}
	deletes := 0
	funcs := &interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		deletes++
		return c.Delete(ctx, obj, opts...)
	}}
	f := apitest.JobFixture(t, apitest.Options{Funcs: funcs}, def, other)
	byJob := func(k8sJob string) model.Run {
		t.Helper()
		for _, owner := range []string{"nightly-sales", "weekly"} {
			for _, r := range f.RunsOf(apitest.AliceID, owner) {
				if r.K8sJob == k8sJob {
					return r
				}
			}
		}
		t.Fatalf("no run for %s", k8sJob)
		return model.Run{}
	}
	active, done, foreign := byJob("nightly-sales-1"), byJob("nightly-sales-2"), byJob("weekly-1")

	w := f.Do("POST", "/api/jobs/nightly-sales/runs/"+active.ID+"/cancel", "", f.Session(apitest.AliceID))
	if w.Code != http.StatusOK || apitest.Decode[struct{ Outcome string }](t, w).Outcome != model.RunOutcomeCancelled {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if !byJob("nightly-sales-1").CancelRequested {
		t.Error("the run was not marked for cancelling")
	}
	// A finished run is left alone.
	if w := f.Do("POST", "/api/jobs/nightly-sales/runs/"+done.ID+"/cancel", "", f.Session(apitest.AliceID)); w.Code != http.StatusOK ||
		byJob("nightly-sales-2").CancelRequested || apitest.Decode[struct{ Outcome string }](t, w).Outcome != model.RunOutcomeSucceeded {
		t.Errorf("finished run: status %d: %s", w.Code, w.Body)
	}
	if w := f.Do("POST", "/api/jobs/nightly-sales/runs/missing/cancel", "", f.Session(apitest.AliceID)); w.Code != http.StatusNotFound {
		t.Errorf("missing run: status %d", w.Code)
	}
	if w := f.Do("POST", "/api/jobs/nightly-sales/runs/"+foreign.ID+"/cancel", "", f.Session(apitest.AliceID)); w.Code != http.StatusNotFound ||
		byJob("weekly-1").CancelRequested {
		t.Errorf("another job's run: status %d", w.Code)
	}
	if deletes != 0 {
		t.Errorf("%d deletes sent to Kubernetes", deletes)
	}
}

func TestJobViewListsRunsOldestFirstInTheStripAndExplainsAFailure(t *testing.T) {
	t.Parallel()
	def := apitest.JobObj(apitest.AliceID, "nightly-sales")
	def.Spec.Schedule = &model.JobSchedule{Cron: "0 6 * * *", TimeZone: "Europe/Berlin", Concurrency: "Forbid"}
	def.Status.Phase = model.JobPhaseReady
	Next := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	def.Status.NextRunTimes = []time.Time{Next}
	start := time.Date(2026, 10, 31, 5, 0, 0, 0, time.UTC)
	seeded := gatewaytest.WithRuns{Job: def}
	for i := 0; i < 13; i++ { // oldest first, as they were recorded
		seeded.Runs = append(seeded.Runs, model.Run{K8sJob: "r" + string(rune('a'+i)), Trigger: "schedule", Outcome: "Succeeded", StartTime: start})
	}
	seeded.Runs = append(seeded.Runs, model.Run{K8sJob: "newest", Trigger: "schedule", Outcome: "Failed", Reason: "KernelDied",
		Message: "Cell 3: ZeroDivisionError", StartTime: start,
		OutputPath: "jobs/nightly-sales/newest", Outputs: []string{"output.ipynb", "output.html"}})
	f := apitest.JobFixture(t, apitest.Options{}, seeded)
	newest := f.RunsOf(apitest.AliceID, "nightly-sales")[0]

	w := f.Do("GET", "/api/jobs", "", f.Session(apitest.AliceID))
	list := apitest.Decode[struct {
		Items []struct {
			Name   string
			Recent []string
			Phase  string
			Next   []string                            `json:"nextRuns"`
			Last   struct{ ID, Outcome, Title string } `json:"lastRun"`
			Prob   struct {
				Reason, Title string
				Evidence      []string
			} `json:"problem"`
		}
	}](t, w)
	if len(list.Items) != 1 {
		t.Fatalf("list %s", w.Body)
	}
	it := list.Items[0]
	if len(it.Recent) != 12 || it.Recent[11] != "Failed" || it.Recent[0] != "Succeeded" {
		t.Errorf("recent %v", it.Recent)
	}
	if len(it.Next) != 1 || it.Next[0] != "2026-11-01T05:00:00Z" || it.Last.ID != newest.ID || it.Last.Title == "" {
		t.Errorf("next %v last %+v", it.Next, it.Last)
	}
	if it.Prob.Reason != "KernelDied" || it.Prob.Title == "" || len(it.Prob.Evidence) != 1 || it.Prob.Evidence[0] != "Cell 3: ZeroDivisionError" {
		t.Errorf("problem %+v", it.Prob)
	}

	d := apitest.Decode[struct {
		Runs []struct {
			ID, OutputPath string
			Outputs        []string
		}
	}](t, f.Do("GET", "/api/jobs/nightly-sales", "", f.Session(apitest.AliceID)))
	if len(d.Runs) != 14 || d.Runs[0].ID != newest.ID {
		t.Fatalf("detail runs %+v", d.Runs[:1])
	}
	// Links go to files, never the run folder, with the viewable HTML first.
	want := []string{"jobs/nightly-sales/newest/output.html", "jobs/nightly-sales/newest/output.ipynb"}
	if !slices.Equal(d.Runs[0].Outputs, want) {
		t.Errorf("outputs %v, want %v", d.Runs[0].Outputs, want)
	}
	if len(d.Runs[1].Outputs) != 0 {
		t.Errorf("a run with no output files links %v", d.Runs[1].Outputs)
	}
}

// Deleting marks the job; the controller removes its CronJob and Jobs and
// then the row. Until then the job is gone from every route.
func TestDeleteRemovesTheDefinition(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{}, apitest.JobObj(apitest.AliceID, "nightly-sales"))
	if w := f.Do("DELETE", "/api/jobs/nightly-sales", "", f.Session(apitest.AliceID)); w.Code != http.StatusNoContent || len(f.Jobs()) != 0 {
		t.Fatalf("status %d, %d left", w.Code, len(f.Jobs()))
	}
	if w := f.Do("GET", "/api/jobs/nightly-sales", "", f.Session(apitest.AliceID)); w.Code != http.StatusNotFound {
		t.Errorf("a deleted job is still read: %d", w.Code)
	}
	if got := apitest.Decode[struct{ Items []jobsapi.JobView }](t, f.Do("GET", "/api/jobs", "", f.Session(apitest.AliceID))); len(got.Items) != 0 {
		t.Errorf("a deleted job is listed: %+v", got.Items)
	}
}

func TestSchedulePreviewMatchesTheCronJobControllerAndWarnsAboutDST(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{})
	w := f.Do("POST", "/api/schedule/preview", `{"cron":"30 2 * * *","timeZone":"Europe/Berlin"}`, f.Session(apitest.AliceID))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	p := apitest.Decode[struct {
		Next     []string
		Warnings []struct {
			Code, Text, Date, Time, Until string
			Runs                          int
		}
	}](t, w)
	if len(p.Next) != 5 {
		t.Fatalf("next %v", p.Next)
	}
	if _, err := time.Parse(time.RFC3339, p.Next[0]); err != nil || !strings.HasSuffix(p.Next[0], "Z") {
		t.Errorf("first time %q is not RFC 3339 UTC", p.Next[0])
	}
	if len(p.Warnings) == 0 || p.Warnings[0].Code != string(reasons.MsgDSTSkipped) && p.Warnings[0].Code != string(reasons.MsgDSTRepeated) || p.Warnings[0].Text == "" {
		t.Errorf("warnings %+v", p.Warnings)
	}
	// The parts a client needs to say it in its own words and date format.
	for _, wn := range p.Warnings {
		if _, err := time.Parse(time.DateOnly, wn.Date); err != nil || wn.Time != "02:30" || wn.Until != "02:30" || wn.Runs != 1 {
			t.Errorf("warning parts %+v", wn)
		}
		if strings.Contains(wn.Text, wn.Date) || !strings.Contains(wn.Text, "the 02:30 run") {
			t.Errorf("text %q should name the day in words and the run", wn.Text)
		}
	}

	for _, tc := range []struct{ body, loc string }{
		{`{"cron":"0 25 * * *","timeZone":"UTC"}`, "body.cron"},
		{`{"cron":"0 6 * * *","timeZone":"Mars/Base"}`, "body.timeZone"},
	} {
		w := f.Do("POST", "/api/schedule/preview", tc.body, f.Session(apitest.AliceID))
		if _, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage(tc.loc); w.Code != http.StatusUnprocessableEntity || !ok {
			t.Errorf("%s: status %d: %s", tc.body, w.Code, w.Body)
		}
	}
}

func TestEnginesComeFromTheRegistry(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{})
	w := f.Do("GET", "/api/engines", "", f.Session(apitest.AliceID))
	got := apitest.Decode[struct {
		Items []struct {
			ID, DisplayName string
			Extensions      []string
		}
	}](t, w)
	var ids []string
	for _, e := range got.Items {
		ids = append(ids, e.ID)
		if e.DisplayName == "" || e.Extensions == nil {
			t.Errorf("engine %+v", e)
		}
	}
	if strings.Join(ids, ",") != "bash,command,marimo,papermill" {
		t.Errorf("ids %v", ids)
	}
	b, _ := json.Marshal(got.Items)
	if !strings.Contains(string(b), `".ipynb"`) {
		t.Errorf("papermill extensions missing: %s", b)
	}
}

func TestTemplatesShowJobSettingsBasePathAndAppLogin(t *testing.T) {
	t.Parallel()
	tpl := apitest.JobTemplateObj()
	tpl.Spec.BasePath, tpl.Spec.AppLogin = "Strip", "Disabled"
	f := apitest.New(t, apitest.Options{Engines: apitest.TestEngines()}, tpl, gatewaytest.Template("plain", "streamlit"))
	got := apitest.Decode[struct {
		Items []struct {
			Name, BasePath, AppLogin string
			Jobs                     *struct {
				Engines, Kernels []string
				MaxTimeout       string
			}
		}
	}](t, f.Do("GET", "/api/templates", "", f.Session(apitest.AliceID)))
	byName := map[string]int{}
	for i, it := range got.Items {
		byName[it.Name] = i
	}
	lab, plain := got.Items[byName["lab"]], got.Items[byName["plain"]]
	if lab.BasePath != "Strip" || lab.AppLogin != "Disabled" || lab.Jobs == nil || lab.Jobs.MaxTimeout != "6h0m0s" ||
		strings.Join(lab.Jobs.Engines, ",") != "papermill,marimo,command" || strings.Join(lab.Jobs.Kernels, ",") != "python3" {
		t.Errorf("lab %+v", lab)
	}
	if plain.Jobs != nil {
		t.Errorf("plain has jobs %+v", plain.Jobs)
	}
}

func TestRunNowRefusesWhenTheUserLeftTheTemplateGroups(t *testing.T) {
	t.Parallel()
	tpl := apitest.JobTemplateObj()
	tpl.Spec.AllowedGroups = []string{"data-team"}
	f := apitest.New(t, apitest.Options{Engines: apitest.TestEngines()}, tpl, apitest.JobObj(apitest.AliceID, "nightly-sales"))

	w := f.Do("POST", "/api/jobs/nightly-sales/run", "", f.Session(apitest.AliceID))
	if w.Code != http.StatusUnprocessableEntity || len(f.RunsOf(apitest.AliceID, "nightly-sales")) != 0 {
		t.Fatalf("status %d, %d runs: %s", w.Code, len(f.RunsOf(apitest.AliceID, "nightly-sales")), w.Body)
	}
}

// A template that turned jobs off since the job was saved refuses a run the
// controller could not start, and no run is stored.
func TestRunNowRefusesWhenTheTemplateNoLongerAllowsJobs(t *testing.T) {
	t.Parallel()
	tpl := apitest.JobTemplateObj()
	tpl.Spec.Jobs = nil
	f := apitest.New(t, apitest.Options{Engines: apitest.TestEngines()}, tpl, apitest.JobObj(apitest.AliceID, "nightly-sales"))

	w := f.Do("POST", "/api/jobs/nightly-sales/run", "", f.Session(apitest.AliceID))
	if w.Code != http.StatusUnprocessableEntity || len(f.RunsOf(apitest.AliceID, "nightly-sales")) != 0 {
		t.Fatalf("status %d, %d runs: %s", w.Code, len(f.RunsOf(apitest.AliceID, "nightly-sales")), w.Body)
	}
	if p := apitest.Decode[apitest.Problem](t, w); len(p.Errors) != 1 || p.Errors[0].Location != "body.template" ||
		p.Errors[0].Message != reasons.Msg(reasons.MsgJobsNotOnTemplate) {
		t.Fatalf("errors %+v", p.Errors)
	}
}

func TestNewServerRefusesWhatItCannotWorkWithout(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	full := gateway.Config{Store: db, Engines: apitest.TestEngines(), NetworkPolicy: &netprobe.Store{}, Tokens: &token.DBStore{}, Machines: capacity.Nodes{}}
	for name, cut := range map[string]func(*gateway.Config){
		"no store":          func(c *gateway.Config) { c.Store = nil },
		"no engines":        func(c *gateway.Config) { c.Engines = nil },
		"no network policy": func(c *gateway.Config) { c.NetworkPolicy = nil },
		"no tokens":         func(c *gateway.Config) { c.Tokens = nil },
		"no machines":       func(c *gateway.Config) { c.Machines = nil },
	} {
		cfg := full
		cut(&cfg)
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: NewServer did not fail", name)
				}
			}()
			gateway.NewServer(cfg)
		}()
	}
}

func TestLogOfAnActiveRunWhoseContainerHasNotStartedIs409WithRetryAfter(t *testing.T) {
	t.Parallel()
	pending := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly-sales-31-abc", Namespace: gatewaytest.Namespace, Labels: map[string]string{
			jobs.LabelJobName: "nightly-sales-31", model.LabelOwner: apitest.AliceID}},
		Status: corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}}},
	}
	started := model.Run{K8sJob: "nightly-sales-31", Trigger: "schedule", Outcome: model.RunOutcomeRunning}
	requested := model.Run{Trigger: store.TriggerManual} // nobody has started it yet
	for name, tc := range map[string]struct {
		run  model.Run
		pods []*corev1.Pod
	}{
		"pod pending":     {started, []*corev1.Pod{pending}},
		"no pod yet":      {started, nil},
		"not yet started": {requested, nil},
	} {
		def := gatewaytest.WithRuns{Job: apitest.JobObj(apitest.AliceID, "nightly-sales"), Runs: []model.Run{tc.run}}
		f := apitest.JobFixture(t, apitest.Options{Logs: apitest.NewPipeLogs()}, def)
		for _, p := range tc.pods {
			f.AddRunPod(apitest.AliceID, "nightly-sales", p.DeepCopy())
		}
		run := f.RunsOf(apitest.AliceID, "nightly-sales")[0]
		w := f.Do("GET", "/api/jobs/nightly-sales/runs/"+run.ID+"/log", "", f.Session(apitest.AliceID))
		if w.Code != http.StatusConflict || w.Header().Get("Retry-After") == "" {
			t.Errorf("%s: status %d, Retry-After %q: %s", name, w.Code, w.Header().Get("Retry-After"), w.Body)
			continue
		}
		if got, want := apitest.Decode[apitest.Problem](t, w).Title, reasons.Msg(reasons.MsgRunNotStarted); got != want {
			t.Errorf("%s: title %q, want %q", name, got, want)
		}
	}
}

// Under per-user namespaces a job lives in its owner's namespace, next to the
// owner's apps, and is found, run and deleted there; another person neither
// lists nor reaches it.
func TestPerUserJobLivesInTheOwnersNamespace(t *testing.T) {
	t.Parallel()
	pl := placementperuser.New(config.Namespaces{Prefix: "javo"})
	f := apitest.JobFixture(t, apitest.Options{Placement: pl}, apitest.ManagedNS("javo-"+apitest.AliceID), apitest.ManagedNS("javo-"+apitest.BobID))
	alice, bob := f.Session(apitest.AliceID), f.Session(apitest.BobID)

	if w := f.Do("POST", "/api/jobs", fmt.Sprintf(apitest.NewJob, ""), alice); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if got := f.Jobs(); len(got) != 1 || got[0].Namespace != "javo-"+apitest.AliceID {
		t.Fatalf("job not in alice's namespace: %+v", got)
	}
	if w := f.Do("POST", "/api/jobs/nightly-sales/run", "", alice); w.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", w.Code, w.Body)
	}
	if n := len(f.RunsOf(apitest.AliceID, "nightly-sales")); n != 1 {
		t.Fatalf("%d runs", n)
	}
	if got := apitest.Decode[struct{ Items []jobsapi.JobView }](t, f.Do("GET", "/api/jobs", "", bob)); len(got.Items) != 0 {
		t.Fatalf("bob lists %+v", got.Items)
	}
	if w := f.Do("GET", "/api/jobs/nightly-sales", "", bob); w.Code != http.StatusNotFound {
		t.Fatalf("bob reads alice's job: %d", w.Code)
	}
	if w := f.Do("DELETE", "/api/jobs/nightly-sales", "", alice); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
}

// A platform admin reaches someone else's job with ?owner=, as for apps;
// anyone else gets the same 404 as a missing job, and the admin's own list
// keeps only the admin's jobs.
func TestAdminReachesAnothersJobWithOwner(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{}, apitest.JobObj(apitest.AliceID, "nightly-sales"))
	admin, bob := f.Session(apitest.CarolID, "javo-admins"), f.Session(apitest.BobID)
	q := "/api/jobs/nightly-sales"
	if w := f.Do("GET", q+"?owner="+apitest.AliceID, "", admin); w.Code != http.StatusOK {
		t.Fatalf("admin get: %d %s", w.Code, w.Body)
	}
	if w := f.Do("POST", q+"/pause?owner="+apitest.AliceID, "", admin); w.Code != http.StatusOK {
		t.Fatalf("admin pause: %d %s", w.Code, w.Body)
	}
	if w := f.Do("GET", q+"?owner="+apitest.AliceID, "", bob); w.Code != http.StatusNotFound {
		t.Fatalf("bob with owner=: %d", w.Code)
	}
	if got := apitest.Decode[struct{ Items []jobsapi.JobView }](t, f.Do("GET", "/api/jobs", "", admin)); len(got.Items) != 0 {
		t.Fatalf("admin's own list shows %+v", got.Items)
	}
	if w := f.Do("PUT", q+"?owner="+apitest.AliceID, fmt.Sprintf(apitest.NewJob, ""), admin); w.Code != http.StatusOK {
		t.Fatalf("admin update: %d %s", w.Code, w.Body)
	}
	if d := f.Jobs()[0]; d.Owner != apitest.AliceID {
		t.Fatalf("admin's change took the job over: owner %q", d.Owner)
	}
}

// A command job is a command line, read like an app's typed command, with
// no file; the job shows it back as a line.
func TestCreateCommandJobSplitsTheLineAndShowsItBack(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{})
	body := `{"displayName":"Print the time","template":"lab","engine":"command","command":"bash '{{.Home}}/my jobs/run.sh' --n 2",` +
		`"parameters":[{"name":"greeting","type":"string","default":"hello"}]}`
	w := f.Do("POST", "/api/jobs", body, f.Session(apitest.AliceID))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	defs := f.Jobs()
	if len(defs) != 1 || !slices.Equal(defs[0].Spec.Command, []string{"bash", "/home/user/my jobs/run.sh", "--n", "2"}) || defs[0].Spec.Source.Path != "" {
		t.Fatalf("stored %+v", defs)
	}
	got := apitest.Decode[struct{ Engine, Source, Command string }](t, w)
	if got.Engine != "command" || got.Source != "" || got.Command != "bash '/home/user/my jobs/run.sh' --n 2" {
		t.Errorf("view %+v", got)
	}
	if w := f.Do("GET", "/api/jobs", "", f.Session(apitest.AliceID)); !strings.Contains(w.Body.String(), `"command":"bash '/home/user/my jobs/run.sh' --n 2"`) {
		t.Errorf("list does not show the command: %s", w.Body)
	}
}

func TestCreateChecksTheCommandLine(t *testing.T) {
	t.Parallel()
	cmd := func(line string) string {
		return `{"displayName":"A","template":"lab","engine":"command","command":` + apitest.StrconvQuote(line) + `}`
	}
	var syn *cmdline.SyntaxError
	if _, err := cmdline.Split("date | tee log"); !errors.As(err, &syn) {
		t.Fatalf("Split: %v", err)
	}
	for _, tc := range []struct{ name, body, msg string }{
		{"missing", `{"displayName":"A","template":"lab","engine":"command"}`, reasons.Msg(reasons.MsgJobCommandRequired)},
		{"variable", cmd("echo $JAVO_PARAM_GREETING"), reasons.Msg(reasons.MsgJobCommandVariable)},
		{"app placeholder", cmd("serve --port={{.Port}}"), reasons.Msg(reasons.MsgJobCommandPlaceholder)},
		{"pipe", cmd("date | tee log"), reasons.Msg(reasons.MsgCommandInvalid, syn.Detail)},
		{"too long", cmd("echo " + strings.Repeat("a", 2048)), reasons.Msg(reasons.MsgCommandTooLong)},
		{"command on a file job", `{"displayName":"A","template":"lab","engine":"papermill","source":"a.ipynb","command":"date"}`, reasons.Msg(reasons.MsgJobCommandNotTaken)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := apitest.JobFixture(t, apitest.Options{})
			w := f.Do("POST", "/api/jobs", tc.body, f.Session(apitest.AliceID))
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.command"); !ok || msg != tc.msg {
				t.Fatalf("got %q (found %v), want %q: %s", msg, ok, tc.msg, w.Body)
			}
			if n := len(f.Jobs()); n != 0 {
				t.Errorf("%d jobs created", n)
			}
		})
	}
}

// A bash job keeps the typed script whole (variables, pipes, several lines),
// since bash reads it, and shows it back unchanged.
func TestCreateBashJobKeepsTheScriptWhole(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{}, apitest.ShellTemplate())
	script := "echo \"Hello from $(hostname)\"\ndate | tee log.txt\necho $JAVO_PARAM_GREETING"
	w := f.Do("POST", "/api/jobs", `{"displayName":"Hello","template":"shell","engine":"bash","command":`+apitest.StrconvQuote(script)+`}`, f.Session(apitest.AliceID))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if defs := f.Jobs(); len(defs) != 1 || !slices.Equal(defs[0].Spec.Command, []string{script}) {
		t.Fatalf("stored %+v", defs)
	}
	if got := apitest.Decode[struct{ Engine, Command string }](t, w); got.Engine != "bash" || got.Command != script {
		t.Errorf("view %+v", got)
	}
}

func TestCreateChecksTheBashScript(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, script, msg string }{
		{"blank", " \n ", reasons.Msg(reasons.MsgJobCommandRequired)},
		{"too long", strings.Repeat("a", model.MaxCommandLength+1), reasons.Msg(reasons.MsgCommandTooLong)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := apitest.JobFixture(t, apitest.Options{}, apitest.ShellTemplate())
			w := f.Do("POST", "/api/jobs", `{"displayName":"A","template":"shell","engine":"bash","command":`+apitest.StrconvQuote(tc.script)+`}`, f.Session(apitest.AliceID))
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.command"); !ok || msg != tc.msg {
				t.Fatalf("got %q (found %v), want %q: %s", msg, ok, tc.msg, w.Body)
			}
		})
	}
}

// One click makes the template's sample job, run on demand; a second click
// makes another under the next free name, as app samples do.
func TestStartJobSampleMakesTheTemplatesCommandJob(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{}, apitest.SampleJobTemplate())
	w := f.Do("POST", "/api/job-samples/jobs-lab", "", f.Session(apitest.AliceID))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := apitest.Decode[struct {
		Name, DisplayName, Engine, Command, Template string
		Schedule                                     *struct{}
		Parameters                                   []struct{ Name, Default string }
	}](t, w)
	if got.Name != "print-the-time" || got.DisplayName != "Print the time" || got.Engine != "command" || got.Template != "jobs-lab" ||
		got.Command != "bash /opt/javo/samples/jobs/print-time.sh" || got.Schedule != nil ||
		len(got.Parameters) != 1 || got.Parameters[0].Name != "greeting" || got.Parameters[0].Default != "hello" {
		t.Fatalf("view %+v", got)
	}
	defs := f.Jobs()
	if len(defs) != 1 || defs[0].Owner != apitest.AliceID || !slices.Equal(defs[0].Spec.Command, []string{"bash", "/opt/javo/samples/jobs/print-time.sh"}) {
		t.Fatalf("stored %+v", defs)
	}
	w = f.Do("POST", "/api/job-samples/jobs-lab", "", f.Session(apitest.AliceID))
	if w.Code != http.StatusCreated || apitest.Decode[struct{ DisplayName string }](t, w).DisplayName != "Print the time 2" {
		t.Fatalf("second sample: status %d: %s", w.Code, w.Body)
	}
}

// The sample of a template that runs only bash scripts is a bash job: its
// command is the script, kept whole.
func TestStartJobSampleOfABashTemplateMakesABashJob(t *testing.T) {
	t.Parallel()
	tpl := apitest.ShellTemplate()
	script := `echo "Hello, ${JAVO_PARAM_NAME:-world}"`
	tpl.Spec.Jobs.Sample = &model.TemplateJobSample{Title: "Say hello", Command: script}
	f := apitest.JobFixture(t, apitest.Options{}, tpl)
	w := f.Do("POST", "/api/job-samples/shell", "", f.Session(apitest.AliceID))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := apitest.Decode[struct{ Engine, Command string }](t, w); got.Engine != "bash" || got.Command != script {
		t.Fatalf("view %+v", got)
	}
	if defs := f.Jobs(); len(defs) != 1 || !slices.Equal(defs[0].Spec.Command, []string{script}) {
		t.Fatalf("stored %+v", defs)
	}
}

func TestStartJobSampleOfATemplateWithoutOneIsRefused(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{})
	w := f.Do("POST", "/api/job-samples/lab", "", f.Session(apitest.AliceID))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), reasons.Msg(reasons.MsgNoSample, "lab")) {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if n := len(f.Jobs()); n != 0 {
		t.Errorf("%d jobs created", n)
	}
}

func TestTemplatesShowTheJobSample(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{}, apitest.SampleJobTemplate())
	w := f.Do("GET", "/api/templates", "", f.Session(apitest.AliceID))
	if !strings.Contains(w.Body.String(), `"sample":{"title":"Print the time","command":"bash /opt/javo/samples/jobs/print-time.sh","parameters":[{"name":"greeting","type":"string","default":"hello"}]}`) {
		t.Fatalf("templates: %s", w.Body)
	}
}

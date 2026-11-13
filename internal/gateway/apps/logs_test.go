package apps_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/executor/kube"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/podspec"
)

// After a mode change the home's files are on another claim; its events are
// the ones shown, not those of the claim kept for the grace period.
func TestEventsAreThoseOfTheHomesCurrentClaim(t *testing.T) {
	t.Parallel()
	owner := "alice-aaaaaaaa"
	old, live := naming.HomeClaimName(owner), naming.HomeClaimNameGen(owner, 2)
	home := &model.Home{Namespace: gatewaytest.Namespace, Spec: model.HomeSpec{Owner: owner}, Status: model.HomeStatus{ClaimName: live}}
	objs := []any{gatewaytest.Workspace("app", owner, "App"), gatewaytest.Template("streamlit", "streamlit"), home,
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: old, Namespace: gatewaytest.Namespace, UID: "uid-old"}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: live, Namespace: gatewaytest.Namespace, UID: "uid-live"}},
		apitest.KubeEvent("e0", "PersistentVolumeClaim", old, "uid-old", 0, "Normal", "ProvisioningSucceeded", "the old volume"),
		apitest.KubeEvent("e1", "PersistentVolumeClaim", live, "uid-live", 1, "Normal", "ProvisioningSucceeded", "the new volume"),
	}
	f := apitest.New(t, apitest.Options{}, objs...)
	w := f.Do("GET", "/api/workspaces/app/events", "", f.Session(owner))
	got := apitest.Decode[struct{ Items []apitest.EventJSON }](t, w).Items
	if w.Code != http.StatusOK || len(got) != 1 || got[0].Message != "the new volume" {
		t.Fatalf("status %d, events %+v", w.Code, got)
	}
}

func TestEventsAreTheLivePodsAndHomeClaimsMappedToSteps(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("app", "alice-aaaaaaaa", "App")
	pod := gatewaytest.LivePod("alice-aaaaaaaa", "app", "uid-live")
	claim := naming.HomeClaimName("alice-aaaaaaaa")
	objs := []any{ws, gatewaytest.Template("streamlit", "streamlit"), pod,
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: claim, Namespace: gatewaytest.Namespace, UID: "uid-claim"}},
		apitest.KubeEvent("e6", "Pod", pod.Name, "uid-live", 6, "Normal", "Started", "Started container app"),
		apitest.KubeEvent("e1", "Pod", pod.Name, "uid-live", 1, "Normal", "Scheduled", "Successfully assigned javo-apps/x to node-1"),
		apitest.KubeEvent("e0", "PersistentVolumeClaim", claim, "uid-claim", 0, "Normal", "ProvisioningSucceeded", "Successfully provisioned volume"),
		apitest.KubeEvent("e3", "Pod", pod.Name, "uid-live", 3, "Normal", "Pulling", "Pulling image \"example.org/streamlit:1\""),
		apitest.KubeEvent("e7", "Pod", pod.Name, "uid-live", 7, "Warning", "Unhealthy", "Readiness probe failed: connection refused"),
		// A deleted pod of the same name: its events must not show.
		apitest.KubeEvent("old", "Pod", pod.Name, "uid-old", 2, "Warning", "FailedScheduling", "0/1 nodes are available"),
	}
	f := apitest.New(t, apitest.Options{}, objs...)

	w := f.Do("GET", "/api/workspaces/app/events", "", f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := apitest.Decode[struct{ Items []apitest.EventJSON }](t, w).Items
	want := []struct{ step, reason string }{
		{model.ConditionHomeReady, "ProvisioningSucceeded"},
		{model.ConditionScheduled, "Scheduled"},
		{model.ConditionImageReady, "Pulling"},
		{model.ConditionStarted, "Started"},
		{model.ConditionReady, "Unhealthy"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(got), len(want), got)
	}
	for i, wnt := range want {
		if got[i].Step != wnt.step || got[i].Reason != wnt.reason {
			t.Errorf("event %d: got %s/%s, want %s/%s", i, got[i].Step, got[i].Reason, wnt.step, wnt.reason)
		}
	}
	if e := got[4]; e.Type != "Warning" || e.Message != "Readiness probe failed: connection refused" || e.Count != 1 || !e.Time.Equal(apitest.T0.Add(7*time.Second)) {
		t.Errorf("last event %+v", e)
	}
}

func TestLogsAndEventsAreOwnerScoped(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Workspace("app", "alice-aaaaaaaa", "App"), gatewaytest.Template("streamlit", "streamlit"),
		gatewaytest.LivePod("alice-aaaaaaaa", "app", "uid-live"))
	bob := f.Session("bob-bbbbbbbb")
	for _, p := range []string{"/api/workspaces/app/events", "/api/workspaces/app/logs", "/api/workspaces/app/logs/stream"} {
		w := f.Do("GET", p, "", bob)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404: %s", p, w.Code, w.Body)
		}
	}
}

func TestEventsInTheSameSecondKeepStartupOrder(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("app", "alice-aaaaaaaa", "App")
	pod := gatewaytest.LivePod("alice-aaaaaaaa", "app", "uid-live")
	// The scheduler records a precise event time; the kubelet whole seconds.
	sched := apitest.KubeEvent("a", "Pod", pod.Name, "uid-live", 0, "Normal", "Scheduled", "Successfully assigned")
	sched.LastTimestamp = metav1.Time{}
	sched.EventTime = metav1.NewMicroTime(apitest.T0.Add(400 * time.Millisecond))
	pulled := apitest.KubeEvent("b", "Pod", pod.Name, "uid-live", 0, "Normal", "Pulled", "Container image already present")
	f := apitest.New(t, apitest.Options{}, ws, gatewaytest.Template("streamlit", "streamlit"), pod, pulled, sched)
	got := apitest.Decode[struct{ Items []apitest.EventJSON }](t, f.Do("GET", "/api/workspaces/app/events", "", f.Session("alice-aaaaaaaa"))).Items
	if len(got) != 2 || got[0].Reason != "Scheduled" || got[1].Reason != "Pulled" {
		t.Fatalf("order %+v", got)
	}
}

func TestLogsReturnsTheAppContainersRecentLinesAsText(t *testing.T) {
	t.Parallel()
	cs := apitest.FakeLogs("2026-10-30T12:00:01.5Z  You can now view your Streamlit app\n2026-10-30T12:00:02Z <script>alert(1)</script>\n")
	f := apitest.New(t, apitest.Options{Logs: kube.ClientsetLogs{Clientset: cs}}, gatewaytest.Workspace("app", "alice-aaaaaaaa", "App"),
		gatewaytest.Template("streamlit", "streamlit"), apitest.Running(gatewaytest.LivePod("alice-aaaaaaaa", "app", "uid-live"), 2))
	alice := f.Session("alice-aaaaaaaa")

	w := f.Do("GET", "/api/workspaces/app/logs", "", alice)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := apitest.Decode[apitest.LogsJSON](t, w)
	if len(got.Lines) != 2 || got.Lines[0].Text != " You can now view your Streamlit app" || got.Lines[1].Text != "<script>alert(1)</script>" {
		t.Fatalf("lines %+v", got.Lines)
	}
	if got.Lines[0].Time == nil || !got.Lines[0].Time.Equal(apitest.T0.Add(1500*time.Millisecond)) {
		t.Errorf("time %v", got.Lines[0].Time)
	}
	if got.Restarts != 2 {
		t.Errorf("restarts %d", got.Restarts)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content type %q", ct)
	}

	if w := f.Do("GET", "/api/workspaces/app/logs?tail=50&previous=true", "", alice); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	opts := apitest.LogOpts(cs)
	if len(opts) != 2 {
		t.Fatalf("%d log requests", len(opts))
	}
	def, prev := opts[0], opts[1]
	if def.Container != podspec.ContainerName || !def.Timestamps || def.Previous || def.Follow ||
		def.TailLines == nil || *def.TailLines != 200 || def.LimitBytes == nil || *def.LimitBytes <= 0 {
		t.Errorf("default options %+v", def)
	}
	if !prev.Previous || *prev.TailLines != 50 {
		t.Errorf("previous options %+v", prev)
	}
}

func TestLogsTailIsBounded(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Logs: kube.ClientsetLogs{Clientset: apitest.FakeLogs("")}}, gatewaytest.Workspace("app", "alice-aaaaaaaa", "App"),
		gatewaytest.Template("streamlit", "streamlit"), apitest.Running(gatewaytest.LivePod("alice-aaaaaaaa", "app", "uid-live"), 0))
	for _, q := range []string{"tail=0", "tail=100000"} {
		if w := f.Do("GET", "/api/workspaces/app/logs?"+q, "", f.Session("alice-aaaaaaaa")); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d", q, w.Code)
		}
	}
}

func TestLogsAreEmptyBeforeTheContainerStartsOrWithoutAPreviousRun(t *testing.T) {
	t.Parallel()
	cs := apitest.FakeLogs("2026-10-30T12:00:01Z never read\n")
	f := apitest.New(t, apitest.Options{Logs: kube.ClientsetLogs{Clientset: cs}}, gatewaytest.Workspace("app", "alice-aaaaaaaa", "App"),
		gatewaytest.Template("streamlit", "streamlit"), gatewaytest.LivePod("alice-aaaaaaaa", "app", "uid-live"),
		gatewaytest.Workspace("other", "alice-aaaaaaaa", "Other"), apitest.Running(gatewaytest.LivePod("alice-aaaaaaaa", "other", "uid-other"), 0))
	alice := f.Session("alice-aaaaaaaa")
	for _, p := range []string{"/api/workspaces/app/logs", "/api/workspaces/other/logs?previous=true"} {
		w := f.Do("GET", p, "", alice)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", p, w.Code, w.Body)
		}
		if got := apitest.Decode[apitest.LogsJSON](t, w); len(got.Lines) != 0 {
			t.Errorf("%s: lines %+v", p, got.Lines)
		}
	}
	if n := len(apitest.LogOpts(cs)); n != 0 {
		t.Errorf("%d log requests, want none", n)
	}
}

func TestLogsOfACrashLoopingContainerAreItsLastRun(t *testing.T) {
	t.Parallel()
	cs := apitest.FakeLogs("2026-10-30T12:00:01Z ModuleNotFoundError: No module named 'plotly'\n")
	pod := apitest.Running(gatewaytest.LivePod("alice-aaaaaaaa", "app", "uid-live"), 3)
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
	f := apitest.New(t, apitest.Options{Logs: kube.ClientsetLogs{Clientset: cs}}, gatewaytest.Workspace("app", "alice-aaaaaaaa", "App"),
		gatewaytest.Template("streamlit", "streamlit"), pod)
	w := f.Do("GET", "/api/workspaces/app/logs", "", f.Session("alice-aaaaaaaa"))
	if got := apitest.Decode[apitest.LogsJSON](t, w); len(got.Lines) != 1 || got.Restarts != 3 {
		t.Fatalf("status %d, logs %+v", w.Code, got)
	}
}

// A site app's Build step comes between the image and the start, and only on
// an app that has one.
func TestBuildStepIsShownOnlyOnAnAppThatBuilds(t *testing.T) {
	t.Parallel()
	ws, _ := apitest.SiteApp()
	f := apitest.New(t, apitest.Options{}, ws, gatewaytest.Workspace("plots", "alice-aaaaaaaa", "Plots"), gatewaytest.Template("static-site", "static"), gatewaytest.Template("streamlit", "streamlit"))
	alice := f.Session("alice-aaaaaaaa")
	for name, want := range map[string]string{
		"site":  "Scheduled,HomeReady,ImageReady,Built,Started,Ready",
		"plots": "Scheduled,HomeReady,ImageReady,Started,Ready",
	} {
		got := apitest.Decode[apitest.WorkspaceJSON](t, f.Do("GET", "/api/workspaces/"+name, "", alice))
		var types []string
		for _, s := range got.Steps {
			types = append(types, s.Type)
		}
		if strings.Join(types, ",") != want {
			t.Errorf("%s steps %v, want %s", name, types, want)
		}
	}
}

// The events of the steps that fetch and build the code belong to the Build
// step of an app that builds, and to the start of one that does not (where
// a failed clone shows).
func TestSiteStepEventsBelongToTheBuildStep(t *testing.T) {
	t.Parallel()
	ws, pod := apitest.SiteApp()
	plain := gatewaytest.Workspace("plots", "alice-aaaaaaaa", "Plots")
	plainPod := gatewaytest.LivePod("alice-aaaaaaaa", "plots", "uid-plots")
	build := apitest.KubeEvent("b1", "Pod", pod.Name, "uid-site", 1, "Normal", "Started", "Started container build")
	build.InvolvedObject.FieldPath = "spec.initContainers{build}"
	clone := apitest.KubeEvent("p1", "Pod", plainPod.Name, "uid-plots", 1, "Normal", "Started", "Started container clone")
	clone.InvolvedObject.FieldPath = "spec.initContainers{clone}"
	f := apitest.New(t, apitest.Options{}, ws, plain, gatewaytest.Template("static-site", "static"), gatewaytest.Template("streamlit", "streamlit"), pod, plainPod, build, clone)
	alice := f.Session("alice-aaaaaaaa")
	for app, want := range map[string]string{"site": model.ConditionBuilt, "plots": model.ConditionStarted} {
		got := apitest.Decode[struct{ Items []apitest.EventJSON }](t, f.Do("GET", "/api/workspaces/"+app+"/events", "", alice)).Items
		if len(got) != 1 || got[0].Step != want {
			t.Errorf("%s events %+v, want step %s", app, got, want)
		}
	}
}

// The Build step's log is the build container's: the full output of a
// failed build, with its own restarts.
func TestBuildStepLogIsTheBuildContainers(t *testing.T) {
	t.Parallel()
	cs := apitest.FakeLogs("2026-10-30T12:00:01Z npm error code EUSAGE\n2026-10-30T12:00:01Z npm error The `npm ci` command can only install with an existing package-lock.json\n")
	ws, pod := apitest.SiteApp()
	f := apitest.New(t, apitest.Options{Logs: kube.ClientsetLogs{Clientset: cs}}, ws, gatewaytest.Template("static-site", "static"), pod)
	w := f.Do("GET", "/api/workspaces/site/logs?step=Built&tail=1000", "", f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := apitest.Decode[apitest.LogsJSON](t, w)
	if len(got.Lines) != 2 || got.Lines[0].Text != "npm error code EUSAGE" || got.Restarts != 2 {
		t.Fatalf("logs %+v", got)
	}
	if opts := apitest.LogOpts(cs); len(opts) != 1 || opts[0].Container != "build" || *opts[0].TailLines != 1000 {
		t.Fatalf("log requests %+v", opts)
	}
}

func TestLogStreamSendsLinesAndStopsWhenTheClientLeaves(t *testing.T) {
	t.Parallel()
	logs := apitest.NewPipeLogs()
	f := apitest.New(t, apitest.Options{Logs: logs}, gatewaytest.Workspace("app", "alice-aaaaaaaa", "App"),
		gatewaytest.Template("streamlit", "streamlit"), apitest.Running(gatewaytest.LivePod("alice-aaaaaaaa", "app", "uid-live"), 0))
	srv := httptest.NewServer(f.Srv)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/workspaces/app/logs/stream", nil)
	req.AddCookie(f.Session("alice-aaaaaaaa"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d, type %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	opts := <-logs.Opts
	if !opts.Follow || !opts.Timestamps || opts.Container != podspec.ContainerName {
		t.Errorf("options %+v", opts)
	}

	io.WriteString(logs.W, "2026-10-30T12:00:01Z first\n2026-10-30T12:00:02Z <b>second</b>\n")
	sc := bufio.NewScanner(res.Body)
	var data []string
	for len(data) < 2 && sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			data = append(data, d)
		}
	}
	for i, want := range []string{"first", "<b>second</b>"} {
		var line struct{ Text string }
		if i >= len(data) || json.Unmarshal([]byte(data[i]), &line) != nil || line.Text != want {
			t.Fatalf("data %q, want line %d %q", data, i, want)
		}
	}

	cancel()
	select {
	case <-logs.Closed:
	case <-time.After(5 * time.Second):
		t.Fatal("log stream still open after the client left")
	}
}

// The home step stalls on the Job that readies the home; its events and its
// pod's are the home step's, the kubelet's warnings first.
func TestTheHomeStepShowsThePrepareJobsEventsWarningsFirst(t *testing.T) {
	t.Parallel()
	owner := "alice-aaaaaaaa"
	claim := naming.HomeClaimName(owner)
	prep := apitest.HomePreparePod(owner, "javo-prepare-home-alice-x1", "uid-prep")
	other := apitest.HomePreparePod("bob-bbbbbbbb", "javo-prepare-home-bob-x1", "uid-bob")
	objs := []any{gatewaytest.Workspace("app", owner, "App"), gatewaytest.Template("streamlit", "streamlit"), prep, other,
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: claim, Namespace: gatewaytest.Namespace, UID: "uid-claim"}},
		apitest.KubeEvent("c0", "PersistentVolumeClaim", claim, "uid-claim", 0, "Normal", "ProvisioningSucceeded", "Successfully provisioned volume"),
		apitest.KubeEvent("j0", "Job", "javo-prepare-home-"+owner, "job-uid-prep", 1, "Normal", "SuccessfulCreate", "Created pod: javo-prepare-home-alice-x1"),
		apitest.KubeEvent("p0", "Pod", prep.Name, "uid-prep", 2, "Normal", "Scheduled", "Successfully assigned javo-apps/x to node-1"),
		apitest.KubeEvent("p1", "Pod", prep.Name, "uid-prep", 3, "Warning", "FailedMount", "MountVolume.MountDevice failed: bad option"),
		apitest.KubeEvent("b0", "Pod", other.Name, "uid-bob", 4, "Warning", "FailedMount", "bob's volume"),
	}
	f := apitest.New(t, apitest.Options{}, objs...)
	w := f.Do("GET", "/api/workspaces/app/events", "", f.Session(owner))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var got []string
	for _, e := range apitest.Decode[struct{ Items []apitest.EventJSON }](t, w).Items {
		got = append(got, e.Step+"/"+e.Reason)
	}
	want := []string{"HomeReady/FailedMount", "HomeReady/ProvisioningSucceeded", "HomeReady/SuccessfulCreate", "HomeReady/Scheduled"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("events %v, want %v", got, want)
	}
}

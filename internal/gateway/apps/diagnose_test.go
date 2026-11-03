package apps_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestDiagnosisFindsTheCauseInTheLog(t *testing.T) {
	t.Parallel()
	pod := apitest.Running(gatewaytest.LivePod("alice-aaaaaaaa", "sales", "uid-live"), 3)
	pod.Spec.Containers = []corev1.Container{{Name: "app", Env: []corev1.EnvVar{{Name: "DB_PASSWORD", Value: "s3cret-env-value"}}}}
	logs := &apitest.RunLogs{
		Prev: "2026-10-30T12:00:01Z Traceback (most recent call last):\n2026-10-30T12:00:02Z ModuleNotFoundError: No module named 'plotly'\n",
		Cur:  "2026-10-30T12:01:00Z starting\n",
	}
	f := apitest.New(t, apitest.Options{Logs: logs}, apitest.CrashedApp(), gatewaytest.Template("streamlit", "streamlit"), pod,
		gatewaytest.LivePod("bob-bbbbbbbb", "other", "uid-other"))

	w := f.Do("GET", "/api/workspaces/sales/diagnosis", "", f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	d := apitest.Decode[apitest.DiagnosisJSON](t, w)
	r, _ := reasons.Lookup(reasons.MissingModule)
	if d.Code != string(reasons.MissingModule) || !d.Known || d.Title != r.Title || d.What != r.What {
		t.Fatalf("diagnosis %+v", d)
	}
	refs := map[string]string{}
	for _, e := range d.Bundle {
		refs[e.Ref] = e.Excerpt
	}
	if !strings.Contains(refs["log/app/prev/2"], "plotly") {
		t.Errorf("bundle lacks the previous run's line: %v", refs)
	}
	if refs["log/app/cur/1"] != "starting" {
		t.Errorf("bundle lacks the current run: %v", refs)
	}
	if len(d.Proposals) != 1 || d.Proposals[0].Action != "restart" || d.Proposals[0].Target != "sales" || d.Proposals[0].Summary == "" {
		t.Errorf("proposals %+v", d.Proposals)
	}
	if strings.Contains(w.Body.String(), "s3cret-env-value") {
		t.Error("an environment value reached the diagnosis")
	}
	for _, c := range logs.Calls {
		if c.Pod != pod.Name || c.Opts.Container != "app" || c.Opts.TailLines == nil || *c.Opts.TailLines != 200 {
			t.Errorf("log read %s %+v", c.Pod, c.Opts)
		}
	}
	if len(logs.Calls) != 2 {
		t.Errorf("%d log reads, want the current and the previous run", len(logs.Calls))
	}
}

func TestDiagnosisReadsThePreviousRunOnlyAfterARestart(t *testing.T) {
	t.Parallel()
	logs := &apitest.RunLogs{Cur: "2026-10-30T12:01:00Z hello\n"}
	f := apitest.New(t, apitest.Options{Logs: logs}, apitest.CrashedApp(), gatewaytest.Template("streamlit", "streamlit"),
		apitest.Running(gatewaytest.LivePod("alice-aaaaaaaa", "sales", "uid-live"), 0))
	if w := f.Do("GET", "/api/workspaces/sales/diagnosis", "", f.Session("alice-aaaaaaaa")); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	for _, c := range logs.Calls {
		if c.Opts.Previous {
			t.Fatal("read the previous run of a container that never restarted")
		}
	}
}

func TestDiagnosisWithoutAPodStillAnswers(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Logs: &apitest.RunLogs{}}, apitest.CrashedApp(), gatewaytest.Template("streamlit", "streamlit"))
	w := f.Do("GET", "/api/workspaces/sales/diagnosis", "", f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if d := apitest.Decode[apitest.DiagnosisJSON](t, w); d.Code != string(reasons.AppCrashed) || d.Known {
		t.Fatalf("diagnosis %+v", d)
	}
}

func TestDiagnosisOfARunningAppHasNoProposals(t *testing.T) {
	t.Parallel()
	app := gatewaytest.Workspace("sales", "alice-aaaaaaaa", "Sales")
	app.Status.Phase = "Running"
	f := apitest.New(t, apitest.Options{Logs: &apitest.RunLogs{}}, app, gatewaytest.Template("streamlit", "streamlit"))
	d := apitest.Decode[apitest.DiagnosisJSON](t, f.Do("GET", "/api/workspaces/sales/diagnosis", "", f.Session("alice-aaaaaaaa")))
	if d.Code != string(reasons.Ready) || !d.Known || len(d.Proposals) != 0 {
		t.Fatalf("diagnosis %+v", d)
	}
}

func TestDiagnosisOfAnotherPersonsAppIsNotFound(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Logs: &apitest.RunLogs{}}, apitest.CrashedApp(), gatewaytest.Template("streamlit", "streamlit"))
	w := f.Do("GET", "/api/workspaces/sales/diagnosis", "", f.Session("bob-bbbbbbbb"))
	if w.Code != http.StatusNotFound || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgAppNotFound) {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestRestartAsksTheControllerAndDoesNotTouchThePod(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.CrashedApp(), gatewaytest.Template("streamlit", "streamlit"),
		apitest.Running(gatewaytest.LivePod("alice-aaaaaaaa", "sales", "uid-live"), 1))
	before := f.GetWorkspace("alice-aaaaaaaa", "sales")
	w := f.Do("POST", "/api/workspaces/sales/restart", "", f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	after := f.GetWorkspace("alice-aaaaaaaa", "sales")
	if !after.RestartRequestedAt.After(before.RestartRequestedAt) {
		t.Fatalf("restart not requested: %v", after.RestartRequestedAt)
	}
	var p corev1.Pod
	if err := f.Client.Get(context.Background(), client.ObjectKey{Namespace: gatewaytest.Namespace, Name: before.ObjectName()}, &p); err != nil {
		t.Fatalf("the gateway removed the pod: %v", err)
	}
	if w := f.Do("POST", "/api/workspaces/sales/restart", "", f.Session("bob-bbbbbbbb")); w.Code != http.StatusNotFound {
		t.Fatalf("bob restarting alice's app: %d", w.Code)
	}
}

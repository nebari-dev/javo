//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	execkube "github.com/nebari-dev/javo/internal/executor/kube"
)

// TestSampleJob makes the sample job a template ships, the way its card on
// the Jobs page does, runs it with a parameter and reads the run's log: the
// script in the image prints the time, the folder it runs in (the home) and
// the parameter it got as JAVO_PARAM_GREETING.
func TestSampleJob(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	ensureHomeClaim(t, kube(t), me.ID)

	var templates struct {
		Items []struct {
			Name string
			Jobs *struct {
				Sample *struct{ Title, Command string }
			}
		}
	}
	s.json(http.MethodGet, "/api/templates", nil, http.StatusOK, &templates)
	tpl := ""
	for _, x := range templates.Items {
		if x.Jobs != nil && x.Jobs.Sample != nil {
			tpl = x.Name
			break
		}
	}
	if tpl != "jobs-python" {
		t.Fatalf("the sample job comes from template %q, want jobs-python", tpl)
	}

	var job struct{ Name, Command string }
	s.json(http.MethodPost, "/api/job-samples/"+tpl, nil, http.StatusCreated, &job)
	t.Cleanup(func() { s.do(http.MethodDelete, "/api/jobs/"+job.Name, nil) })
	t.Logf("sample job %s runs %q", job.Name, job.Command)

	// The first run waits (503) until the owner has a uid.
	var run struct{ ID string }
	body := map[string]any{"parameters": map[string]string{"greeting": "hello from e2e"}}
	for deadline := time.Now().Add(time.Minute); ; {
		resp, data := s.do(http.MethodPost, "/api/jobs/"+job.Name+"/run", body)
		if resp.StatusCode == http.StatusServiceUnavailable && time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
			continue
		}
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("run: %d %s", resp.StatusCode, data)
		}
		if err := json.Unmarshal(data, &run); err != nil || run.ID == "" {
			t.Fatalf("run: %v: %s", err, data)
		}
		break
	}

	var outcome, folder string
	for deadline := time.Now().Add(5 * time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		var d struct {
			Runs []struct{ ID, Outcome, Reason, Message, OutputPath string }
		}
		s.json(http.MethodGet, "/api/jobs/"+job.Name, nil, http.StatusOK, &d)
		for _, r := range d.Runs {
			if r.ID != run.ID {
				continue
			}
			folder = r.OutputPath
			if r.Outcome != outcome {
				t.Logf("run %s: %s %s %s", r.ID, r.Outcome, r.Reason, r.Message)
				outcome = r.Outcome
			}
		}
		if outcome != "" && outcome != "Running" {
			break
		}
	}
	if outcome != "Succeeded" {
		t.Fatalf("run %s ended %q, want Succeeded", run.ID, outcome)
	}

	timeLine := regexp.MustCompile(`(?m)^The time is \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} `)
	resp, log := s.do(http.MethodGet, "/api/jobs/"+job.Name+"/runs/"+run.ID+"/log", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("log: %d %s", resp.StatusCode, log)
	}
	t.Logf("run log (%s):\n%s", resp.Header.Get("X-Log-Source"), log)
	if !timeLine.Match(log) {
		t.Errorf("the log has no time line")
	}
	for _, want := range []string{"Folder: /home/user\n", "Greeting: hello from e2e\n"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("the log lacks %q", want)
		}
	}

	// Once the run's pod is gone, the log comes from run.log in the run folder,
	// jobs/<job>/<the run's Kubernetes Job name>.
	c := kube(t)
	ctx := context.Background()
	k8sJob := path.Base(folder)
	if folder == "" {
		t.Fatal("the run names no folder")
	}
	runPods := []client.ListOption{client.InNamespace(appsNS), client.MatchingLabels{execkube.LabelJobName: k8sJob}}
	if err := c.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(appsNS), client.MatchingLabels{execkube.LabelJobName: k8sJob}); err != nil {
		t.Fatalf("delete the run's pods: %v", err)
	}
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(time.Second) {
		var pods corev1.PodList
		if err := c.List(ctx, &pods, runPods...); err != nil {
			t.Fatal(err)
		}
		if len(pods.Items) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run's pods are still there: %d", len(pods.Items))
		}
	}
	// Reading the home folder may first answer 503 while its reader starts
	// (Retry-After), as the page does when it retries by itself.
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(2 * time.Second) {
		resp, log = s.do(http.MethodGet, "/api/jobs/"+job.Name+"/runs/"+run.ID+"/log", nil)
		if resp.StatusCode != http.StatusServiceUnavailable || time.Now().After(deadline) {
			break
		}
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Log-Source") != "saved" {
		t.Fatalf("log after the pod is gone: %d %q %s", resp.StatusCode, resp.Header.Get("X-Log-Source"), log)
	}
	if !timeLine.Match(log) {
		t.Errorf("the saved log has no time line:\n%s", log)
	}
}

// TestCustomCommandJob runs a typed script with the Custom command job
// template: bash reads it (a command substitution, a pipe and a parameter
// variable work), in the home folder, and the run log shows its output.
func TestCustomCommandJob(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	ensureHomeClaim(t, kube(t), me.ID)

	script := "echo \"Hello from $(hostname)\"\necho \"greeting=$JAVO_PARAM_GREETING\" | tr a-z A-Z\npwd"
	var job struct{ Name, Command string }
	s.json(http.MethodPost, "/api/jobs", map[string]any{
		"displayName": "custom command " + time.Now().Format("150405"), "template": "jobs-command", "engine": "bash",
		"command": script, "parameters": []map[string]string{{"name": "greeting", "type": "string", "default": "hi"}},
	}, http.StatusCreated, &job)
	t.Cleanup(func() { s.do(http.MethodDelete, "/api/jobs/"+job.Name, nil) })
	if job.Command != script {
		t.Errorf("the job shows %q, want the script as typed", job.Command)
	}

	run, _ := runToEnd(t, s, job.Name)
	resp, log := s.do(http.MethodGet, "/api/jobs/"+job.Name+"/runs/"+run+"/log", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("log: %d %s", resp.StatusCode, log)
	}
	t.Logf("run log:\n%s", log)
	if !regexp.MustCompile(`(?m)^Hello from \S+$`).Match(log) {
		t.Errorf("the log has no hello line")
	}
	for _, want := range []string{"GREETING=HI\n", "/home/user\n"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("the log lacks %q", want)
		}
	}
}

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/controller/jobs"
	"github.com/nebari-dev/javo/internal/model"
)

// runToEnd starts a run of the job and waits until it ends, returning its id
// and the name of its Kubernetes Job.
func runToEnd(t *testing.T, s *session, job string) (id, k8sJob string) {
	t.Helper()
	var run struct{ ID string }
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(2 * time.Second) {
		resp, data := s.do(http.MethodPost, "/api/jobs/"+job+"/run", nil)
		// The first run waits (503) until the owner has a uid.
		if resp.StatusCode == http.StatusServiceUnavailable && time.Now().Before(deadline) {
			continue
		}
		if resp.StatusCode != http.StatusCreated || json.Unmarshal(data, &run) != nil || run.ID == "" {
			t.Fatalf("run: %d %s", resp.StatusCode, data)
		}
		break
	}
	for deadline := time.Now().Add(5 * time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		var d struct {
			Runs []struct{ ID, Outcome, Reason, Message, OutputPath string }
		}
		s.json(http.MethodGet, "/api/jobs/"+job, nil, http.StatusOK, &d)
		for _, r := range d.Runs {
			if r.ID == run.ID && r.Outcome != "" && r.Outcome != "Running" {
				if r.Outcome != "Succeeded" {
					t.Fatalf("run %s ended %s: %s %s", r.ID, r.Outcome, r.Reason, r.Message)
				}
				return r.ID, r.OutputPath[strings.LastIndex(r.OutputPath, "/")+1:]
			}
		}
	}
	t.Fatalf("run %s did not end", run.ID)
	return "", ""
}

// A secret stored under Settings reaches each run of a job as an environment
// variable, read from a Secret the run's Job owns; no API answer, Job or pod
// spec holds the value, and a new value reaches the next run.
func TestJobGetsASecret(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	ensureHomeClaim(t, kube(t), me.ID)
	value := fmt.Sprintf("e2e-placeholder-%d", time.Now().UnixNano())
	resp, body := s.do(http.MethodPost, "/api/secrets", map[string]any{"name": "E2E_JOB_SECRET", "value": value})
	if resp.StatusCode != http.StatusCreated || strings.Contains(string(body), value) {
		t.Fatalf("create secret: %d %s", resp.StatusCode, body)
	}
	var job struct{ Name string }
	s.json(http.MethodPost, "/api/jobs", map[string]any{
		"displayName": fmt.Sprintf("secret job %d", time.Now().Unix()%100000), "template": "jobs-python", "engine": "command",
		"command": `python -c "import os; print('MY_SECRET=' + os.environ['MY_SECRET']); print('REGION=' + os.environ['REGION'])"`,
		"env":     []map[string]string{{"name": "MY_SECRET", "secret": "E2E_JOB_SECRET"}, {"name": "REGION", "value": "emea"}},
	}, http.StatusCreated, &job)
	t.Cleanup(func() { s.do(http.MethodDelete, "/api/jobs/"+job.Name, nil) })

	logOf := func(run string) string {
		resp, log := s.do(http.MethodGet, "/api/jobs/"+job.Name+"/runs/"+run+"/log", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("log: %d %s", resp.StatusCode, log)
		}
		return string(log)
	}
	run, k8sJob := runToEnd(t, s, job.Name)
	if log := logOf(run); !strings.Contains(log, "MY_SECRET="+value+"\n") || !strings.Contains(log, "REGION=emea\n") {
		t.Fatalf("the run log lacks the values:\n%s", log)
	}

	c := kube(t)
	ctx := context.Background()
	var k batchv1.Job
	if err := c.Get(ctx, client.ObjectKey{Namespace: appsNS, Name: k8sJob}, &k); err != nil {
		t.Fatal(err)
	}
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(appsNS), client.MatchingLabels{jobs.LabelJobName: k8sJob}); err != nil {
		t.Fatal(err)
	}
	for _, o := range []any{k, pods} {
		if raw, _ := json.Marshal(o); strings.Contains(string(raw), value) {
			t.Fatalf("the value is in a %T spec", o)
		}
	}
	var sec corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: appsNS, Name: jobs.RunSecretName(k8sJob)}, &sec); err != nil {
		t.Fatalf("the run's Secret: %v", err)
	}
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].UID != k.UID {
		t.Fatalf("the run's Secret is not owned by its Job: %+v", sec.ObjectMeta)
	}
	for _, path := range []string{"/api/secrets", "/api/jobs", "/api/jobs/" + job.Name} {
		if _, body := s.do(http.MethodGet, path, nil); strings.Contains(string(body), value) {
			t.Fatalf("GET %s holds the value", path)
		}
	}

	newValue := value + "-2"
	s.json(http.MethodPut, "/api/secrets/E2E_JOB_SECRET", map[string]any{"value": newValue}, http.StatusOK, nil)
	run2, _ := runToEnd(t, s, job.Name)
	if log := logOf(run2); !strings.Contains(log, "MY_SECRET="+newValue+"\n") {
		t.Fatalf("the next run did not get the new value:\n%s", log)
	}
	if resp, body := s.do(http.MethodDelete, "/api/secrets/E2E_JOB_SECRET", nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete a secret a job uses: %d %s", resp.StatusCode, body)
	}

	// Deleting the job takes the run's Secret with it.
	s.do(http.MethodDelete, "/api/jobs/"+job.Name, nil)
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(2 * time.Second) {
		err := c.Get(ctx, client.ObjectKeyFromObject(&sec), &corev1.Secret{})
		if client.IgnoreNotFound(err) == nil && err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run's Secret outlived the job")
		}
	}
}

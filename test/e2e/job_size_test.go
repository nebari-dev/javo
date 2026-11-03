//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"path"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestJobRunsAtItsSize: a job starts at the default size, Change size moves
// it to another, and the next run's container gets that size's CPU and
// memory as its requests and limits.
func TestJobRunsAtItsSize(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	ensureHomeClaim(t, kube(t), me.ID)

	var job struct{ Name, Size string }
	s.json(http.MethodPost, "/api/job-samples/jobs-python", nil, http.StatusCreated, &job)
	t.Cleanup(func() { s.do(http.MethodDelete, "/api/jobs/"+job.Name, nil) })
	if job.Size != "small" {
		t.Fatalf("a new job is at size %q, want the default small", job.Size)
	}
	s.json(http.MethodPut, "/api/jobs/"+job.Name+"/size", map[string]string{"size": "medium"}, http.StatusOK, &job)
	if job.Size != "medium" {
		t.Fatalf("after Change size the job is at %q, want medium", job.Size)
	}

	// The first run waits (503) until the owner has a uid.
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(2 * time.Second) {
		resp, _ := s.do(http.MethodPost, "/api/jobs/"+job.Name+"/run", nil)
		if resp.StatusCode == http.StatusServiceUnavailable && time.Now().Before(deadline) {
			continue
		}
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("run: %d", resp.StatusCode)
		}
		break
	}

	// The run folder is jobs/<job>/<the run's Kubernetes Job name>.
	var k8sJob string
	for deadline := time.Now().Add(2 * time.Minute); k8sJob == "" && time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		var d struct{ Runs []struct{ OutputPath string } }
		s.json(http.MethodGet, "/api/jobs/"+job.Name, nil, http.StatusOK, &d)
		if len(d.Runs) > 0 && d.Runs[0].OutputPath != "" {
			k8sJob = path.Base(d.Runs[0].OutputPath)
		}
	}
	if k8sJob == "" {
		t.Fatal("the run never started")
	}
	var kj batchv1.Job
	if err := kube(t).Get(context.Background(), client.ObjectKey{Namespace: appsNS, Name: k8sJob}, &kj); err != nil {
		t.Fatalf("read the run's Job %s: %v", k8sJob, err)
	}
	// medium's CPU and memory as this install defines them.
	var sizes sizeList
	s.json(http.MethodGet, "/api/sizes", nil, http.StatusOK, &sizes)
	var cpu, mem resource.Quantity
	for _, it := range sizes.Items {
		if it.Name == "medium" {
			cpu = *resource.NewMilliQuantity(int64(it.CPU*1000), resource.DecimalSI)
			mem = *resource.NewQuantity(it.Memory, resource.BinarySI)
		}
	}
	if cpu.IsZero() {
		t.Fatal("this install has no size medium")
	}
	r := kj.Spec.Template.Spec.Containers[0].Resources
	t.Logf("run %s: requests %v, limits %v", k8sJob, r.Requests, r.Limits)
	for name, l := range map[string]corev1.ResourceList{"requests": r.Requests, "limits": r.Limits} {
		if l.Cpu().Cmp(cpu) != 0 || l.Memory().Cmp(mem) != 0 {
			t.Errorf("the run's %s are %v, want medium's %s CPU and %s", name, l, cpu.String(), mem.String())
		}
	}
}

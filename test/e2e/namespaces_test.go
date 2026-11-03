//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
)

// helm runs helm against the release in gatewayNS.
func helm(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("helm", append(args, "--namespace", gatewayNS)...)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	t.Logf("helm %s:\n%s", strings.Join(args, " "), out)
	if err != nil {
		t.Fatalf("helm %s: %v", args[0], err)
	}
}

// TestNamespaceModes upgrades the release to per-user namespaces
// (deploy/dev/per-user.yaml) and back to shared at the end. It changes the
// deployment for every other test, so it runs only when
// JAVO_E2E_NAMESPACE_MODES=1 (make e2e runs it after the other tests).
func TestNamespaceModes(t *testing.T) {
	if os.Getenv("JAVO_E2E_NAMESPACE_MODES") != "1" {
		t.Skip("set JAVO_E2E_NAMESPACE_MODES=1; it reconfigures the deployment")
	}
	c := kube(t)
	helm(t, "upgrade", "javo", "deploy/helm/javo", "--reuse-values", "-f", "deploy/dev/per-user.yaml", "--wait", "--timeout", "3m")
	waitRelease(t, c)
	// Not helm rollback: the shared namespace is kept on uninstall
	// (resource-policy keep), and rollback refuses to restore a kept object.
	t.Cleanup(func() {
		helm(t, "upgrade", "javo", "deploy/helm/javo", "--reuse-values", "--set", "config.namespaces.mode=shared", "--wait", "--timeout", "3m")
		waitRelease(t, c)
	})
	s := newSession(t)
	s.login(localLogin, "alice")
	who := s.me()
	want := naming.NamespaceName(model.ProductName, who.ID)

	// The first app waits for the controller to make alice's namespace: the
	// API answers 503 NamespacePending until it exists.
	var a app
	deadline := time.Now().Add(time.Minute)
	for {
		resp, body := s.do(http.MethodPost, "/api/samples/streamlit", nil)
		if resp.StatusCode == http.StatusCreated {
			if err := json.Unmarshal(body, &a); err != nil {
				t.Fatal(err)
			}
			break
		}
		if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "NamespacePending") || time.Now().After(deadline) {
			t.Fatalf("POST /api/samples/streamlit: %d %s", resp.StatusCode, body)
		}
		time.Sleep(2 * time.Second)
	}
	t.Cleanup(func() { s.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil) })
	s.waitFor(a.Name, 5*time.Minute, func(a app) bool {
		if a.Phase == "Failed" {
			t.Fatalf("streamlit failed: %+v", a.Steps)
		}
		return a.Phase == "Running"
	})

	var pod corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: want, Name: naming.ObjectName(who.ID, a.Name)}, &pod); err != nil {
		t.Fatalf("alice's app pod in namespace %s: %v", want, err)
	}
	target := fmt.Sprintf("http://%s:8501/u/%s/%s/_stcore/health", pod.Status.PodIP, who.ID, a.Name)
	control := "http://javo-gateway." + gatewayNS + ".svc/healthz"
	out := probe(t, c, control, target)
	t.Logf("probe from namespace default:\n%s", out)
	if !strings.Contains(out, control+" ok") {
		t.Fatalf("control request to the gateway failed, the probe proves nothing:\n%s", out)
	}
	if strings.Contains(out, target+" ok") {
		t.Fatalf("an app pod in %s answered a pod in another namespace", want)
	}
}

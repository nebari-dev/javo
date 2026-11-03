//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// fakeToken is printed by the broken app; it is a fake GitHub token shape
// the secret scanner knows, and must never reach the model.
const fakeToken = "ghp_Zq8XbT3kLm9PvR2sW7yN4cJ6hF1dG5aE0uIo"

// startFakeModel runs testdata/fakemodel.py as a pod and service
// fake-model in gatewayNS and returns its OpenAI-style base URL.
func startFakeModel(t *testing.T, c client.Client) string {
	t.Helper()
	ctx := context.Background()
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "test", "e2e", "testdata", "fakemodel.py"))
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"app": "e2e-fake-model"}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "e2e-fake-model", Namespace: gatewayNS}, Data: map[string]string{"fakemodel.py": string(src)}}
	podSec, ctrSec := restrictedSecurity(1000)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-fake-model", Namespace: gatewayNS, Labels: labels},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: ptr.To(false), SecurityContext: podSec,
			Containers: []corev1.Container{{
				Name: "model", Image: helperImage, ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"python", "/fake/fakemodel.py"}, SecurityContext: ctrSec,
				Ports:          []corev1.ContainerPort{{ContainerPort: 8000}},
				VolumeMounts:   []corev1.VolumeMount{{Name: "src", MountPath: "/fake"}},
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(8000)}}},
			}},
			Volumes: []corev1.Volume{{Name: "src", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cm.Name}}}}},
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "fake-model", Namespace: gatewayNS},
		Spec:       corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{Port: 8000, TargetPort: intstr.FromInt32(8000)}}},
	}
	for _, o := range []client.Object{cm, pod, svc} {
		_ = c.Delete(ctx, o)
	}
	for _, o := range []client.Object{cm, pod, svc} {
		deadline := time.Now().Add(time.Minute)
		for err := c.Create(ctx, o); err != nil; err = c.Create(ctx, o) {
			if time.Now().After(deadline) {
				t.Fatalf("create %s: %v", o.GetName(), err)
			}
			time.Sleep(2 * time.Second)
		}
		t.Cleanup(func() { _ = c.Delete(context.Background(), o) })
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		var p corev1.Pod
		if err := c.Get(ctx, client.ObjectKeyFromObject(pod), &p); err != nil {
			t.Fatal(err)
		}
		ready := false
		for _, cond := range p.Status.Conditions {
			ready = ready || cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake model not ready: %+v", p.Status)
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Sprintf("http://fake-model.%s.svc:8000/v1", gatewayNS)
}

// With AI on against a fake OpenAI-compatible server: the self-test passes,
// Explain more cites only what was sent, a fake token the app printed is
// hidden before it is sent, and Describe it fills a form without creating an
// app. It reconfigures the release (and turns AI off again at the end), so
// it runs only with JAVO_E2E_AI=1 (make e2e-ai).
func TestAIWithAFakeModel(t *testing.T) {
	if os.Getenv("JAVO_E2E_AI") != "1" {
		t.Skip("set JAVO_E2E_AI=1; it reconfigures the deployment")
	}
	c := kube(t)
	base := startFakeModel(t, c)
	helm(t, "upgrade", "javo", "deploy/helm/javo", "--reuse-values", "--set", "config.ai.enabled=true", "--set", "config.ai.provider=openai",
		"--set", "config.ai.model=fake-model", "--set", "config.ai.baseURL="+base, "--wait", "--timeout", "3m")
	waitRelease(t, c)
	t.Cleanup(func() {
		helm(t, "upgrade", "javo", "deploy/helm/javo", "--reuse-values", "--set", "config.ai.enabled=false", "--wait", "--timeout", "3m")
		waitRelease(t, c)
	})

	s := newSession(t)
	s.login(localLogin, "alice")
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)

	// The self-test runs in the background at start.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var st struct {
			Enabled  bool
			SelfTest *struct{ StructuredOutput bool }
		}
		s.json(http.MethodGet, "/api/ai/status", nil, http.StatusOK, &st)
		if st.Enabled && st.SelfTest != nil && st.SelfTest.StructuredOutput {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("AI status %+v", st)
		}
		time.Sleep(2 * time.Second)
	}

	const secretFile = "e2e/broken_secret.py"
	ensureHomeClaim(t, c, me.ID)
	runHomeJob(t, s, c, me.ID, "sh", "-c", `mkdir -p "$(dirname "$1")" && printf '%s' "$2" > "$1"`, "seed", "/home/user/"+secretFile,
		"print('cloning with token "+fakeToken+"', flush=True)\nimport plotly_missing_e2e\n")
	broken := crashingApp(s, "e2e ai", secretFile)
	s.waitFor(broken.Name, 4*time.Minute, func(a app) bool { return a.Phase == "Failed" })

	var d struct{ Bundle []struct{ Ref string } }
	s.json(http.MethodGet, "/api/workspaces/"+broken.Name+"/diagnosis", nil, http.StatusOK, &d)
	refs := map[string]bool{}
	for _, e := range d.Bundle {
		refs[e.Ref] = true
	}
	var ex struct {
		Explanation *struct{ Evidence []string }
		Bundle      struct {
			Text   string
			Hidden int
		}
		Message string
	}
	s.json(http.MethodPost, "/api/workspaces/"+broken.Name+"/diagnosis/explain", map[string]any{}, http.StatusOK, &ex)
	if ex.Explanation == nil || len(ex.Explanation.Evidence) == 0 {
		t.Fatalf("no explanation: %+v", ex)
	}
	for _, r := range ex.Explanation.Evidence {
		if !refs[r] {
			t.Errorf("cited %s, which is not in the diagnosis bundle", r)
		}
	}
	if strings.Contains(ex.Bundle.Text, fakeToken) || ex.Bundle.Hidden == 0 {
		t.Fatalf("the fake token was sent (hidden %d): %s", ex.Bundle.Hidden, ex.Bundle.Text)
	}
	if !strings.Contains(ex.Bundle.Text, "plotly_missing_e2e") {
		t.Fatalf("the log was not sent: %s", ex.Bundle.Text)
	}

	var draft struct {
		Draft  struct{ DisplayName, Template string }
		Errors []struct{ Location, Message string }
	}
	seedHome(t, s, c, me.ID)
	s.json(http.MethodPost, "/api/ai/draft", map[string]any{"text": "A Streamlit app from e2e/hello.py called Sales draft", "kind": "app"}, http.StatusOK, &draft)
	if draft.Draft.Template == "" || len(draft.Errors) != 0 {
		t.Fatalf("draft %+v", draft)
	}
	if resp, _ := s.do(http.MethodGet, "/api/workspaces/sales-draft", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a draft created an app: %d", resp.StatusCode)
	}
}

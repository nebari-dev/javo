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

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// envPrinter prints MY_SECRET at start and then serves, so the test reads
// what the app got from its log.
const envPrinter = `import os,http.server as h; print("MY_SECRET="+os.environ.get("MY_SECRET","<unset>"),flush=True); ` +
	`h.HTTPServer(("",int(os.environ["PORT"])),type("S",(h.BaseHTTPRequestHandler,),dict(do_GET=lambda s:(s.send_response(200),s.end_headers())))).serve_forever()`

// appLog waits until the app's log has a line starting with prefix and
// returns that line.
func appLog(t *testing.T, s *session, name, prefix string, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		var logs struct{ Lines []struct{ Text string } }
		s.json(http.MethodGet, "/api/workspaces/"+name+"/logs", nil, http.StatusOK, &logs)
		for _, l := range logs.Lines {
			if strings.HasPrefix(l.Text, prefix) {
				return l.Text
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %q line in the log of %s", prefix, name)
		}
		time.Sleep(2 * time.Second)
	}
}

// A secret stored under Settings reaches an app as an environment variable;
// no API answer and no pod spec holds its value, and a new value restarts
// the app with it.
func TestAppGetsASecret(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	value := fmt.Sprintf("e2e-placeholder-%d", time.Now().UnixNano())
	resp, body := s.do(http.MethodPost, "/api/secrets", map[string]any{"name": "E2E_SECRET", "value": value, "note": "e2e"})
	if resp.StatusCode != http.StatusCreated || strings.Contains(string(body), value) {
		t.Fatalf("create secret: %d %s", resp.StatusCode, body)
	}
	a := s.createApp(map[string]any{
		"displayName": fmt.Sprintf("secret %d", time.Now().Unix()%100000), "template": "custom",
		"command": "python -c '" + envPrinter + "'",
		"env":     []map[string]string{{"name": "MY_SECRET", "secret": "E2E_SECRET"}},
	})
	s.waitFor(a.Name, 5*time.Minute, running(t))
	if got := appLog(t, s, a.Name, "MY_SECRET=", time.Minute); got != "MY_SECRET="+value {
		t.Fatalf("the app got %q", got)
	}

	c := kube(t)
	pod := appPod(t, c, me.ID, a.Name)
	raw, _ := json.Marshal(pod)
	if strings.Contains(string(raw), value) {
		t.Fatal("the value is in the pod spec")
	}
	for _, path := range []string{"/api/secrets", "/api/workspaces/" + a.Name, "/api/workspaces/export"} {
		if _, body := s.do(http.MethodGet, path, nil); strings.Contains(string(body), value) {
			t.Fatalf("GET %s holds the value", path)
		}
	}

	newValue := value + "-2"
	s.json(http.MethodPut, "/api/secrets/E2E_SECRET", map[string]any{"value": newValue}, http.StatusOK, nil)
	deadline := time.Now().Add(3 * time.Minute)
	for {
		var p corev1.Pod
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(pod), &p); err == nil && p.UID != pod.UID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the app did not restart after the secret's value changed")
		}
		time.Sleep(2 * time.Second)
	}
	s.waitFor(a.Name, 5*time.Minute, running(t))
	if got := appLog(t, s, a.Name, "MY_SECRET=", time.Minute); got != "MY_SECRET="+newValue {
		t.Fatalf("after the change the app got %q", got)
	}
	// A secret an app uses cannot be deleted.
	if resp, body := s.do(http.MethodDelete, "/api/secrets/E2E_SECRET", nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete a used secret: %d %s", resp.StatusCode, body)
	}
}

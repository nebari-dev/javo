//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/naming"
)

// Small, well-known images pinned by index digest (DEPENDENCIES.md).
const (
	// Runs as uid 101 and serves its welcome page at / on 8080.
	nonRootImage = "nginxinc/nginx-unprivileged:1.31.6-alpine-slim@sha256:ac4d9c7e457c6510b95ac72dd84b27f7cc68ecb9a83fbd44f05a6460cf08aba4"
	// Names no user (root); the owner's uid runs it.
	rootImage = "traefik/whoami:v1.12.0@sha256:c4717a8d1f0134a7444e24f881160e033991f23027c6c5a9a3f8fd22e70d1d44"
)

func appPod(t *testing.T, c client.Client, ownerID, short string) *corev1.Pod {
	t.Helper()
	var p corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: appsNS, Name: naming.ObjectName(ownerID, short)}, &p); err != nil {
		t.Fatalf("pod of %s: %v", short, err)
	}
	return &p
}

func running(t *testing.T) func(app) bool {
	return func(a app) bool {
		if a.Phase == "Failed" {
			t.Fatalf("%s failed: %s", a.Name, summary(a))
		}
		return a.Phase == "Running"
	}
}

// A typed command is refused before anything is created when only a shell
// could run it, and changing it restarts the app on the new command.
func TestTypedCommand(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	c := kube(t)

	resp, body := s.do(http.MethodPost, "/api/workspaces", map[string]any{
		"displayName": "e2e refused command", "template": "custom", "command": "python sample.py --port=$PORT | tee log",
	})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "body.command") {
		t.Fatalf("shell command: %d %s", resp.StatusCode, body)
	}

	// The sample's own command, in the sample's folder.
	var a app
	s.json(http.MethodPost, "/api/samples/custom", nil, http.StatusCreated, &a)
	t.Cleanup(func() { s.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil) })
	got := s.waitFor(a.Name, 5*time.Minute, running(t))
	if resp, body := s.do(http.MethodGet, got.URL, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Server time") {
		t.Fatalf("GET %s: %d %.300s", got.URL, resp.StatusCode, body)
	}
	old := appPod(t, c, me.ID, a.Name)

	s.json(http.MethodPut, "/api/workspaces/"+a.Name+"/launch",
		map[string]any{"command": "python -u sample.py --port={{.Port}} --base-url={{.BaseURL}}"}, http.StatusOK, nil)
	var pod *corev1.Pod
	deadline := time.Now().Add(3 * time.Minute)
	for {
		var p corev1.Pod
		err := c.Get(context.Background(), client.ObjectKeyFromObject(old), &p)
		if err == nil && p.UID != old.UID {
			pod = &p
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no new pod after the command changed")
		}
		time.Sleep(2 * time.Second)
	}
	if args := pod.Spec.Containers[0].Args; !slices.Equal(args[:2], []string{"python", "-u"}) {
		t.Fatalf("new pod args = %q", args)
	}
	got = s.waitFor(a.Name, 5*time.Minute, running(t))
	if resp, body := s.do(http.MethodGet, got.URL, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Server time") {
		t.Fatalf("after change, GET %s: %d %.300s", got.URL, resp.StatusCode, body)
	}
}

// An image of the user's choice that serves at / answers through the
// gateway, which removes the URL prefix; it runs as its owner (never the
// image's own user) under the restricted pod security.
func TestContainerImage(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	c := kube(t)

	a := s.createApp(map[string]any{"displayName": "e2e nginx " + time.Now().Format("150405"), "template": "container", "image": nonRootImage})
	got := s.waitFor(a.Name, 5*time.Minute, running(t))
	resp, body := s.do(http.MethodGet, got.URL, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Welcome to nginx") {
		t.Fatalf("GET %s: %d %.300s", got.URL, resp.StatusCode, body)
	}
	p := appPod(t, c, me.ID, a.Name)
	psc, csc := p.Spec.SecurityContext, p.Spec.Containers[0].SecurityContext
	if psc.RunAsUser == nil || *psc.RunAsUser == 0 || psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot || psc.SeccompProfile == nil ||
		csc.AllowPrivilegeEscalation == nil || *csc.AllowPrivilegeEscalation || len(csc.Capabilities.Drop) != 1 {
		t.Fatalf("security: pod %+v container %+v", psc, csc)
	}
}

// An image whose user is root runs as its owner's uid and gid instead: the
// image's user is never used.
func TestImageUserIsReplacedByTheOwner(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)

	a := s.createApp(map[string]any{"displayName": "e2e root " + time.Now().Format("150405"), "template": "container",
		"image": rootImage, "port": 8080, "command": "--port={{.Port}}"})
	got := s.waitFor(a.Name, 5*time.Minute, running(t))
	if resp, body := s.do(http.MethodGet, got.URL, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %.300s", got.URL, resp.StatusCode, body)
	}
	psc := appPod(t, kube(t), me.ID, a.Name).Spec.SecurityContext
	if psc.RunAsUser == nil || *psc.RunAsUser == 0 || psc.RunAsGroup == nil || *psc.RunAsGroup == 0 {
		t.Fatalf("pod security context = %+v, want the owner's uid and gid", psc)
	}
}

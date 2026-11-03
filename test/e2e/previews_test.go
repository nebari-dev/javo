//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/naming"
)

type previewItem struct {
	Number int32
	Title  string
	App    string
	Reason string
}

type previewState struct {
	Enabled  bool
	Max      int32
	SyncedAt string
	Reason   string
	Items    []previewItem
}

func (s *session) previews(name string) previewState {
	s.t.Helper()
	var p previewState
	s.json(http.MethodGet, "/api/workspaces/"+name+"/previews", nil, http.StatusOK, &p)
	return p
}

// waitPreviews polls the app's previews until done says so.
func (s *session) waitPreviews(name string, within time.Duration, what string, done func(previewState) bool) previewState {
	s.t.Helper()
	deadline := time.Now().Add(within)
	for {
		p := s.previews(name)
		if done(p) {
			return p
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("%s: previews did not reach %q within %s; items: %+v reason %q", name, what, within, p.Items, p.Reason)
		}
		time.Sleep(2 * time.Second)
	}
}

// childOf is the preview app of pull request number, and "" if it has none.
func childOf(p previewState, number int32) string {
	for _, it := range p.Items {
		if it.Number == number {
			return it.App
		}
	}
	return ""
}

func reasonOf(p previewState, number int32) string {
	for _, it := range p.Items {
		if it.Number == number {
			return it.Reason
		}
	}
	return ""
}

// children are the preview apps of a sync, by pull request number.
func children(p previewState) map[int32]string {
	out := map[int32]string{}
	for _, it := range p.Items {
		if it.App != "" {
			out[it.Number] = it.App
		}
	}
	return out
}

// pageHTML is a page of the static-site fixture with text in it; the build
// fills in {{BUILD}}.
func pageHTML(text string) string {
	return "<!doctype html><title>preview</title><p>" + text + " {{BUILD}}</p>\n"
}

// pullFixture is a repository with one branch and commit per pull request.
type pullFixture struct {
	repo string
	g    *gitea
	fake *fakeGitHub
}

// pull makes branch pr-<n> with its own page and returns the pull request.
func (f pullFixture) pull(n int, fork bool, age time.Duration) fakePull {
	f.g.branch(f.repo, fmt.Sprintf("pr-%d", n))
	sha := f.g.write(f.repo, fmt.Sprintf("pr-%d", n), "src/index.html", pageHTML(fmt.Sprintf("preview of pull request %d", n)))
	return fakePull{Number: n, Title: fmt.Sprintf("change %d", n), HeadSHA: sha, Fork: fork, UpdatedAt: time.Now().Add(-age).UTC()}
}

// newPreviewApp creates a static-site app from a fresh copy of the fixture
// repository; previews are turned on by the caller.
func newPreviewApp(t *testing.T, s *session, g *gitea, run, label string) (pullFixture, app) {
	t.Helper()
	repo := label + "-" + run
	g.seedRepo(repo, "static-site", false)
	f := pullFixture{repo: repo, g: g, fake: newFakeGitHub(t)}
	a := s.createApp(map[string]any{
		"displayName": "e2e " + label + " " + run, "template": "static-site",
		"source": map[string]any{"url": repoURL(repo)},
	})
	return f, a
}

func enablePreviews(t *testing.T, s *session, name string, max int, forks bool) {
	t.Helper()
	s.json(http.MethodPut, "/api/workspaces/"+name+"/previews", map[string]any{"enabled": true, "max": max, "forks": forks}, http.StatusOK, nil)
}

func TestPreviews(t *testing.T) {
	g := newGitea(t)
	t.Parallel()
	s := newUser(t)
	me := s.me()
	c := kube(t)
	run := time.Now().Format("150405")
	f, parent := newPreviewApp(t, s, g, run, "previews")

	// Pull requests 1 and 2 are from the repository, 3 is from a fork.
	pr1, pr2 := f.pull(1, false, 3*time.Minute), f.pull(2, false, 2*time.Minute)
	pr3 := f.pull(3, true, time.Minute)
	open := map[int]fakePull{1: pr1, 2: pr2, 3: pr3}
	setOpen := func() { f.fake.set(f.repo, pullList(open)...) }
	setOpen()
	enablePreviews(t, s, parent.Name, 1, false)

	var seen []string // every preview app name, to check its objects are gone at the end
	var first, second int32
	t.Run("the limit allows one preview and says why the others have none", func(t *testing.T) {
		p := s.waitPreviews(parent.Name, 3*time.Minute, "one child and two reasons", func(p previewState) bool {
			return len(children(p)) == 1 && len(p.Items) == 3
		})
		for n, name := range children(p) {
			first = n
			seen = append(seen, name)
		}
		second = 3 - first
		if first != 1 && first != 2 {
			t.Fatalf("the preview is for pull request %d, which must not have one (3 is a fork)", first)
		}
		if got := reasonOf(p, second); got != "PreviewLimitReached" {
			t.Fatalf("pull request %d: reason %q, want PreviewLimitReached", second, got)
		}
		if got := reasonOf(p, 3); got != "PreviewForkSkipped" {
			t.Fatalf("fork pull request: reason %q, want PreviewForkSkipped", got)
		}
	})

	t.Run("the preview runs at its own URL with its pull request's page", func(t *testing.T) {
		name := childOf(s.previews(parent.Name), first)
		child := s.repoApp(name)
		if child.Source == nil || child.PreviewOf == nil || child.PreviewOf.PullNumber != first || child.PreviewOf.Name != parent.Name {
			t.Fatalf("preview view = %+v", child)
		}
		got := s.waitRepo(name, 6*time.Minute, repoRunning(t))
		if got.URL == s.repoApp(parent.Name).URL {
			t.Fatal("the preview has the parent's URL")
		}
		s.waitPage(got.URL, fmt.Sprintf("preview of pull request %d", first), time.Minute)
		// A preview mounts no home folder and carries no API token.
		pod := appPod(t, c, me.ID, name)
		for _, v := range pod.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				t.Errorf("the preview mounts claim %s", v.PersistentVolumeClaim.ClaimName)
			}
		}
		for _, ct := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
			for _, e := range ct.Env {
				if e.Name == "JAVO_TOKEN" {
					t.Errorf("container %s has JAVO_TOKEN", ct.Name)
				}
			}
		}
	})

	t.Run("closing a pull request removes its preview and frees the slot", func(t *testing.T) {
		old := childOf(s.previews(parent.Name), first)
		delete(open, int(first))
		setOpen()
		p := s.waitPreviews(parent.Name, 3*time.Minute, "the other pull request has the preview", func(p previewState) bool {
			return childOf(p, second) != "" && childOf(p, first) == ""
		})
		seen = append(seen, childOf(p, second))
		waitGone(t, s, c, me.ID, old)
	})

	t.Run("a webhook with a good signature starts a sync before the poll", func(t *testing.T) {
		// Pull request 4 appears, older than the others (previews go to the most recently updated); the limit is 1 and `second` has the preview.
		open[4] = f.pull(4, false, 10*time.Minute)
		setOpen()
		hook := makeWebhook(t, s, parent.Name)
		s.waitPreviews(parent.Name, 2*time.Minute, "pull request 4 listed", func(p previewState) bool { return reasonOf(p, 4) == "PreviewLimitReached" })
		// Wait for the next sync, so the one after it is a full poll interval
		// away, then change the state and ring the webhook at once.
		synced := s.previews(parent.Name).SyncedAt
		s.waitPreviews(parent.Name, 2*time.Minute, "a later sync", func(p previewState) bool { return p.SyncedAt != synced })

		seen = append(seen, childOf(s.previews(parent.Name), int32(second)))
		delete(open, int(second))
		setOpen()
		if code := hook.deliver("wrong-" + hook.secret); code != http.StatusUnauthorized {
			t.Fatalf("a delivery with a wrong signature: %d, want 401", code)
		}
		if code := hook.deliver(hook.secret); code/100 != 2 {
			t.Fatalf("a signed delivery: %d", code)
		}
		p := s.waitPreviews(parent.Name, 15*time.Second, "pull request 4 has the preview within 15 s", func(p previewState) bool {
			return childOf(p, 4) != ""
		})
		seen = append(seen, childOf(p, 4))
	})

	t.Run("deleting the app removes every preview", func(t *testing.T) {
		s.json(http.MethodDelete, "/api/workspaces/"+parent.Name, nil, http.StatusNoContent, nil)
		for _, name := range seen {
			if name != "" {
				waitGone(t, s, c, me.ID, name)
			}
		}
	})
}

// pullList is the pull requests of open, in number order.
func pullList(open map[int]fakePull) []fakePull {
	var out []fakePull
	for n := 1; len(out) < len(open); n++ {
		if p, ok := open[n]; ok {
			out = append(out, p)
		}
	}
	return out
}

// waitGone waits until preview app name has no row and no pod.
func waitGone(t *testing.T, s *session, c client.Client, ownerID, name string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		resp, _ := s.do(http.MethodGet, "/api/workspaces/"+name, nil)
		var pod corev1.Pod
		err := c.Get(context.Background(), client.ObjectKey{Namespace: appsNS, Name: naming.ObjectName(ownerID, name)}, &pod)
		if resp.StatusCode == http.StatusNotFound && client.IgnoreNotFound(err) == nil && err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("preview %s is still there: API %d, pod error %v", name, resp.StatusCode, err)
		}
		time.Sleep(2 * time.Second)
	}
}

// webhook is an app's webhook address and secret.
type webhook struct {
	t      *testing.T
	s      *session
	url    string
	secret string
}

func makeWebhook(t *testing.T, s *session, name string) webhook {
	t.Helper()
	var out struct{ URL, Secret string }
	s.json(http.MethodPost, "/api/workspaces/"+name+"/previews/webhook", nil, http.StatusOK, &out)
	if out.URL == "" || out.Secret == "" {
		t.Fatalf("webhook answer %+v", out)
	}
	return webhook{t: t, s: s, url: out.URL, secret: out.Secret}
}

// deliver posts a pull_request event signed with key and returns the status.
func (h webhook) deliver(key string) int {
	h.t.Helper()
	body := []byte(`{"action":"closed"}`)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	req, err := http.NewRequest(http.MethodPost, h.s.abs(h.url), bytes.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := h.s.http.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// With Postgres the leader controller can die in the middle of a sync: the
// other replica takes over and the app ends with exactly the allowed number
// of previews, never more.
func TestPreviewsSurviveLeaderLoss(t *testing.T) {
	if sqliteInstall(t) {
		t.Skip("the release runs SQLite: one pod is both gateway and controller")
	}
	g := newGitea(t)
	s := newSession(t)
	s.login(localLogin, "alice")
	c := kube(t)
	f, parent := newPreviewApp(t, s, g, time.Now().Format("150405"), "failover")
	f.fake.set(f.repo, f.pull(1, false, 3*time.Minute), f.pull(2, false, 2*time.Minute), f.pull(3, false, time.Minute))
	// Hold the pull request list so the sync is stuck in its first call, then
	// kill the leader there: the next leader must run the whole sync itself.
	f.fake.hold(f.repo, 25*time.Second)
	enablePreviews(t, s, parent.Name, 2, false)
	f.fake.waitHeld(2 * time.Minute)
	deleteControllerLeader(t, c)
	f.fake.hold(f.repo, 0)

	// Count the real preview apps (the API's own list), not the stored sync
	// result: a sync that ran twice leaves two apps for one pull request.
	deadline := time.Now().Add(5 * time.Minute)
	for previewApps(s, parent.Name) != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("%d preview apps after 5m, want 2: %+v", previewApps(s, parent.Name), s.previews(parent.Name).Items)
		}
		time.Sleep(2 * time.Second)
	}
	// Two more poll intervals: a sync that ran twice must not have made a third.
	for end := time.Now().Add(40 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Second) {
		if got := previewApps(s, parent.Name); got != 2 {
			t.Fatalf("%d preview apps, want exactly 2: %+v", got, s.previews(parent.Name).Items)
		}
	}
}

// previewApps counts the apps the API lists (with previews=true) as previews of parent.
func previewApps(s *session, parent string) int {
	s.t.Helper()
	var list struct {
		Items []struct {
			PreviewOf *struct{ Name string }
		}
	}
	s.json(http.MethodGet, "/api/workspaces?previews=true", nil, http.StatusOK, &list)
	n := 0
	for _, it := range list.Items {
		if it.PreviewOf != nil && it.PreviewOf.Name == parent {
			n++
		}
	}
	return n
}

// Live smoke against the real github.com (JAVO_E2E_GITHUB=1), on a cluster
// whose DNS does not send github.com to the hermetic Gitea (that is, not
// started with JAVO_E2E_GIT=1) and whose source.previews is enabled. A static
// site from a public repository; its previews with forks turned on are safe
// because nginx only serves files. The repository has only a README and the
// template's readiness check asks for "/", so the build copies the README to
// index.html.
func TestLiveGitHub(t *testing.T) {
	if os.Getenv("JAVO_E2E_GITHUB") != "1" {
		t.Skip("set JAVO_E2E_GITHUB=1 to run against github.com")
	}
	var gitea appsv1.Deployment
	if err := kube(t).Get(context.Background(), client.ObjectKey{Namespace: e2eNS, Name: "gitea"}, &gitea); err == nil {
		t.Skip("this cluster sends github.com to the hermetic Gitea (JAVO_E2E_GIT=1): run make e2e-github on a cluster made without it")
	}
	s := newSession(t)
	s.login(localLogin, "alice")
	a := s.createApp(map[string]any{
		"displayName": "e2e live github " + time.Now().Format("150405"), "template": "static-site",
		"source": map[string]any{
			"url":   "https://github.com/octocat/Hello-World",
			"build": map[string]any{"command": "mkdir dist && cp README dist/index.html", "output": "dist"},
		},
	})
	got := s.waitRepo(a.Name, 6*time.Minute, repoRunning(t))
	s.waitPage(got.URL, "Hello World", time.Minute)

	enablePreviews(t, s, a.Name, 2, true)
	p := s.waitPreviews(a.Name, 5*time.Minute, "two previews and the rest over the limit", func(p previewState) bool {
		over := 0
		for _, it := range p.Items {
			if it.Reason == "PreviewLimitReached" {
				over++
			}
		}
		return len(children(p)) == 2 && over > 0
	})
	t.Logf("%d pull requests listed, %d previews", len(p.Items), len(children(p)))
	if strings.TrimSpace(p.Reason) != "" {
		t.Fatalf("the sync failed: %s", p.Reason)
	}
}

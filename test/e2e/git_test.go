//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/naming"
)

// repoApp is an app's view with the repository part.
type repoApp struct {
	app
	Source *struct {
		URL, Ref, Path, Commit string
		HasToken               bool
	}
	Redeployable bool
	PreviewOf    *struct {
		Name       string
		PullNumber int32
	}
}

func (s *session) repoApp(name string) repoApp {
	s.t.Helper()
	var a repoApp
	s.json(http.MethodGet, "/api/workspaces/"+name, nil, http.StatusOK, &a)
	return a
}

// waitRepo polls the app until done says so.
func (s *session) waitRepo(name string, within time.Duration, done func(repoApp) bool) repoApp {
	s.t.Helper()
	deadline := time.Now().Add(within)
	last := ""
	for {
		a := s.repoApp(name)
		commit := ""
		if a.Source != nil {
			commit = a.Source.Commit
		}
		if line := a.Phase + " " + failedReason(a.app) + " " + commit; line != last {
			s.t.Logf("%s: %s", name, line)
			last = line
		}
		if done(a) {
			return a
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("%s: not done within %s; last: %s %s", name, within, a.Phase, summary(a.app))
		}
		time.Sleep(2 * time.Second)
	}
}

// failedReason is the reason of the first step that failed; empty if none.
func failedReason(a app) string {
	for _, st := range a.Steps {
		if st.Status == "False" {
			return st.Reason
		}
	}
	return ""
}

// waitFailed waits for the app to fail and returns its reason.
func (s *session) waitFailed(name string, within time.Duration) string {
	s.t.Helper()
	a := s.waitRepo(name, within, func(a repoApp) bool { return a.Phase == "Failed" })
	return failedReason(a.app)
}

// get fetches an app page through the gateway and returns the status and body.
func (s *session) page(u string) (int, string) {
	s.t.Helper()
	resp, body := s.do(http.MethodGet, u, nil)
	return resp.StatusCode, string(body)
}

// waitPage waits until the page answers 200 and, when want is not empty,
// contains it.
func (s *session) waitPage(u, want string, within time.Duration) string {
	s.t.Helper()
	deadline := time.Now().Add(within)
	for {
		code, body := s.page(u)
		if code == http.StatusOK && strings.Contains(body, want) {
			return body
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("GET %s: %d, want 200 with %q; body: %.300s", u, code, want, body)
		}
		time.Sleep(2 * time.Second)
	}
}

// catIn reads path in the app container of the pod.
func catIn(t *testing.T, pod *corev1.Pod, path string) string {
	t.Helper()
	return kubectl(t, "-n", pod.Namespace, "exec", pod.Name, "-c", "app", "--", "cat", path)
}

// The python back-end repository answers every path, so a custom app
// from it is a plain "any command serving HTTP on the port" check.
const backendCommand = "python server.py {{.Port}}"

func TestGitApps(t *testing.T) {
	g := newGitea(t)
	t.Parallel()
	s := newUser(t)
	me := s.me()
	c := kube(t)
	run := time.Now().Format("150405")

	t.Run("a public repository runs, redeploys and shows the new commit", func(t *testing.T) {
		repo := "streamlit-" + run
		first := g.seedRepo(repo, "streamlit-app", false)
		a := s.createApp(map[string]any{
			"displayName": "e2e git streamlit " + run, "template": "streamlit", "file": "app.py",
			"source": map[string]any{"url": repoURL(repo), "path": "app"},
		})
		got := s.waitRepo(a.Name, 5*time.Minute, func(a repoApp) bool {
			if a.Phase == "Failed" {
				t.Fatalf("failed: %s", summary(a.app))
			}
			return a.Phase == "Running"
		})
		if got.Source == nil || got.Source.Commit != first {
			t.Fatalf("source = %+v, want commit %s", got.Source, first)
		}
		if !got.Redeployable {
			t.Fatal("a repository app is not redeployable")
		}
		if code, body := s.page(got.URL); code != http.StatusOK || !strings.Contains(body, "<title>Streamlit</title>") {
			t.Fatalf("GET %s: %d %.200s", got.URL, code, body)
		}
		if text := catIn(t, appPod(t, c, me.ID, a.Name), "/srv/src/current/app/app.py"); !strings.Contains(text, "git v1") {
			t.Fatalf("the clone holds %q", text)
		}

		second := g.write(repo, "main", "app/app.py", "import streamlit as st\n\nst.title(\"hello from git v2\")\n")
		s.json(http.MethodPost, "/api/workspaces/"+a.Name+"/redeploy", nil, http.StatusOK, nil)
		s.waitRepo(a.Name, 5*time.Minute, func(a repoApp) bool {
			return a.Phase == "Running" && a.Source != nil && a.Source.Commit == second
		})
		if text := catIn(t, appPod(t, c, me.ID, a.Name), "/srv/src/current/app/app.py"); !strings.Contains(text, "git v2") {
			t.Fatalf("after Redeploy the clone holds %q", text)
		}
	})

	t.Run("a wrong branch fails with RefNotFound", func(t *testing.T) {
		repo := "backend-ref-" + run
		g.seedRepo(repo, "backend-python", false)
		a := s.createApp(map[string]any{
			"displayName": "e2e git wrong branch " + run, "template": "custom", "file": "server.py", "command": backendCommand,
			"source": map[string]any{"url": repoURL(repo), "ref": "no-such-branch"},
		})
		if got := s.waitFailed(a.Name, 4*time.Minute); got != "RefNotFound" {
			t.Fatalf("reason %q, want RefNotFound", got)
		}
	})

	t.Run("a private repository needs the right token, which never leaks", func(t *testing.T) {
		repo := "backend-private-" + run
		head := g.seedRepo(repo, "backend-python", true)
		good := g.readToken()
		bad := "e2e-" + randomHex(t, 20)
		// Tokens come from secrets.
		s.json(http.MethodPost, "/api/secrets", map[string]any{"name": "E2E_GOOD_TOKEN_" + run, "value": good}, http.StatusCreated, nil)
		s.json(http.MethodPost, "/api/secrets", map[string]any{"name": "E2E_BAD_TOKEN_" + run, "value": bad}, http.StatusCreated, nil)
		body := func(label, secret string) map[string]any {
			src := map[string]any{"url": repoURL(repo)}
			if secret != "" {
				src["tokenSecret"] = secret
			}
			return map[string]any{"displayName": "e2e git private " + label + " " + run, "template": "custom", "file": "server.py", "command": backendCommand, "source": src}
		}

		// No token: the host answers as for a repository that is not there.
		none := s.createApp(body("no token", ""))
		if got := s.waitFailed(none.Name, 4*time.Minute); got != "RepoNotFound" && got != "AuthFailed" {
			t.Fatalf("no token: reason %q, want RepoNotFound or AuthFailed", got)
		}
		s.json(http.MethodDelete, "/api/workspaces/"+none.Name, nil, http.StatusNoContent, nil)

		// A wrong token is refused.
		a := s.createApp(body("wrong token", "E2E_BAD_TOKEN_"+run))
		if got := s.waitFailed(a.Name, 4*time.Minute); got != "AuthFailed" {
			t.Fatalf("wrong token: reason %q, want AuthFailed", got)
		}
		// The right one, picked afterwards, restarts the app and it runs.
		s.json(http.MethodPut, "/api/workspaces/"+a.Name+"/source/token", map[string]any{"secret": "E2E_GOOD_TOKEN_" + run}, http.StatusNoContent, nil)
		got := s.waitRepo(a.Name, 5*time.Minute, func(a repoApp) bool {
			if a.Phase == "Failed" && failedReason(a.app) != "AuthFailed" {
				t.Fatalf("failed: %s", summary(a.app))
			}
			return a.Phase == "Running"
		})
		if got.Source == nil || got.Source.Commit != head || !got.Source.HasToken {
			t.Fatalf("source = %+v, want commit %s and hasToken", got.Source, head)
		}
		s.waitPage(got.URL, "hello from a python back-end", time.Minute)

		// Control: the same search finds what the database does hold, so a
		// count of 0 for the tokens is not a search that finds nothing.
		if n := dbCopiesOf(t, c, repoURL(repo)); n == 0 {
			t.Fatal("the database search does not find the app's own repository address")
		}
		for _, tok := range []string{good, bad} {
			assertTokenNowhere(t, s, c, me.ID, a.Name, tok)
		}
		// The controller opened the secret into the app's Secret: it holds the token.
		secret := kubectl(t, "-n", appsNS, "get", "secret", workspace.GitTokenSecretName(naming.ObjectName(me.ID, a.Name)),
			"-o", "go-template={{index .data \"token\" | base64decode}}")
		if secret != good {
			t.Fatal("the app's Secret does not hold the token")
		}
	})
}

// assertTokenNowhere fails if token appears where it must not: in an API
// response about the app, in the database (a copy taken with the CLI, or the
// Postgres dump), in the app container's environment and files, or in the
// gateway and controller logs.
func assertTokenNowhere(t *testing.T, s *session, c client.Client, ownerID, name, token string) {
	t.Helper()
	for _, path := range []string{"/api/workspaces/" + name, "/api/workspaces", "/api/workspaces/" + name + "/previews"} {
		if _, body := s.do(http.MethodGet, path, nil); bytes.Contains(body, []byte(token)) {
			t.Errorf("GET %s returns the token", path)
		}
	}
	if n := dbCopiesOf(t, c, token); n != 0 {
		t.Errorf("the database or its backup holds the token %d time(s)", n)
	}
	pod := appPod(t, c, ownerID, name)
	if out := kubectl(t, "-n", pod.Namespace, "exec", pod.Name, "-c", "app", "--", "sh", "-c", "env; ls /etc/javo/git 2>&1; cat /proc/1/cmdline | tr '\\0' ' '"); strings.Contains(out, token) {
		t.Errorf("the app container shows the token")
	}
	spec := kubectl(t, "-n", pod.Namespace, "get", "pod", pod.Name, "-o", "json")
	if strings.Contains(spec, token) {
		t.Errorf("the pod spec or status holds the token")
	}
	logs := kubectl(t, "-n", gatewayNS, "logs", "-l", "app.kubernetes.io/instance=javo,app.kubernetes.io/name=javo", "--all-containers", "--tail=-1", "--prefix=false")
	if strings.Contains(logs, token) {
		t.Errorf("a gateway or controller log holds the token")
	}
	events := kubectl(t, "get", "events", "-A", "-o", "json")
	if strings.Contains(events, token) {
		t.Errorf("an Event holds the token")
	}
}

// dbCopiesOf counts the places the database holds token: a backup copy (a
// logical dump, which holds no free pages) with SQLite, pg_dump with Postgres.
func dbCopiesOf(t *testing.T, c client.Client, token string) int {
	t.Helper()
	if !sqliteInstall(t) {
		cmd := exec.Command("kubectl", "-n", "javo-postgres", "exec", "deploy/postgres", "--", "sh", "-c",
			`PGPASSWORD="$POSTGRES_PASSWORD" pg_dump -h 127.0.0.1 -U postgres javo`)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			t.Fatalf("pg_dump: %v\n%s", err, errb.String())
		}
		return bytes.Count(out.Bytes(), []byte(token))
	}
	file := "/var/lib/javo/e2e-git-backup-" + time.Now().Format("20060102150405") + ".db"
	javoCLI(t, "db", "backup", file)
	out := runGrepPod(t, c, token, file)
	var n int
	if _, err := fmt.Sscan(out, &n); err != nil {
		t.Fatalf("grep count %q: %v", out, err)
	}
	return n
}

// runGrepPod counts the lines of file (on the server's data volume) that
// hold token, in a one-off pod, and returns its output.
func runGrepPod(t *testing.T, c client.Client, token, file string) string {
	t.Helper()
	ctx := context.Background()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-grep-", Namespace: gatewayNS},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			AutomountServiceAccountToken: ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To[int64](65532), RunAsGroup: ptr.To[int64](65532),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name: "grep", Image: helperImage, ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"sh", "-c", `grep -a -c -F -- "$1" "$2" || true`, "grep", token, file},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
					Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/var/lib/javo", ReadOnly: true}},
			}},
			Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "javo-data", ReadOnly: true},
			}}},
		},
	}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Delete(context.Background(), pod) }()
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(2 * time.Second) {
		var p corev1.Pod
		if err := c.Get(ctx, client.ObjectKeyFromObject(pod), &p); err != nil {
			t.Fatal(err)
		}
		if p.Status.Phase == corev1.PodSucceeded {
			return strings.TrimSpace(podLog(t, pod))
		}
		if p.Status.Phase == corev1.PodFailed || time.Now().After(deadline) {
			t.Fatalf("grep pod %s", p.Status.Phase)
		}
	}
}

// The templates that take a repository or a home file: a front-end built
// in the pod and served by nginx, one HTML file from the home, a plain
// Python back-end and a Panel file.
func TestGitTemplates(t *testing.T) {
	g := newGitea(t)
	t.Parallel()
	s := newUser(t)
	me := s.me()
	c := kube(t)
	run := time.Now().Format("150405")

	t.Run("static-site builds the repository and serves its pages", func(t *testing.T) {
		repo := "site-" + run
		g.seedRepo(repo, "static-site", false)
		a := s.createApp(map[string]any{
			"displayName": "e2e static from git " + run, "template": "static-site",
			"source": map[string]any{"url": repoURL(repo)},
		})
		got := s.waitRepo(a.Name, 6*time.Minute, repoRunning(t))
		// The page carries the build's mark, so it is the built file and not the source.
		s.waitPage(got.URL, "built-by-javo-static-site-fixture", time.Minute)
		if code, body := s.page(got.URL + "style.css"); code != http.StatusOK || body == "" {
			t.Fatalf("relative asset: %d", code)
		}
		if code, _ := s.page(got.URL + "docs/"); code != http.StatusOK {
			t.Fatalf("folder page: %d", code)
		}
	})

	t.Run("static-site serves a repository without a package.json as it is", func(t *testing.T) {
		repo := "plain-" + run
		g.seedRepo(repo, "static-plain", false)
		a := s.createApp(map[string]any{
			"displayName": "e2e plain static from git " + run, "template": "static-site",
			"source": map[string]any{"url": repoURL(repo)},
		})
		got := s.waitRepo(a.Name, 6*time.Minute, repoRunning(t))
		s.waitPage(got.URL, "served-as-is-by-javo-static-plain-fixture", time.Minute)
		if code, _ := s.page(got.URL + "notes/"); code != http.StatusOK {
			t.Fatalf("folder page: %d", code)
		}
	})

	t.Run("static-site serves one HTML file of the home and Redeploy picks up edits", func(t *testing.T) {
		ensureHomeClaim(t, c, me.ID)
		const file = "e2e/page.html"
		write := func(text string) {
			runHomeJob(t, s, c, me.ID, "sh", "-c", `mkdir -p "$(dirname "$1")" && printf '%s' "$2" > "$1"`, "seed", "/home/user/"+file, "<!doctype html><title>page</title><p>"+text+"</p>")
		}
		write("first text " + run)
		a := s.createApp(map[string]any{"displayName": "e2e static from home " + run, "template": "static-site", "file": file})
		got := s.waitRepo(a.Name, 5*time.Minute, repoRunning(t))
		s.waitPage(got.URL, "first text "+run, time.Minute)
		if !got.Redeployable {
			t.Fatal("a static home-file app is not redeployable")
		}
		write("second text " + run)
		s.json(http.MethodPost, "/api/workspaces/"+a.Name+"/redeploy", nil, http.StatusOK, nil)
		s.waitPage(got.URL, "second text "+run, 3*time.Minute)
	})

	t.Run("a custom command serves HTTP from a repository", func(t *testing.T) {
		repo := "backend-" + run
		g.seedRepo(repo, "backend-python", false)
		a := s.createApp(map[string]any{
			"displayName": "e2e custom from git " + run, "template": "custom", "file": "server.py", "command": backendCommand,
			"source": map[string]any{"url": repoURL(repo)},
		})
		got := s.waitRepo(a.Name, 5*time.Minute, repoRunning(t))
		s.waitPage(got.URL, "hello from a python back-end", time.Minute)
	})

	t.Run("a Panel file runs from a repository", func(t *testing.T) {
		repo := "panel-" + run
		g.seedRepo(repo, "panel-app", false)
		a := s.createApp(map[string]any{
			"displayName": "e2e panel from git " + run, "template": "panel", "file": "dashboard.py",
			"source": map[string]any{"url": repoURL(repo)},
		})
		got := s.waitRepo(a.Name, 5*time.Minute, repoRunning(t))
		s.waitPage(got.URL+"dashboard", "", time.Minute)
	})
}

// repoRunning is done when the app runs, and fatal when it fails.
func repoRunning(t *testing.T) func(repoApp) bool {
	return func(a repoApp) bool {
		if a.Phase == "Failed" {
			t.Fatalf("%s failed: %s", a.Name, summary(a.app))
		}
		return a.Phase == "Running"
	}
}

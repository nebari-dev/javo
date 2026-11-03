//go:build e2e

// Package e2e drives a deployed javo through its gateway, the way a browser
// does. Run it with `make e2e`, which first deploys to this checkout's kind
// cluster (scripts/dev/up.sh; javo-dev in the main checkout). It needs:
//
//   - the UI at JAVO_URL (default http://javo.localhost:<JAVO_HTTP_PORT>, the
//     kind cluster, with the port from the environment or .dev/kind.env and
//     8080 without either; apps there are served on the apps.localhost host);
//   - KUBECONFIG pointing at the cluster (make e2e uses .dev/kubeconfig);
//   - alice's local password in JAVO_PASSWORD_FILE (default .dev/alice.password,
//     written by dev-up; scripts/install/deploy-single-node.sh writes .dev/alice.<site>.password);
//   - for the OIDC and sharing tests, the dev Keycloak and the passwords
//     dev-up writes to .dev/ (see oidc_test.go). They are skipped on a
//     deployment that offers no Keycloak sign-in unless JAVO_E2E_TARGET is
//     kind (the default) or JAVO_E2E_KEYCLOAK is 1, where a missing Keycloak
//     is a failure.
//
// JAVO_E2E_TARGET names the deployment: kind (the default) or dev
// (scripts/dev/up.sh on either cluster, with deploy/dev/values.yaml), compose
// (the Compose install), single-node (scripts/install/deploy-single-node.sh; any
// other name, such as homelab, is treated the same). JAVO_E2E_IMAGE is the image the tests'
// helper pods run (default: the upstream python slim image, pinned by digest
// in DEPENDENCIES.md, which the cluster pulls).
//
// Most tests sign in as a local account of their own (newUser) and run in
// parallel (go test -parallel). The ones that restart or reconfigure the
// server, change a shared account or measure timings run alone, before them.
//
// The HTTP client resolves every *.localhost name to 127.0.0.1 itself, so
// no DNS setup is needed for the kind hosts.
//
// The cluster client is used only for what a user cannot do through the
// gateway: seeding a file into alice's home (a one-off Job, see seedHome),
// reading pods, and probing the NetworkPolicy.
package e2e

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
)

const (
	appsNS      = model.DefaultWorkspaceNamespace
	gatewayNS   = "javo-system"
	seedFile    = "e2e/hello.py"
	missingTmpl = "e2e-missing-image"
)

// helperImage runs the seed Job and the probe pod (sh, grep and python):
// JAVO_E2E_IMAGE, or the upstream python slim image.
var helperImage = cmp.Or(os.Getenv("JAVO_E2E_IMAGE"), "python:3.13.15-slim-bookworm@sha256:2325bb286ec344af3e5898cc224b5844e2707ac6e26b1632516fd3edc84a5e26")

// The Streamlit app the test runs from alice's home.
const helloPy = `import streamlit as st

st.title("hello from e2e")
`

type step struct {
	Type, Status, Reason, Title string
	Evidence                    []string
}

type app struct {
	Name, DisplayName, Phase, URL string
	Steps                         []step
}

// session is a signed-in browser: a cookie jar plus the gateway URL.
type session struct {
	t    *testing.T
	base string
	http *http.Client
}

// uiBase is the UI's URL: JAVO_URL, or the kind cluster's UI host.
func uiBase() string {
	if u := os.Getenv("JAVO_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://javo.localhost:" + kindPort("JAVO_HTTP_PORT", "8080")
}

// kindPort is a host port of this checkout's kind cluster: the variable key
// from the environment, else from .dev/kind.env (written by scripts/dev/env.sh),
// else def.
func kindPort(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	wd, err := os.Getwd()
	if err != nil {
		return def
	}
	b, err := os.ReadFile(filepath.Join(wd, "..", "..", ".dev", "kind.env"))
	if err != nil {
		return def
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok && v != "" {
			return v
		}
	}
	return def
}

// onKind is true unless JAVO_E2E_TARGET names another target.
func onKind() bool {
	t := os.Getenv("JAVO_E2E_TARGET")
	return t == "" || t == "kind"
}

// devValues is true on a deployment made by scripts/dev/up.sh (kind or dev):
// deploy/dev/values.yaml, and kubectl reaches the cluster.
func devValues() bool {
	return onKind() || os.Getenv("JAVO_E2E_TARGET") == "dev"
}

// dialLocalhost connects every *.localhost name to 127.0.0.1, the way
// browsers resolve them, so the kind hosts need no DNS entries.
func dialLocalhost(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		addr = net.JoinHostPort("127.0.0.1", port)
	}
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
}

// newClient is a browser-like client: its own cookie jar, redirects followed.
func newClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = dialLocalhost
	return &http.Client{Jar: jar, Timeout: 30 * time.Second, Transport: tr}
}

func newSession(t *testing.T) *session {
	t.Helper()
	return &session{t: t, base: uiBase(), http: newClient(t)}
}

// abs resolves a path against the UI; absolute URLs (apps on their own
// host) are kept.
func (s *session) abs(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return s.base + path
}

func (s *session) do(method, path string, body any) (*http.Response, []byte) {
	s.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.abs(path), r)
	if err != nil {
		s.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	return resp, data
}

func (s *session) json(method, path string, body any, want int, out any) {
	s.t.Helper()
	resp, data := s.do(method, path, body)
	if resp.StatusCode != want {
		s.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			s.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
}

// provider is a way to sign in: local accounts or the dev Keycloak.
type provider int

const (
	localLogin provider = iota
	keycloakLogin
)

// login signs user in with p. Local sign-in reads the password from
// JAVO_PASSWORD_FILE (alice only) or .dev/<user>.password; Keycloak sign-in
// from .dev/<user>.keycloak<JAVO_SECRETS_SUFFIX>.password.
func (s *session) login(p provider, user string) {
	s.t.Helper()
	switch p {
	case localLogin:
		file := filepath.Join(repoRoot(s.t), ".dev", user+".password")
		if f := os.Getenv("JAVO_PASSWORD_FILE"); f != "" && user == "alice" {
			file = f
		}
		s.loginLocal(user, readSecret(s.t, file))
	case keycloakLogin:
		s.loginKeycloak(user, readSecret(s.t, devFile(s.t, user+".keycloak"+os.Getenv("JAVO_SECRETS_SUFFIX")+".password")))
	}
}

func (s *session) loginLocal(user, password string) {
	s.t.Helper()
	body := map[string]string{"username": user, "password": password}
	s.json(http.MethodPost, "/api/auth/local/login", body, http.StatusOK, nil)
}

// readSecret returns the trimmed contents of file, never logging them.
func readSecret(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s (run make dev-up): %v", file, err)
	}
	return strings.TrimSpace(string(b))
}

// devFile is the path of name in the repository's .dev directory.
func devFile(t *testing.T, name string) string {
	return filepath.Join(repoRoot(t), ".dev", name)
}

// with is s reporting to t: the same browser (cookies) in a subtest, so
// parallel subtests each fail their own test.
func (s *session) with(t *testing.T) *session {
	c := *s
	c.t = t
	return &c
}

// newUser creates a local account for t alone (`javo user add` in the pod
// that holds the database) and returns a session signed in as it. When the
// test ends, after its own cleanups (its apps), the account, its home and
// the home's files are removed (javo user remove --delete-home). Tests on accounts of their own share no home folder,
// apps, sample copies or file pod, so they run side by side.
func newUser(t *testing.T) *session {
	t.Helper()
	name := userName(t.Name())
	password := strings.TrimSpace(javoCLI(t, "user", "add", name, "--password-file", "-", "--force"))
	s := newSession(t)
	var id string
	t.Cleanup(func() {
		if id != "" {
			// The home's prepare Job goes first: a short test can end before
			// it ran, and its pod would wait on the deleted claim until the
			// Job's deadline.
			if err := kube(t).DeleteAllOf(context.Background(), &batchv1.Job{}, client.InNamespace(appsNS),
				client.MatchingLabels{model.LabelOwner: id, model.LabelRole: model.RoleHomePrepare},
				client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
				t.Errorf("delete the home prepare Job of %s: %v", name, err)
			}
		}
		// The account, its home row (and backup bookkeeping) and its files.
		javoCLI(t, "user", "remove", name, "--delete-home")
	})
	s.loginLocal(name, password)
	id = s.me().ID
	return s
}

// userName is a local account name for the test called test: e2e-, the name
// in lower case letters and digits, and a random suffix (runs do not reuse
// accounts).
func userName(test string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(test) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if s := b.String(); s != "" && !strings.HasSuffix(s, "-") {
			b.WriteByte('-')
		}
	}
	base := strings.TrimSuffix(strings.TrimPrefix(b.String(), "test"), "-")
	if len(base) > 40 {
		base = base[:40]
	}
	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	return "e2e-" + strings.Trim(base, "-") + "-" + hex.EncodeToString(suffix)
}

// create makes an app and registers its deletion at the end of the test.
func (s *session) create(displayName, template, file string) app {
	s.t.Helper()
	body := map[string]any{"displayName": displayName, "template": template}
	if file != "" {
		body["file"] = file
	}
	return s.createApp(body)
}

// createApp posts body to create an app and registers its deletion at the
// end of the test.
func (s *session) createApp(body map[string]any) app {
	s.t.Helper()
	var a app
	s.json(http.MethodPost, "/api/workspaces", body, http.StatusCreated, &a)
	s.t.Cleanup(func() {
		resp, data := s.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil)
		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
			s.t.Errorf("delete %s: %d %s", a.Name, resp.StatusCode, data)
		}
	})
	return a
}

// waitFor polls the app until done returns true, logging each phase change.
func (s *session) waitFor(name string, within time.Duration, done func(app) bool) app {
	s.t.Helper()
	deadline := time.Now().Add(within)
	last := ""
	for {
		var a app
		s.json(http.MethodGet, "/api/workspaces/"+name, nil, http.StatusOK, &a)
		if line := a.Phase + " " + summary(a); line != last {
			s.t.Logf("%s: %s", name, line)
			last = line
		}
		if done(a) {
			return a
		}
		if waitsForRoom(a) {
			// Tests run side by side on one node: time spent queued for
			// room another test's app holds is not this app's to answer for.
			deadline = deadline.Add(2 * time.Second)
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("%s: not done within %s; last: %s", name, within, last)
		}
		time.Sleep(2 * time.Second)
	}
}

// waitsForRoom reports whether a waits for a node with room (Scheduled is
// False with NoCapacity).
func waitsForRoom(a app) bool {
	return slices.ContainsFunc(a.Steps, func(st step) bool {
		return st.Type == "Scheduled" && st.Status == "False" && st.Reason == "NoCapacity"
	})
}

func summary(a app) string {
	var parts []string
	for _, st := range a.Steps {
		parts = append(parts, st.Type+"="+st.Status+"/"+st.Reason)
	}
	return strings.Join(parts, " ")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..")
}

func kube(t *testing.T) client.Client {
	t.Helper()
	cfg, err := config.GetConfig()
	if err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, batchv1.AddToScheme, appsv1.AddToScheme, coordinationv1.AddToScheme, authorizationv1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// restrictedSecurity runs a pod as uid, the way the controller runs apps.
func restrictedSecurity(uid int64) (*corev1.PodSecurityContext, *corev1.SecurityContext) {
	return &corev1.PodSecurityContext{
			RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(uid), RunAsGroup: ptr.To[int64](100),
			FSGroup:        ptr.To[int64](100),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		}, &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		}
}

// seedHome writes seedFile into alice's home with a one-off Job, standing in
// for a user who uploaded their code. A user without an app yet has no home
// claim, so the claim is created first as an earlier release made it (owner
// label and owner ID); the storage controller adopts it, as it is, when the
// owner's first app asks for a home.
func seedHome(t *testing.T, s *session, c client.Client, ownerID string) {
	t.Helper()
	ensureHomeClaim(t, c, ownerID)
	runHomeJob(t, s, c, ownerID, "sh", "-c", `mkdir -p "$(dirname "$1")" && printf '%s' "$2" > "$1"`, "seed", "/home/user/"+seedFile, helloPy)
}

// ensureHomeClaim creates ownerID's home claim if it is missing, on the
// default StorageClass.
func ensureHomeClaim(t *testing.T, c client.Client, ownerID string) {
	t.Helper()
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: naming.HomeClaimName(ownerID), Namespace: appsNS,
			Labels:      map[string]string{model.LabelOwner: ownerID},
			Annotations: map[string]string{model.AnnotationOwnerID: ownerID},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}},
		},
	}
	if err := c.Create(context.Background(), claim); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("home claim: %v", err)
	}
}

// uids caches each owner's uid once a pod of theirs has shown it.
var (
	uidsMu sync.Mutex
	uids   = map[string]int64{}
)

// ownerUID is the uid ownerID's apps and runs use: the run-as user of an app
// or job pod of theirs (not the file picker, which runs as a fixed uid). The
// uid lives in the database, so with no such pod yet the sample job is run
// once through the gateway (s is signed in as ownerID) and deleted.
func ownerUID(t *testing.T, s *session, c client.Client, ownerID string) int64 {
	t.Helper()
	uidsMu.Lock()
	uid, ok := uids[ownerID]
	uidsMu.Unlock()
	if ok {
		return uid
	}
	find := func() (int64, bool) {
		var pods corev1.PodList
		if err := c.List(context.Background(), &pods, client.InNamespace(appsNS), client.MatchingLabels{model.LabelOwner: ownerID}); err != nil {
			t.Fatal(err)
		}
		for _, p := range pods.Items {
			if sc := p.Spec.SecurityContext; p.Labels[model.LabelRole] == "" && sc != nil && sc.RunAsUser != nil {
				return *sc.RunAsUser, true
			}
		}
		return 0, false
	}
	uid, ok = find()
	if !ok {
		var job struct{ Name string }
		s.json(http.MethodPost, "/api/job-samples/jobs-python", nil, http.StatusCreated, &job)
		defer s.do(http.MethodDelete, "/api/jobs/"+job.Name, nil)
		for deadline := time.Now().Add(time.Minute); ; time.Sleep(2 * time.Second) {
			resp, data := s.do(http.MethodPost, "/api/jobs/"+job.Name+"/run", map[string]any{})
			if resp.StatusCode == http.StatusCreated {
				break
			}
			if resp.StatusCode != http.StatusServiceUnavailable || time.Now().After(deadline) {
				t.Fatalf("run %s: %d %s", job.Name, resp.StatusCode, data)
			}
		}
		for deadline := time.Now().Add(2 * time.Minute); !ok; time.Sleep(time.Second) {
			if uid, ok = find(); !ok && time.Now().After(deadline) {
				t.Fatalf("no pod of %s shows its uid", ownerID)
			}
		}
	}
	uidsMu.Lock()
	uids[ownerID] = uid
	uidsMu.Unlock()
	return uid
}

// runHomeJob runs argv as ownerID's uid in a one-off Job that mounts their
// home claim at /home/user, and fails the test unless it succeeds.
func runHomeJob(t *testing.T, s *session, c client.Client, ownerID string, argv ...string) {
	t.Helper()
	runClaimJob(t, c, naming.HomeClaimName(ownerID), ownerUID(t, s, c, ownerID), argv...)
}

// runClaimJob runs argv as uid in a one-off Job that mounts the claim at
// /home/user, and fails the test unless it succeeds.
func runClaimJob(t *testing.T, c client.Client, claimName string, uid int64, argv ...string) {
	t.Helper()
	ctx := context.Background()
	podSec, ctrSec := restrictedSecurity(uid)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-home-", Namespace: appsNS},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To[int32](0),
			TTLSecondsAfterFinished: ptr.To[int32](60),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy:                corev1.RestartPolicyNever,
				AutomountServiceAccountToken: ptr.To(false),
				SecurityContext:              podSec,
				Containers: []corev1.Container{{
					Name: "job", Image: helperImage, ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         argv,
					SecurityContext: ctrSec,
					VolumeMounts:    []corev1.VolumeMount{{Name: "home", MountPath: "/home/user"}},
				}},
				Volumes: []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName},
				}}},
			}},
		},
	}
	if err := c.Create(ctx, job); err != nil {
		t.Fatalf("home job: %v", err)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		var j batchv1.Job
		if err := c.Get(ctx, client.ObjectKeyFromObject(job), &j); err != nil {
			t.Fatal(err)
		}
		if j.Status.Succeeded > 0 {
			break
		}
		if j.Status.Failed > 0 || time.Now().After(deadline) {
			t.Fatalf("home job did not succeed: %+v", j.Status)
		}
		time.Sleep(2 * time.Second)
	}
	_ = c.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
}

func TestEndToEnd(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	slug := me.ID
	c := kube(t)
	run := time.Now().Format("150405")

	t.Run("file picker lists the seeded file", func(t *testing.T) {
		s := s.with(t)
		seedHome(t, s, c, me.ID)
		deadline := time.Now().Add(2 * time.Minute)
		for {
			resp, data := s.do(http.MethodGet, "/api/files?path=e2e", nil)
			if resp.StatusCode == http.StatusOK {
				if !strings.Contains(string(data), `"hello.py"`) {
					t.Fatalf("listing lacks hello.py: %s", data)
				}
				return
			}
			// 503 HomeStarting while the file pod starts; anything else is a failure.
			if resp.StatusCode != http.StatusServiceUnavailable || time.Now().After(deadline) {
				t.Fatalf("GET /api/files: %d %s", resp.StatusCode, data)
			}
			time.Sleep(2 * time.Second)
		}
	})

	t.Run("streamlit app runs and is served through the gateway", func(t *testing.T) {
		s := s.with(t)
		streamlit := s.create("e2e streamlit "+run, "streamlit", seedFile)
		start := time.Now()
		got := s.waitFor(streamlit.Name, 3*time.Minute, func(a app) bool {
			if a.Phase == "Failed" {
				t.Fatalf("streamlit failed: %+v", a.Steps)
			}
			return a.Phase == "Running"
		})
		t.Logf("streamlit Running after %s", time.Since(start).Round(time.Second))
		// Every done step carries what the cluster reported about it, so the
		// details remain once the events behind them expire.
		for _, st := range got.Steps {
			ev := strings.Join(st.Evidence, "\n")
			t.Logf("%s: %q", st.Type, ev)
			if len(st.Evidence) == 0 || strings.Contains(ev, "not reported") {
				t.Errorf("step %s recorded %q", st.Type, ev)
			}
		}
		if u, err := url.Parse(got.URL); err != nil || u.Path != "/u/"+slug+"/"+streamlit.Name+"/" {
			t.Fatalf("url %q", got.URL)
		}
		resp, body := s.do(http.MethodGet, got.URL, nil)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<title>Streamlit</title>") {
			t.Fatalf("GET %s: %d %.300s", got.URL, resp.StatusCode, body)
		}
		// The app's health endpoint answers through the proxy too.
		resp, body = s.do(http.MethodGet, got.URL+"_stcore/health", nil)
		if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" {
			t.Fatalf("health: %d %q", resp.StatusCode, body)
		}

		// Nested so the app it probes still exists: the app is deleted when
		// this subtest ends.
		t.Run("network policy keeps other namespaces away from app pods", func(t *testing.T) {
			var pod corev1.Pod
			key := client.ObjectKey{Namespace: appsNS, Name: naming.ObjectName(slug, streamlit.Name)}
			if err := c.Get(context.Background(), key, &pod); err != nil {
				t.Fatal(err)
			}
			target := fmt.Sprintf("http://%s:8501/u/%s/%s/_stcore/health", pod.Status.PodIP, slug, streamlit.Name)
			control := "http://javo-gateway." + gatewayNS + ".svc/healthz"
			out := probe(t, c, control, target)
			t.Logf("probe from namespace default:\n%s", out)
			if !strings.Contains(out, control+" ok") {
				t.Fatalf("control request to the gateway failed, the probe proves nothing:\n%s", out)
			}
			if strings.Contains(out, target+" ok") {
				t.Fatalf("an app pod answered a pod in another namespace: NetworkPolicy is not enforced")
			}
		})
	})

	t.Run("missing image fails with ImagePullFailed", func(t *testing.T) {
		s := s.with(t)
		putTemplate(t, missingTmpl, map[string]any{
			"displayName": "Missing image (e2e)", "description": "An image tag that does not exist.",
			"kind": "jupyterlab", "port": 8888, "command": []string{"jupyter-lab"},
			"image": "quay.io/jupyter/base-notebook:javo-e2e-tag-does-not-exist",
		})
		a := s.create("e2e missing image "+run, missingTmpl, "")
		got := s.waitFor(a.Name, 2*time.Minute, func(a app) bool { return a.Phase == "Failed" })
		var reason string
		for _, st := range got.Steps {
			if st.Status == "False" {
				reason = st.Reason
				break
			}
		}
		if reason != "ImagePullFailed" {
			t.Fatalf("failed step reason %q, want ImagePullFailed: %+v", reason, got.Steps)
		}
	})

	t.Run("jupyterlab runs and its kernel websocket connects through the proxy", func(t *testing.T) {
		s := s.with(t)
		a := s.create("e2e lab "+run, "jupyterlab", "")
		// The first pull of the JupyterLab image is large; allow for it.
		got := s.waitFor(a.Name, 8*time.Minute, func(a app) bool {
			if a.Phase == "Failed" {
				t.Fatalf("jupyterlab failed: %+v", a.Steps)
			}
			return a.Phase == "Running"
		})
		kernelID := startKernel(t, s, got.URL)
		status := websocketStatus(t, s, got.URL+"api/kernels/"+kernelID+"/channels")
		if status != http.StatusSwitchingProtocols {
			t.Fatalf("kernel channels websocket: status %d, want 101", status)
		}
	})
}

// putTemplate creates or replaces template name through the API as a
// platform admin (Keycloak's alice, in /javo-admins) and deletes it when the
// test ends. Without Keycloak sign-in the test is skipped (requireKeycloak).
func putTemplate(t *testing.T, name string, spec map[string]any) {
	t.Helper()
	requireKeycloak(t)
	admin := newSession(t)
	admin.login(keycloakLogin, "alice")
	admin.json(http.MethodPut, "/api/templates/"+name, spec, http.StatusOK, nil)
	t.Cleanup(func() {
		resp, data := admin.do(http.MethodDelete, "/api/templates/"+name, nil)
		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
			t.Errorf("delete template %s: %d %s", name, resp.StatusCode, data)
		}
	})
}

// startKernel starts a kernel through the proxy, the way JupyterLab does:
// it reads the _xsrf cookie Jupyter sets and echoes it in X-XSRFToken.
func startKernel(t *testing.T, s *session, base string) string {
	t.Helper()
	resp, body := s.do(http.MethodGet, base+"lab", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %slab: %d %.200s", base, resp.StatusCode, body)
	}
	u, _ := url.Parse(s.abs(base))
	var xsrf string
	for _, ck := range s.http.Jar.Cookies(u) {
		if ck.Name == "_xsrf" {
			xsrf = ck.Value
		}
	}
	req, _ := http.NewRequest(http.MethodPost, s.abs(base)+"api/kernels", strings.NewReader(`{"name":"python3"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-XSRFToken", xsrf)
	r, err := s.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	data, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("start kernel: %d %s", r.StatusCode, data)
	}
	var k struct{ ID string }
	if err := json.Unmarshal(data, &k); err != nil || k.ID == "" {
		t.Fatalf("kernel answer %s: %v", data, err)
	}
	t.Cleanup(func() {
		req, _ := http.NewRequest(http.MethodDelete, s.abs(base)+"api/kernels/"+k.ID, nil)
		req.Header.Set("X-XSRFToken", xsrf)
		if r, err := s.http.Do(req); err == nil {
			r.Body.Close()
		}
	})
	return k.ID
}

// websocketStatus sends a websocket upgrade for path with the session's
// cookies, an Origin of the app's own host and any extra header lines, and
// returns the HTTP status.
func websocketStatus(t *testing.T, s *session, path string, extra ...string) int {
	t.Helper()
	conn, _, resp := dialWebsocket(t, s, path, extra...)
	conn.Close()
	return resp.StatusCode
}

// dialWebsocket sends the upgrade websocketStatus does and returns the open
// connection, the reader holding what arrived after the answer, and the
// answer; the caller closes the connection.
func dialWebsocket(t *testing.T, s *session, path string, extra ...string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	u, _ := url.Parse(s.abs(path))
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), map[string]string{"https": "443", "http": "80"}[u.Scheme])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialLocalhost(ctx, "tcp", host)
	if err == nil && u.Scheme == "https" {
		tc := tls.Client(conn, &tls.Config{ServerName: u.Hostname()})
		err = tc.HandshakeContext(ctx)
		conn = tc
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	var cookies []string
	for _, ck := range s.http.Jar.Cookies(u) {
		cookies = append(cookies, ck.Name+"="+ck.Value)
	}
	var more string
	for _, h := range extra {
		more += h + "\r\n"
	}
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"+
		"Origin: %s\r\nCookie: %s\r\n%s\r\n", u.RequestURI(), u.Host, u.Scheme+"://"+u.Host, strings.Join(cookies, "; "), more)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, br, resp
}

// probe runs a pod in namespace default that requests each URL and prints
// "<url> ok" or "<url> failed: <error>". It returns the pod's log.
func probe(t *testing.T, c client.Client, urls ...string) string {
	t.Helper()
	ctx := context.Background()
	podSec, ctrSec := restrictedSecurity(1000)
	script := `import sys, urllib.request
for u in sys.argv[1:]:
    try:
        urllib.request.urlopen(u, timeout=5)
        print(u, "ok")
    except Exception as e:
        print(u, "failed:", e)
`
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-netpol-probe-", Namespace: "default"},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false),
			SecurityContext: podSec,
			Containers: []corev1.Container{{
				Name: "probe", Image: helperImage, ImagePullPolicy: corev1.PullIfNotPresent,
				Command: append([]string{"python", "-c", script}, urls...), SecurityContext: ctrSec,
			}},
		},
	}
	if err := c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Delete(context.Background(), pod) }()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var p corev1.Pod
		if err := c.Get(ctx, client.ObjectKeyFromObject(pod), &p); err != nil {
			t.Fatal(err)
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe pod did not finish: %s", p.Status.Phase)
		}
		time.Sleep(2 * time.Second)
	}
	return podLog(t, pod)
}

// podLog reads a pod's log through the API server's pods/log endpoint.
func podLog(t *testing.T, pod *corev1.Pod) string {
	t.Helper()
	cfg, err := config.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	hc, err := rest.HTTPClientFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	u := fmt.Sprintf("%s/api/v1/namespaces/%s/pods/%s/log", strings.TrimRight(cfg.Host, "/"), pod.Namespace, pod.Name)
	resp, err := hc.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pod log: %d %s", resp.StatusCode, data)
	}
	return string(data)
}

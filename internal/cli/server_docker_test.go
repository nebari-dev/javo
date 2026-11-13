//go:build linux || darwin

package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/workload"
)

// dockerImage is busybox 1.37.0 by the digest of its image index, the
// image the Docker executor's own tests run.
const dockerImage = "busybox@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec"

// dockerRun marks what this run of the tests makes on the daemon, which
// other test runs use at the same time: only what carries it is removed.
var dockerRun = strings.ToLower(rand.Text())

const dockerRunLabel = "javo.wiring-test"

// dockerTemplate is an app that serves one page from the test image.
const dockerTemplate = `apiVersion: javo.nebari.dev/v1alpha1
kind: WorkspaceTemplate
metadata:
  name: page
spec:
  displayName: Page
  kind: custom
  image: ` + dockerImage + `
  port: 8080
  command: ["sh", "-c", "mkdir -p /tmp/w && echo page > /tmp/w/index.html && exec httpd -f -p {{.Port}} -h /tmp/w"]
  appLogin: Enabled
  readinessPath: "/"
  resources:
    limits: {cpu: 500m, memory: 64Mi}
`

// dockerDaemon is the socket of a Docker daemon of this machine that can
// run the tests, holding the test image; the test is skipped without one.
func dockerDaemon(t *testing.T) (string, *docker.Client) {
	t.Helper()
	home, _ := os.UserHomeDir()
	sockets := []string{"/var/run/docker.sock", filepath.Join(home, ".colima/default/docker.sock"), filepath.Join(home, ".docker/run/docker.sock")}
	if h, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok {
		sockets = []string{h}
	}
	for _, sock := range sockets {
		c := docker.New(sock)
		c.Labels = map[string]string{dockerRunLabel: dockerRun}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, err := c.Daemon(ctx)
		cancel()
		if errors.Is(err, docker.ErrTooOld) {
			t.Skipf("the Docker daemon at %s is too old: %v", sock, err)
		}
		if err != nil {
			continue
		}
		ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := docker.EnsureImage(ctx, c, dockerImage, false, nil); err != nil {
			t.Skipf("cannot pull %s: %v", dockerImage, err)
		}
		return sock, c
	}
	t.Skip("no Docker daemon answers")
	return "", nil
}

// gatewayContainer starts a container that stands in for the one Javo
// runs in, under a name of this test run, and removes it when the test
// ends.
func gatewayContainer(t *testing.T, c *docker.Client) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	name := "javo-wiring-gateway-" + dockerRun
	spec := workload.Spec{
		Ref:  workload.Ref{Namespace: "wiring-" + dockerRun, Name: "gateway", ID: "gateway-" + dockerRun, Owner: testPerson},
		User: workload.User{UID: 1000, GID: 100},
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("64Mi"), corev1.ResourceCPU: resource.MustParse("500m"),
		}},
	}
	body, err := docker.ContainerBody(spec, workload.Unit{Name: "gateway", Image: dockerImage, Command: []string{"sleep", "86400"}}, docker.Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	body.HostConfig.NetworkMode = "bridge"
	id, err := c.ContainerCreate(ctx, name, body)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := c.ContainerRemove(ctx, id); err != nil {
			t.Errorf("removing the test's gateway container: %v", err)
		}
		left, err := c.ContainerList(ctx, dockerRunLabel+"="+dockerRun)
		if err != nil || len(left) != 0 {
			t.Errorf("this run left containers on the daemon: %v, %v", left, err)
		}
	})
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}
	return name
}

// `javo server` with executor docker starts against a real daemon with no
// cluster and no kubeconfig, finds its own container, signs a person in
// and takes their app.
//
// The app does not reach Running here, for two reasons this test states
// rather than hides. Homes on Docker (a volume per person) are not built
// yet, so the app waits for its home; and an app is only Ready once Javo
// reaches its port, which it does from inside the gateway container, not
// from a test process on the host. When the home provider lands, the
// first assertion below is the one to change.
func TestServerStartsOnDocker(t *testing.T) {
	socket, c := dockerDaemon(t)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "none"))
	gateway := gatewayContainer(t, c)
	dir := t.TempDir()
	db := filepath.Join(dir, "javo.db")
	config := write(t, filepath.Join(dir, "config.yaml"), `auth:
  local:
    enabled: true
urls:
  scheme: http
executor: docker
keysDir: `+filepath.Join(dir, "keys")+`
docker:
  socket: `+socket+`
  gateway: `+gateway+`
`, 0o600)
	templates := filepath.Join(dir, "templates")
	write(t, filepath.Join(templates, "page.yaml"), dockerTemplate, 0o600)
	seedAccount(t, db, testPerson)

	s := startServer(t, "--config", config, "--database-path", db, "--templates-dir", templates,
		"--runner-image", dockerImage, "--workspace-namespace", "wiring-"+dockerRun)
	if code := s.signIn(testPerson); code != http.StatusOK {
		t.Fatalf("sign in: %d", code)
	}
	name := s.createApp("Page", "page")
	waiting := func(a appView) bool {
		for _, step := range a.Steps {
			if step.Type == "HomeReady" && step.Status == "False" {
				return true
			}
		}
		return false
	}
	if app := s.waitApp(name, "waiting for its home", waiting); app.Phase == "Running" {
		t.Fatalf("the app runs without a home: %+v", app)
	}
	// Nothing reads a home on Docker yet: the file routes say so.
	if code := s.call(http.MethodGet, "/api/files", nil, nil); code != http.StatusNotImplemented {
		t.Errorf("list the home folder: %d, want 501", code)
	}
	if code := s.call(http.MethodDelete, "/api/workspaces/"+name, nil, nil); code >= 300 {
		t.Errorf("delete the app: %d", code)
	}
}

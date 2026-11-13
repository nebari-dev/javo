package docker

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/workload"
)

// The tests below run against a real Docker daemon and are skipped when
// none answers. They state what the daemon does, which the translation
// and the isolation rest on. Everything they create carries javo.test=1
// and is removed when the test ends.

// testImage is busybox 1.37.0, by the digest of its image index on Docker
// Hub: small, public, and never a different image under the same name.
const testImage = "busybox@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec"

// testLabel marks what the daemon tests create.
const testLabel = "javo.test"

// testRun marks what this run of the tests creates: an Executor of the
// tests adopts and lists only that, and never what another run going on
// against the same daemon made.
var testRun = strings.ToLower(rand.Text())

// daemon is a client of the Docker daemon on this machine, holding the
// test image.
func daemon(t *testing.T) (*Client, Daemon) {
	t.Helper()
	home, _ := os.UserHomeDir()
	sockets := []string{"/var/run/docker.sock", filepath.Join(home, ".colima/default/docker.sock"), filepath.Join(home, ".docker/run/docker.sock")}
	if h, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok {
		sockets = []string{h}
	}
	for _, sock := range sockets {
		c := New(sock)
		c.Labels = map[string]string{testLabel: "1", testLabel + ".run": testRun}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		d, err := c.Daemon(ctx)
		cancel()
		if errors.Is(err, ErrTooOld) {
			t.Skipf("the Docker daemon at %s is too old: %v", sock, err)
		}
		if err != nil {
			continue
		}
		ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := EnsureImage(ctx, c, testImage, false, nil); err != nil {
			t.Skipf("cannot pull %s: %v", testImage, err)
		}
		t.Cleanup(func() { sweep(t, c) })
		return c, d
	}
	t.Skip("no Docker daemon answers")
	return nil, Daemon{}
}

// sweep removes what this run of the daemon tests created, and only
// that: another run may be going on against the same daemon.
func sweep(t *testing.T, c *Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	mark := testLabel + ".run=" + testRun
	del := func(path string) {
		if err := gone(c.call(ctx, request{method: http.MethodDelete, path: path}, nil)); err != nil {
			t.Errorf("cleanup of %s: %v", path, err)
		}
	}
	containers, err := c.ContainerList(ctx, mark)
	if err != nil {
		t.Errorf("cleanup: %v", err)
	}
	for _, x := range containers {
		del("/containers/" + x.ID + "?force=1&v=1")
	}
	networks, err := c.NetworkList(ctx, mark)
	if err != nil {
		t.Errorf("cleanup: %v", err)
	}
	for _, x := range networks {
		del("/networks/" + x.ID)
	}
	volumes, err := c.VolumeList(ctx, mark)
	if err != nil {
		t.Errorf("cleanup: %v", err)
	}
	for _, x := range volumes {
		del("/volumes/" + x.Name)
	}
}

// testSpec is a workload of the daemon tests, called name.
func testSpec(name string) workload.Spec {
	return workload.Spec{
		Ref:  workload.Ref{Namespace: "javotest", Name: name, ID: "id-" + name, Owner: "alice"},
		User: workload.User{UID: 1000, GID: 100},
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("64Mi"), corev1.ResourceCPU: resource.MustParse("500m"),
		}},
	}
}

// sh is a unit that runs a shell script in the test image.
func sh(name, script string, mounts ...workload.Mount) workload.Unit {
	return workload.Unit{Name: name, Image: testImage, Command: []string{"sh", "-c", script}, Mounts: mounts}
}

func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// start creates and starts the container of unit u of s, through the same
// translation every workload gets.
func start(t *testing.T, c *Client, s workload.Spec, u workload.Unit) string {
	t.Helper()
	ctx := testContext(t)
	body, err := ContainerBody(s, u, Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	name, _ := ContainerName(s.Ref, u.Name)
	id, err := c.ContainerCreate(ctx, name, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatalf("start of %s: %v", u.Name, err)
	}
	return id
}

// run runs unit u of s to its end and returns its exit code and what it
// printed.
func run(t *testing.T, c *Client, s workload.Spec, u workload.Unit) (int, string) {
	t.Helper()
	ctx := testContext(t)
	id := start(t, c, s, u)
	code, err := c.ContainerWait(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	out := logs(t, c, id, LogOptions{})
	if err := c.ContainerRemove(ctx, id); err != nil {
		t.Fatal(err)
	}
	return code, out
}

func logs(t *testing.T, c *Client, id string, o LogOptions) string {
	t.Helper()
	rc, err := c.ContainerLogs(testContext(t), id, o)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// gateway starts a container that stands in for Javo's gateway: on the
// daemon's default network, so it reaches the internet, serving "gateway"
// on port 9090 and running script beside that.
func gateway(t *testing.T, c *Client, name, script string) string {
	t.Helper()
	ctx := testContext(t)
	s := testSpec(name)
	body, err := ContainerBody(s, sh("gateway", "mkdir /tmp/w; echo gateway > /tmp/w/index.html; httpd -p 9090 -h /tmp/w; "+script+"; sleep 3600"), Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	body.HostConfig.NetworkMode = "bridge"
	id, err := c.ContainerCreate(ctx, "javo-javotest."+name+".gateway", body)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// sandbox isolates s and starts its sandbox.
func sandbox(t *testing.T, c *Client, s workload.Spec, gatewayID string) string {
	t.Helper()
	ctx := testContext(t)
	if err := Isolate(ctx, c, s.Ref, gatewayID, testPool); err != nil {
		t.Fatal(err)
	}
	body, err := SandboxBody(s, Tool{Image: testImage, Command: []string{"sleep", "3600"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	name, _ := SandboxName(s.Ref)
	id, err := c.ContainerCreate(ctx, name, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// up gives workload s a gateway and a running sandbox, which every unit
// needs: a unit has no network of its own. It returns the gateway's id.
func up(t *testing.T, c *Client, s workload.Spec) string {
	t.Helper()
	gw := gateway(t, c, s.Ref.Name+"-gw", "true")
	sandbox(t, c, s, gw)
	return gw
}

// results reads the "name=value" lines a probe script printed.
func results(out string) map[string]string {
	got := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}
	return got
}

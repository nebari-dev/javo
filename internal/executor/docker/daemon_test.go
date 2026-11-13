//go:build e2e

package docker

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/executor/docker/testrun"
	"github.com/nebari-dev/javo/internal/workload"
)

// The tests below run against a real Docker daemon and are skipped when
// none answers. They state what the daemon does, which the translation
// and the isolation rest on. Everything they create is stamped, named and
// swept the way every daemon test of the repository is (testrun): with
// this test process's own id, so two test runs against one daemon never
// see, name or remove each other's containers, networks and volumes.

// daemon is a client of the Docker daemon on this machine, holding the
// test image. What it creates carries this test process's id, so an
// Executor of the tests adopts and lists only that, and all of it goes
// when the test ends.
func daemon(t *testing.T) (*Client, Daemon) {
	t.Helper()
	sock := testrun.Socket(t)
	c := New(sock)
	c.Labels = testrun.Stamp()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := c.Daemon(ctx)
	if err != nil {
		t.Skipf("the Docker daemon at %s: %v", sock, err)
	}
	if err := EnsureImage(ctx, c, testImage, false, nil); err != nil {
		t.Skipf("cannot pull %s: %v", testImage, err)
	}
	t.Cleanup(func() { testrun.Sweep(t, sock) })
	return c, d
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
	id, err := c.ContainerCreate(ctx, "javo-"+testNamespace+"."+name+".gateway", body)
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

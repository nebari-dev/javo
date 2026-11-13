package egressproxy

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/executor/docker/dockerlive"
	"github.com/nebari-dev/javo/internal/workload"
)

// daemonPool is where the app's network takes its addresses in this
// test: a range no other test of the repository uses.
var daemonPool = netip.MustParsePrefix("10.213.92.0/24")

// logsOf is what a container printed.
func logsOf(t *testing.T, c *docker.Client, id string) string {
	t.Helper()
	rc, err := c.ContainerLogs(context.Background(), id, docker.LogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	raw, _ := io.ReadAll(rc)
	return string(raw)
}

// lines reads the "url => result" lines fetch printed.
func lines(out string) map[string]string {
	got := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if u, result, ok := strings.Cut(line, " => "); ok {
			got[u] = result
		}
	}
	return got
}

// Against a real daemon: an app on its isolated network, told about the
// proxy by the executor's own translation, fetches a public HTTPS page
// and a public HTTP page through the gateway's proxy, and is refused the
// metadata address, a private address, the Docker host and the gateway.
// Without the proxy it reaches nothing, and a container that is no app
// is not served. Skipped when no daemon answers.
//
// The gateway is a container on the daemon's default network that runs
// the proxy (testdata/proxyd) with the Docker socket; the app runs
// testdata/fetch. Both are built for Linux into the pinned busybox image.
func TestDaemonAnAppReachesTheInternetOnlyThroughTheProxy(t *testing.T) {
	d := dockerlive.New(t)
	c := d.Client
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	image := d.Image(t, map[string]string{
		"/proxyd": "./internal/egressproxy/testdata/proxyd",
		"/fetch":  "./internal/egressproxy/testdata/fetch",
	})

	// The gateway.
	var labels []string
	for k, v := range dockerlive.Stamp() {
		labels = append(labels, k+"="+v)
	}
	var gw struct {
		ID string `json:"Id"`
	}
	if err := d.Call(ctx, http.MethodPost, "/containers/create", nil, map[string]any{
		"Image": image, "Labels": dockerlive.Stamp(),
		"Entrypoint": []string{"/proxyd", "-listen=:3128", "-pool=" + daemonPool.String(), "-labels=" + strings.Join(labels, ",")},
		"HostConfig": map[string]any{"NetworkMode": "bridge", "Binds": []string{"/var/run/docker.sock:/var/run/docker.sock"}},
	}, &gw); err != nil {
		t.Fatal(err)
	}
	if err := c.ContainerStart(ctx, gw.ID); err != nil {
		t.Fatal(err)
	}
	gateway, err := c.ContainerInspect(ctx, gw.ID)
	if err != nil {
		t.Fatal(err)
	}
	gwName := strings.TrimPrefix(gateway.Name, "/")
	gwOnBridge := gateway.IP("bridge")
	bridge, err := c.NetworkInspect(ctx, "bridge")
	if err != nil || len(bridge.IPAM.Config) == 0 || bridge.IPAM.Config[0].Gateway == "" || gwOnBridge == "" {
		t.Fatalf("the default bridge: %+v, %v; the gateway on it: %q", bridge, err, gwOnBridge)
	}
	host := bridge.IPAM.Config[0].Gateway

	// This machine's containers must reach the internet for the test to
	// say anything about it.
	fetch := func(args ...string) []string { return append([]string{"/fetch"}, args...) }
	if code, err := c.Exec(ctx, gw.ID, []string{"wget", "-q", "-T", "10", "-O", "/dev/null", "http://example.com/"}); err != nil || code != 0 {
		t.Skipf("containers on this machine do not reach example.com (wget exit %d, %v)", code, err)
	}

	// The app: a sandbox on a network of its own, and a unit beside it.
	s := workload.Spec{
		Ref:  workload.Ref{Namespace: "javotest", Name: "egress", ID: "id-egress", Owner: "alice"},
		User: workload.User{UID: 1000, GID: 100},
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("128Mi"), corev1.ResourceCPU: resource.MustParse("500m"),
		}},
	}
	if err := docker.Isolate(ctx, c, s.Ref, gw.ID, daemonPool); err != nil {
		t.Fatal(err)
	}
	sandbox, err := docker.SandboxBody(s, docker.Tool{Image: dockerlive.Busybox, Command: []string{"sleep", "3600"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	sandboxName, _ := docker.SandboxName(s.Ref)
	id, err := c.ContainerCreate(ctx, sandboxName, sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}
	run := func(name string, in docker.Inputs, command []string) map[string]string {
		t.Helper()
		// The app tries to switch the proxy off for itself.
		u := workload.Unit{Name: name, Image: image, Command: command, ReadOnlyRoot: true,
			Env: []workload.Env{{Name: "NO_PROXY", Value: "*"}, {Name: "no_proxy", Value: "*"}}}
		body, err := docker.ContainerBody(s, u, in)
		if err != nil {
			t.Fatal(err)
		}
		unit, _ := docker.ContainerName(s.Ref, name)
		id, err := c.ContainerCreate(ctx, unit, body)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.ContainerStart(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, err := c.ContainerWait(ctx, id); err != nil {
			t.Fatal(err)
		}
		out := logsOf(t, c, id)
		t.Logf("%s:\n%s", name, out)
		return lines(out)
	}
	proxied := docker.Inputs{Proxy: "http://" + gwName + ":3128"}

	got := run("proxied", proxied, fetch(
		"https://example.com/", "http://example.com/",
		"http://169.254.169.254/latest/meta-data/", "https://169.254.169.254/",
		"http://10.0.0.1/", "https://10.0.0.1/",
		"http://"+host+"/", "https://"+host+"/",
		"http://"+gwOnBridge+"/", "https://"+gwOnBridge+":3128/",
		"https://example.com:22/",
	))
	for _, u := range []string{"https://example.com/", "http://example.com/"} {
		if !strings.HasPrefix(got[u], "200 ") || !strings.Contains(got[u], "Example Domain") {
			t.Errorf("%s through the proxy: %q, want the page", u, got[u])
		}
	}
	for _, u := range []string{"http://169.254.169.254/latest/meta-data/", "http://10.0.0.1/", "http://" + host + "/", "http://" + gwOnBridge + "/"} {
		if !strings.HasPrefix(got[u], "403 ") {
			t.Errorf("%s: %q, want the proxy's 403", u, got[u])
		}
	}
	for _, u := range []string{"https://169.254.169.254/", "https://10.0.0.1/", "https://" + host + "/", "https://" + gwOnBridge + ":3128/", "https://example.com:22/"} {
		if !strings.Contains(got[u], "Forbidden") {
			t.Errorf("%s: %q, want the tunnel refused", u, got[u])
		}
	}

	// Without the proxy there is no way out at all.
	got = run("direct", docker.Inputs{}, fetch("-direct", "https://example.com/", "http://"+host+"/"))
	for u, result := range got {
		if !strings.HasPrefix(result, "error: ") {
			t.Errorf("%s without the proxy: %q, want no route", u, result)
		}
	}
	if len(got) != 2 {
		t.Errorf("the direct fetch printed %v", got)
	}

	// A container that is no app (here: on the gateway's own network)
	// reaches the port and is hung up on.
	var stranger struct {
		ID string `json:"Id"`
	}
	if err := d.Call(ctx, http.MethodPost, "/containers/create", nil, map[string]any{
		"Image": image, "Labels": dockerlive.Stamp(), "User": "1000:100",
		"Env":        []string{"HTTP_PROXY=http://" + gwOnBridge + ":3128", "HTTPS_PROXY=http://" + gwOnBridge + ":3128"},
		"Entrypoint": fetch("http://example.com/", "https://example.com/"),
		"HostConfig": map[string]any{"NetworkMode": "bridge"},
	}, &stranger); err != nil {
		t.Fatal(err)
	}
	if err := c.ContainerStart(ctx, stranger.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContainerWait(ctx, stranger.ID); err != nil {
		t.Fatal(err)
	}
	out := logsOf(t, c, stranger.ID)
	t.Logf("stranger:\n%s", out)
	got = lines(out)
	for _, u := range []string{"http://example.com/", "https://example.com/"} {
		if !strings.HasPrefix(got[u], "error: ") {
			t.Errorf("%s from a container that is no app: %q, want no answer", u, got[u])
		}
	}

	// The gateway's log names the app, and no path.
	log := logsOf(t, c, gw.ID)
	t.Logf("gateway:\n%s", log)
	if !strings.Contains(log, "app=javotest/egress") || !strings.Contains(log, "not on a workload's network") {
		t.Errorf("the gateway's log does not name the app or the refused peer")
	}
	if strings.Contains(log, "meta-data") {
		t.Errorf("the gateway's log holds a path")
	}
}

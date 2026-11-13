package dockerprobe

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/executor/docker/dockerlive"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/workload"
)

// The tests below run the Docker probe against a real daemon and are
// skipped when none answers. Their image is the pinned busybox with a
// Linux build of javo in it (dockerlive.Image); the gateway is a
// container of that image on the daemon's default network that serves
// "javo netprobe listen" on port 9090.

// dockerPool is where the probe's networks take their addresses in these
// tests: a range no other test of the repository uses.
var dockerPool = netip.MustParsePrefix("10.213.91.0/24")

// dockerProber is a prober of the daemon with a running gateway.
func dockerProber(t *testing.T) (*dockerlive.Daemon, *Prober) {
	t.Helper()
	d := dockerlive.New(t)
	image := d.Image(t, map[string]string{"/javo": "./cmd/javo"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var gw struct {
		ID string `json:"Id"`
	}
	err := d.Call(ctx, http.MethodPost, "/containers/create", nil, map[string]any{
		"Image": image, "User": "65532:65532", "Labels": dockerlive.Stamp(),
		"Entrypoint": []string{"/javo", "netprobe", "listen", "--addr=:9090"},
		"HostConfig": map[string]any{"NetworkMode": "bridge"},
	}, &gw)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Client.ContainerStart(ctx, gw.ID); err != nil {
		t.Fatal(err)
	}
	return d, &Prober{
		Client: d.Client, Gateway: gw.ID, Image: image, Pool: dockerPool,
		ControlPort: 9090, ControlPath: "/", Timeout: 2 * time.Minute,
	}
}

// left are the containers and networks of the probe still on the daemon.
func left(t *testing.T, d *dockerlive.Daemon) []string {
	t.Helper()
	ctx := context.Background()
	var out []string
	containers, err := d.Client.ContainerList(ctx, Label, dockerlive.Mark())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range containers {
		out = append(out, c.Names...)
	}
	networks, err := d.Client.NetworkList(ctx, Label, dockerlive.Mark())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range networks {
		out = append(out, n.Name)
	}
	return out
}

// On the networks the executor gives apps, the dialer reaches the gateway
// and nothing else, and the probe leaves nothing behind.
func TestDockerProbeFindsWorkloadNetworksEnforced(t *testing.T) {
	d, p := dockerProber(t)
	res := p.Probe(context.Background())
	t.Logf("evidence: %v", res.Evidence)
	if res.State != netprobe.Enforced {
		t.Fatalf("state %s, want Enforced: %v", res.State, res.Evidence)
	}
	if got := left(t, d); len(got) != 0 {
		t.Errorf("the probe left %v behind", got)
	}
}

// plainInternal makes the workload's network the way "docker network
// create --internal" does: no route out, but with the host's address on
// the bridge. It stands in for a daemon that ignores the isolated gateway
// mode.
func plainInternal(d *dockerlive.Daemon) func(context.Context, *docker.Client, workload.Ref, string, netip.Prefix) error {
	return func(ctx context.Context, c *docker.Client, ref workload.Ref, gateway string, _ netip.Prefix) error {
		name, err := docker.NetworkName(ref)
		if err != nil {
			return err
		}
		labels := docker.Labels(ref)
		for k, v := range c.Labels {
			labels[k] = v
		}
		if err := d.Call(ctx, http.MethodPost, "/networks/create", nil, map[string]any{
			"Name": name, "Driver": "bridge", "Internal": true, "Labels": labels,
		}, nil); err != nil {
			return err
		}
		return c.NetworkConnect(ctx, name, gateway)
	}
}

// A network that is internal but not isolated lets a container reach the
// Docker host on the bridge's address: the probe says NotEnforced and
// names the host.
func TestDockerProbeFindsAPlainInternalNetworkNotEnforced(t *testing.T) {
	d, p := dockerProber(t)
	p.isolate = plainInternal(d)
	res := p.Probe(context.Background())
	t.Logf("evidence: %v", res.Evidence)
	if res.State != netprobe.NotEnforced {
		t.Fatalf("state %s, want NotEnforced: %v", res.State, res.Evidence)
	}
	if !strings.Contains(strings.Join(res.Evidence, "\n"), "the Docker host") {
		t.Errorf("the evidence does not name the Docker host: %v", res.Evidence)
	}
	if got := left(t, d); len(got) != 0 {
		t.Errorf("the probe left %v behind", got)
	}
}

// A dialer that cannot reach the gateway proves nothing: Unknown.
func TestDockerProbeIsUnknownWithoutItsControl(t *testing.T) {
	d, p := dockerProber(t)
	p.ControlPort = 9091 // nothing listens there
	res := p.Probe(context.Background())
	if res.State != netprobe.Unknown || !strings.Contains(strings.Join(res.Evidence, "\n"), "could not reach the gateway") {
		t.Fatalf("state %s, want Unknown for want of the control: %v", res.State, res.Evidence)
	}
	if got := left(t, d); len(got) != 0 {
		t.Errorf("the probe left %v behind", got)
	}
}

// The probe removes what an earlier probe left, and nothing else: a
// network of Javo's without the probe's label stays.
func TestDockerProbeSweepsOnlyItsOwn(t *testing.T) {
	d, p := dockerProber(t)
	ctx := context.Background()
	stale := p.spec("listen", "stale")
	if err := docker.Isolate(ctx, p.client(), stale.Ref, p.Gateway, dockerPool); err != nil {
		t.Fatal(err)
	}
	if err := p.sandbox(ctx, p.client(), stale); err != nil {
		t.Fatal(err)
	}
	// An app that happens to live in a namespace of the same name.
	other := workload.Ref{Namespace: namespace, Name: "an-app", ID: "x", Owner: "alice"}
	if err := docker.Isolate(ctx, d.Client, other, p.Gateway, dockerPool); err != nil {
		t.Fatal(err)
	}

	if res := p.Probe(ctx); res.State != netprobe.Enforced {
		t.Fatalf("state %s: %v", res.State, res.Evidence)
	}
	if got := left(t, d); len(got) != 0 {
		t.Errorf("the probe left %v behind", got)
	}
	name, _ := docker.NetworkName(other)
	if _, err := d.Client.NetworkInspect(ctx, name); err != nil {
		t.Errorf("the probe removed a network that was not its own: %v", err)
	}
}

// Without a daemon the probe could not run: Unknown, never an error.
func TestDockerProbeIsUnknownWithoutADaemon(t *testing.T) {
	t.Parallel()
	p := &Prober{Client: docker.New("/nonexistent/docker.sock"), Gateway: "gw", Image: "example.com/javo:1", Timeout: 5 * time.Second}
	res := p.Probe(context.Background())
	if res.State != netprobe.Unknown || len(res.Evidence) == 0 {
		t.Fatalf("result %+v, want Unknown with the reason", res)
	}
}

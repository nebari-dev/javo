// Package dockerprobe is the isolation probe on Docker: a netprobe.Probe
// that measures whether a workload's network keeps an app from everything
// but the gateway. Its result is stored and read as the Kubernetes probe's
// is. (It is a package of its own because netprobe is imported by what
// plans workloads, which the docker package's tests import.)
package dockerprobe

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/workload"
)

// Label marks every container and network of the Docker probe, on
// top of Javo's own labels. The probe removes nothing without both.
const Label = "javo.netprobe"

const (
	// namespace is the namespace of the probe's two workloads.
	namespace = "javo-netprobe"
	// hostPort is the port the dialer tries on each address of the Docker
	// host. A refusal counts as reached, so which port it is matters only
	// on a host whose firewall drops what it does not serve.
	hostPort = "22"
	// metadata is the address clouds serve a machine's credentials at.
	metadata = "169.254.169.254:80"
	// listenPort is where the listener serves, and dialTimeout how long
	// the dialer waits for each of its requests.
	listenPort  = 8080
	dialTimeout = 3 * time.Second
)

// Prober is the probe on Docker: it finds out whether a workload's
// network (docker.Isolate) keeps an app from everything but the gateway,
// by measuring it rather than by reading the network's options back.
//
// It runs two workloads the way the executor runs an app, each a sandbox
// on a network of its own with the gateway attached: one serves (the
// listener), one connects (the dialer). The result is Enforced only when
// the dialer reaches the gateway (the control) and none of: the listener
// on the other network, the Docker host at any bridge address, a private
// address (the gateway's own on the listener's network), the cloud
// metadata address, a public address, and the name of a public host.
// Reaching any of them is NotEnforced, with the checks that failed as
// evidence; a probe that could not run is Unknown.
type Prober struct {
	// Client talks to the Docker daemon.
	Client *docker.Client
	// Gateway is the name or id of the container Javo's gateway runs in,
	// as the executor has it.
	Gateway string
	// Image is the javo image both workloads run.
	Image string
	// Pool is where the two networks take their addresses; the zero Pool
	// is the executor's default.
	Pool netip.Prefix
	// Runtime is the container runtime the executor uses; empty is the
	// daemon's own.
	Runtime string
	// ControlPort and ControlPath are the gateway's health endpoint, which
	// the dialer must reach at the gateway container's name. It proves
	// that a failure to reach the rest is the network and not a broken
	// dialer.
	ControlPort int
	ControlPath string
	// Public is a public address ("host:port") and PublicName the name of
	// a public host; empty is 1.1.1.1:443 and example.com.
	Public, PublicName string
	// Timeout bounds the whole probe.
	Timeout time.Duration

	// isolate makes a workload's network; nil is docker.Isolate. Only a
	// test replaces it, with a network that is not isolated.
	isolate func(ctx context.Context, c *docker.Client, ref workload.Ref, gateway string, pool netip.Prefix) error
	poll    time.Duration // pace of waiting for the listener; zero is 300ms
}

var _ netprobe.Probe = (*Prober)(nil)

// Probe runs the probe and returns its result. It never fails: whatever
// goes wrong is an Unknown result with the reason as evidence. What it
// made is removed before it returns.
func (p *Prober) Probe(ctx context.Context) netprobe.Result {
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	res := netprobe.Result{State: netprobe.Unknown, CheckedAt: time.Now().UTC()}
	plan, err := p.measure(ctx)
	if err != nil {
		res.Evidence = []string{err.Error()}
		return res
	}
	res.State, res.Evidence = plan.verdict()
	return res
}

// client is the prober's own view of the daemon: everything made through
// it carries Label.
func (p *Prober) client() *docker.Client {
	c := *p.Client
	c.Labels = maps.Clone(p.Client.Labels)
	if c.Labels == nil {
		c.Labels = map[string]string{}
	}
	c.Labels[Label] = "true"
	return &c
}

// spec is one of the probe's two workloads.
func (p *Prober) spec(role, suffix string) workload.Spec {
	return workload.Spec{
		Ref:  workload.Ref{Namespace: namespace, Name: role + "-" + suffix, ID: suffix, Owner: "javo"},
		User: workload.User{UID: 65532, GID: 65532},
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("128Mi"), corev1.ResourceCPU: resource.MustParse("500m"),
		}},
	}
}

// unit runs javo with args, on a file system it cannot write.
func (p *Prober) unit(name string, args ...string) workload.Unit {
	return workload.Unit{Name: name, Image: p.Image, Command: append([]string{"/javo", "netprobe"}, args...), ReadOnlyRoot: true}
}

// measurement is what one probe asked its dialer to try and what the
// dialer saw.
type measurement struct {
	control string
	// forbidden names each address the dialer must not reach.
	forbidden  map[string]string
	publicName string
	out        netprobe.Outcome
}

// measure makes the two workloads, runs the dialer and removes it all.
func (p *Prober) measure(ctx context.Context) (*measurement, error) {
	c := p.client()
	isolate := p.isolate
	if isolate == nil {
		isolate = docker.Isolate
	}
	p.sweep(c)
	suffix := strings.ToLower(rand.Text())[:8]
	listener, dialer := p.spec("listen", suffix), p.spec("dial", suffix)
	defer p.remove(c, listener.Ref, dialer.Ref)

	if err := docker.EnsureImage(ctx, c, p.Image, false, nil); err != nil {
		return nil, fmt.Errorf("the probe's image %s: %w", p.Image, err)
	}
	gw, err := c.ContainerInspect(ctx, p.Gateway)
	if err != nil {
		return nil, fmt.Errorf("the gateway container %q: %w", p.Gateway, err)
	}
	for _, s := range []workload.Spec{listener, dialer} {
		if err := isolate(ctx, c, s.Ref, gw.ID, p.Pool); err != nil {
			return nil, fmt.Errorf("network of the probe's %s: %w", s.Ref.Name, err)
		}
		if err := p.sandbox(ctx, c, s); err != nil {
			return nil, fmt.Errorf("sandbox of the probe's %s: %w", s.Ref.Name, err)
		}
	}

	// The listener, and proof that it serves: a second unit of its own
	// workload reaches it at the address the dialer will be given.
	listenerNet, _ := docker.NetworkName(listener.Ref)
	sandbox, _ := docker.SandboxName(listener.Ref)
	held, err := c.ContainerInspect(ctx, sandbox)
	if err != nil {
		return nil, err
	}
	ip := held.IP(listenerNet)
	if ip == "" {
		return nil, fmt.Errorf("the listener has no address on %s", listenerNet)
	}
	target := "http://" + net.JoinHostPort(ip, strconv.Itoa(listenPort)) + "/"
	serving, err := p.start(ctx, c, listener, p.unit("listen", "listen", fmt.Sprintf("--addr=:%d", listenPort)))
	if err != nil {
		return nil, fmt.Errorf("listener: %w", err)
	}
	if err := p.waitServing(ctx, c, listener, target); err != nil {
		return nil, fmt.Errorf("the listener never served, so its unreachability would prove nothing: %w", err)
	}

	// The gateway is on both networks by now.
	if gw, err = c.ContainerInspect(ctx, gw.ID); err != nil {
		return nil, err
	}
	m := &measurement{
		control:    fmt.Sprintf("http://%s:%d%s", strings.TrimPrefix(gw.Name, "/"), p.ControlPort, p.ControlPath),
		forbidden:  map[string]string{},
		publicName: p.PublicName,
	}
	if m.publicName == "" {
		m.publicName = "example.com"
	}
	public := p.Public
	if public == "" {
		public = "1.1.1.1:443"
	}
	m.forbidden[net.JoinHostPort(ip, strconv.Itoa(listenPort))] = "the listener on another workload's network"
	m.forbidden[metadata] = "the cloud metadata address"
	m.forbidden[public] = "a public address"
	if private := gw.IP(listenerNet); private != "" {
		m.forbidden[net.JoinHostPort(private, strconv.Itoa(p.ControlPort))] = "a private address (the gateway's on another workload's network)"
	}
	hosts, err := p.hostAddresses(ctx, c, dialer.Ref)
	if err != nil {
		return nil, err
	}
	for _, h := range hosts {
		m.forbidden[net.JoinHostPort(h, hostPort)] = "the Docker host"
	}

	args := []string{"dial", "--control=" + m.control, "--target=" + target, "--timeout=" + dialTimeout.String(),
		"--message-file=/dev/null", "--resolve=" + m.publicName}
	for _, addr := range slices.Sorted(maps.Keys(m.forbidden)) {
		args = append(args, "--tcp="+addr)
	}
	if m.out, err = p.run(ctx, c, dialer, p.unit("dial", args...)); err != nil {
		return nil, fmt.Errorf("dialer: %w", err)
	}
	// Not reaching the listener proves something only if it served the
	// whole time. The daemon never restarts it, so running is enough.
	if still, err := c.ContainerInspect(ctx, serving); err != nil || !still.State.Running {
		return nil, fmt.Errorf("the listener was not serving when the dialer ran, so its unreachability proves nothing (%v)", err)
	}
	return m, nil
}

// verdict reads the dialer's outcome.
func (m *measurement) verdict() (netprobe.State, []string) {
	if !m.out.Control {
		return netprobe.Unknown, []string{fmt.Sprintf("the dialer could not reach the gateway at %s, so its failure to reach anything else proves nothing", m.control)}
	}
	var reached []string
	if m.out.Target {
		reached = append(reached, "the dialer fetched a page from the listener on another workload's network")
	}
	for _, addr := range slices.Sorted(maps.Keys(m.forbidden)) {
		how, tried := m.out.TCP[addr]
		if !tried {
			return netprobe.Unknown, []string{fmt.Sprintf("the dialer did not say how %s answered", addr)}
		}
		if netprobe.Reached(how) {
			reached = append(reached, fmt.Sprintf("the dialer reached %s at %s (%s)", m.forbidden[addr], addr, how))
		}
	}
	if slices.Contains(m.out.Resolved, m.publicName) {
		reached = append(reached, fmt.Sprintf("the dialer resolved the outside name %s", m.publicName))
	}
	if len(reached) > 0 {
		return netprobe.NotEnforced, reached
	}
	return netprobe.Enforced, []string{fmt.Sprintf("a container on a workload's network reached the gateway and none of %d other addresses: not another workload, the Docker host, a private address, the metadata address or a public one, and no outside name resolved", len(m.forbidden))}
}

// hostAddresses are the addresses the Docker host could be reached at
// from the dialer: the host's address on every bridge of the daemon, and
// the first address of the dialer's own network, where the daemon puts
// the host on a network that is not isolated. Addresses of containers on
// the dialer's network are left out: they are not the host.
func (p *Prober) hostAddresses(ctx context.Context, c *docker.Client, dialer workload.Ref) ([]string, error) {
	all, err := c.NetworkList(ctx)
	if err != nil {
		return nil, err
	}
	name, _ := docker.NetworkName(dialer)
	own, err := c.NetworkInspect(ctx, name)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, member := range own.Containers {
		if prefix, err := netip.ParsePrefix(member.IPv4Address); err == nil {
			seen[prefix.Addr().String()] = true
		}
	}
	var out []string
	add := func(addr string) {
		if a, err := netip.ParseAddr(addr); err == nil && !seen[a.String()] {
			seen[a.String()] = true
			out = append(out, a.String())
		}
	}
	for _, cfg := range own.IPAM.Config {
		if prefix, err := netip.ParsePrefix(cfg.Subnet); err == nil {
			add(prefix.Masked().Addr().Next().String())
		}
	}
	for _, n := range all {
		for _, cfg := range n.IPAM.Config {
			add(cfg.Gateway)
		}
	}
	return out, nil
}

// sandbox starts the container that holds s's place on its network.
func (p *Prober) sandbox(ctx context.Context, c *docker.Client, s workload.Spec) error {
	body, err := docker.SandboxBody(s, docker.Tool{Image: p.Image, Command: []string{"/javo", "pause"}}, p.Runtime)
	if err != nil {
		return err
	}
	name, _ := docker.SandboxName(s.Ref)
	id, err := c.ContainerCreate(ctx, name, body)
	if err != nil {
		return err
	}
	return c.ContainerStart(ctx, id)
}

// start runs unit u of s, with the confinement every unit of an app gets,
// and returns its container's id. An earlier container of u is removed.
func (p *Prober) start(ctx context.Context, c *docker.Client, s workload.Spec, u workload.Unit) (string, error) {
	body, err := docker.ContainerBody(s, u, docker.Inputs{Runtime: p.Runtime})
	if err != nil {
		return "", err
	}
	name, _ := docker.ContainerName(s.Ref, u.Name)
	if err := c.ContainerRemove(ctx, name); err != nil {
		return "", err
	}
	id, err := c.ContainerCreate(ctx, name, body)
	if err != nil {
		return "", err
	}
	return id, c.ContainerStart(ctx, id)
}

// run runs a dial unit of s to its end and reads the outcome it printed.
func (p *Prober) run(ctx context.Context, c *docker.Client, s workload.Spec, u workload.Unit) (netprobe.Outcome, error) {
	id, err := p.start(ctx, c, s, u)
	if err != nil {
		return netprobe.Outcome{}, err
	}
	if _, err := c.ContainerWait(ctx, id); err != nil {
		return netprobe.Outcome{}, err
	}
	logs, err := c.ContainerLogs(ctx, id, docker.LogOptions{})
	if err != nil {
		return netprobe.Outcome{}, err
	}
	defer logs.Close()
	// The outcome is the last line that is JSON.
	var out netprobe.Outcome
	found := false
	lines := bufio.NewScanner(logs)
	lines.Buffer(nil, 1<<20)
	for lines.Scan() {
		var o netprobe.Outcome
		if json.Unmarshal(lines.Bytes(), &o) == nil {
			out, found = o, true
		}
	}
	if err := lines.Err(); err != nil {
		return netprobe.Outcome{}, err
	}
	if !found {
		return netprobe.Outcome{}, errors.New("it printed no outcome")
	}
	return out, nil
}

// waitServing waits until a unit beside the listener, in its sandbox,
// fetches a page from it at url.
func (p *Prober) waitServing(ctx context.Context, c *docker.Client, s workload.Spec, url string) error {
	every := p.poll
	if every <= 0 {
		every = 300 * time.Millisecond
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		out, err := p.run(ctx, c, s, p.unit("check", "dial", "--control="+url, "--target="+url, "--timeout="+dialTimeout.String(), "--message-file=/dev/null"))
		if err == nil && out.Control {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s (%v)", p.Timeout, err)
		case <-t.C:
		}
	}
}

// remove tears the probe's workloads down on a fresh context, so a
// cancelled probe still cleans up.
func (p *Prober) remove(c *docker.Client, refs ...workload.Ref) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, ref := range refs {
		p.teardown(ctx, c, ref)
	}
}

// mine is the label filter for what this prober made: Javo's label, the
// probe's, and the labels of its client.
func mine(c *docker.Client, more ...string) []string {
	filter := append([]string{docker.Managed, docker.LabelNamespace + "=" + namespace}, more...)
	for k, v := range c.Labels {
		filter = append(filter, k+"="+v)
	}
	return filter
}

// teardown removes the containers and the network of one of the probe's
// workloads, the sandbox last since the units live in its network. Only
// what carries the labels of mine is removed.
func (p *Prober) teardown(ctx context.Context, c *docker.Client, ref workload.Ref) {
	containers, _ := c.ContainerList(ctx, mine(c, docker.LabelName+"="+ref.Name)...)
	var sandboxes []string
	for _, x := range containers {
		if x.Labels[docker.LabelUnit] == docker.Sandbox {
			sandboxes = append(sandboxes, x.ID)
			continue
		}
		_ = c.ContainerRemove(ctx, x.ID)
	}
	for _, id := range sandboxes {
		_ = c.ContainerRemove(ctx, id)
	}
	networks, _ := c.NetworkList(ctx, mine(c, docker.LabelName+"="+ref.Name)...)
	for _, n := range networks {
		_ = c.NetworkDisconnect(ctx, n.ID, p.Gateway)
		_ = c.NetworkRemove(ctx, n.ID)
	}
}

// sweep removes what an earlier probe left behind (Javo was stopped
// part-way). Nothing but what mine finds is looked at.
func (p *Prober) sweep(c *docker.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	left := map[workload.Ref]bool{}
	if containers, err := c.ContainerList(ctx, mine(c)...); err == nil {
		for _, x := range containers {
			left[docker.RefOf(x.Labels)] = true
		}
	}
	if networks, err := c.NetworkList(ctx, mine(c)...); err == nil {
		for _, x := range networks {
			left[docker.RefOf(x.Labels)] = true
		}
	}
	for ref := range left {
		p.teardown(ctx, c, ref)
	}
}

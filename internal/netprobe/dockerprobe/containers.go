package dockerprobe

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/workload"
)

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

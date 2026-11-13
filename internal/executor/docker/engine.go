package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/supervise"
	"github.com/nebari-dev/javo/internal/workload"
)

// labelSpec is the label of a sandbox that holds the Spec its workload
// was started from, as JSON, so that an executor made later can keep the
// workload running (Adopt). A Spec holds no secret value.
const labelSpec = "javo.spec"

// engine runs the units of workloads as containers. It is the only
// caller of ContainerCreate outside the volume setup, and every body it
// sends comes from SandboxBody or ContainerBody.
type engine struct {
	c       *Client
	secrets executor.Secrets
	// gateway is the container Javo's gateway runs in.
	gateway string
	// pause is the program of a sandbox; its image is also what volumes
	// are prepared through.
	pause   Tool
	pool    netip.Prefix
	runtime string
	// proxy is the gateway's forward proxy as workloads reach it.
	proxy  string
	logins func(name string) []RegistryAuth
	grace  time.Duration
	// runs is set for the engine of job runs: c then marks what it makes
	// with LabelRun, and finds only that.
	runs bool
	// ctx ends when the executor is closed: nothing waits for a container
	// after that.
	ctx context.Context
}

var (
	_ supervise.Engine   = (*engine)(nil)
	_ supervise.Grounded = (*engine)(nil)
)

// image makes the daemon hold image, for a unit of s.
func (e *engine) image(ctx context.Context, s workload.Spec, image string, always bool) error {
	return EnsureImage(ctx, e.c, image, always, PullAuth(s, image, e.logins))
}

// Ground makes what the units of s stand on: the workload's network with
// the gateway on it, its volumes, and its sandbox, running, with the
// secret files in it. Containers an earlier sandbox of s left are removed
// first: they live in a network namespace nothing reaches any more.
func (e *engine) Ground(ctx context.Context, s workload.Spec) (supervise.Unit, error) {
	if err := e.image(ctx, s, e.pause.Image, false); err != nil {
		return nil, err
	}
	if err := Isolate(ctx, e.c, s.Ref, e.gateway, e.pool); err != nil {
		return nil, err
	}
	if err := e.removeContainers(ctx, s.Ref); err != nil {
		return nil, err
	}
	if err := Prepare(ctx, e.c, s, e.pause.Image); err != nil {
		return nil, err
	}
	body, err := SandboxBody(s, e.pause, e.runtime)
	if err != nil {
		return nil, err
	}
	spec, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	body.Labels[labelSpec] = string(spec)
	name, err := SandboxName(s.Ref)
	if err != nil {
		return nil, err
	}
	id, err := e.c.ContainerCreate(ctx, name, body)
	if err != nil {
		return nil, err
	}
	if err := e.c.ContainerStart(ctx, id); err != nil {
		return nil, err
	}
	for _, v := range s.Volumes {
		if v.Secret == nil {
			continue
		}
		b, err := e.secrets.Get(ctx, s.Ref, v.Secret.Bundle)
		if err != nil {
			return nil, err
		}
		var files map[string][]byte
		if b != nil {
			files = b.Data
		}
		if err := SecretFiles(ctx, e.c, s, v, files); err != nil {
			return nil, err
		}
	}
	return &unit{e: e, id: id}, nil
}

// removeContainers removes every container of the workload ref but the
// one that holds a run's record. Each is checked for Javo's label before
// it goes (ContainerRemove).
func (e *engine) removeContainers(ctx context.Context, ref workload.Ref) error {
	all, err := e.c.ContainerList(ctx, Selector(ref)...)
	if err != nil {
		return err
	}
	var errs []error
	for _, have := range all {
		if have.Labels[LabelUnit] == Record {
			continue
		}
		errs = append(errs, e.c.ContainerRemove(ctx, have.ID))
	}
	return errors.Join(errs...)
}

// Start runs unit u of s as a container in the workload's sandbox. The
// container of the unit's last run, kept until now for its output, is
// removed first.
func (e *engine) Start(ctx context.Context, s workload.Spec, u workload.Unit) (supervise.Unit, error) {
	if err := e.image(ctx, s, u.Image, u.PullAlways); err != nil {
		return nil, &supervise.ImageError{Err: err}
	}
	values, err := e.values(ctx, s.Ref, u)
	if err != nil {
		return nil, err
	}
	body, err := ContainerBody(s, u, Inputs{
		Secret:  func(r workload.SecretRef) (string, bool) { v, ok := values[r]; return v, ok },
		RunID:   supervise.RunID(s),
		Runtime: e.runtime,
		Proxy:   e.proxy,
	})
	if err != nil {
		return nil, err
	}
	name, err := ContainerName(s.Ref, u.Name)
	if err != nil {
		return nil, err
	}
	if err := e.c.ContainerRemove(ctx, name); err != nil {
		return nil, err
	}
	id, err := e.c.ContainerCreate(ctx, name, body)
	if err != nil {
		return nil, redact(err, values)
	}
	if err := e.c.ContainerStart(ctx, id); err != nil {
		return nil, redact(err, values)
	}
	return &unit{e: e, id: id}, nil
}

// values are the values of bundles that u's variables name. One the
// bundle does not hold is left out, and ContainerBody then says which.
func (e *engine) values(ctx context.Context, ref workload.Ref, u workload.Unit) (map[workload.SecretRef]string, error) {
	out := map[workload.SecretRef]string{}
	bundles := map[string]*executor.Bundle{}
	for _, v := range u.Env {
		if v.From == nil {
			continue
		}
		b, ok := bundles[v.From.Bundle]
		if !ok {
			var err error
			if b, err = e.secrets.Get(ctx, ref, v.From.Bundle); err != nil {
				return nil, err
			}
			bundles[v.From.Bundle] = b
		}
		if b == nil {
			continue
		}
		if data, ok := b.Data[v.From.Key]; ok {
			out[*v.From] = string(data)
		}
	}
	return out, nil
}

// redact is err without any of values in its text. The daemon is not
// known to repeat a variable in an answer; if it ever does, the value
// still reaches no state and no log.
func redact(err error, values map[workload.SecretRef]string) error {
	text, clean := err.Error(), true
	for _, v := range values {
		if v != "" && strings.Contains(text, v) {
			text, clean = strings.ReplaceAll(text, v, "[secret]"), false
		}
	}
	if clean {
		return err
	}
	return errors.New(text)
}

// Addr is the workload's port at its sandbox's name, which the daemon
// resolves for the gateway container, the only other container on the
// workload's network. It needs nothing to exist, so it is known before
// the workload runs and is the same after every restart.
func (e *engine) Addr(_ context.Context, ref workload.Ref, port int32) (string, error) {
	name, err := SandboxName(ref)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(name, strconv.Itoa(int(port))), nil
}

// Logs streams what the container of a unit printed. Only a unit's
// current or last run is kept: the run before is removed when the unit
// starts again, so there is nothing to read for o.Previous.
func (e *engine) Logs(ctx context.Context, ref workload.Ref, o executor.LogOptions) (io.ReadCloser, error) {
	if o.Previous {
		return nil, errors.New("docker: the output of a unit's earlier run is not kept")
	}
	unitName := o.Unit
	if unitName == "" {
		unitName = workload.MainUnit
	}
	name, err := ContainerName(ref, unitName)
	if err != nil {
		return nil, err
	}
	have, err := e.c.ContainerInspect(ctx, name)
	if err != nil {
		return nil, err
	}
	if !Ours(have.Config.Labels) {
		return nil, foreign("container", name)
	}
	rc, err := e.c.ContainerLogs(ctx, have.ID, LogOptions{Follow: o.Follow, Tail: o.Tail, Since: o.Since, Timestamps: o.Timestamps})
	if err != nil || o.MaxBytes <= 0 {
		return rc, err
	}
	return struct {
		io.Reader
		io.Closer
	}{io.LimitReader(rc, o.MaxBytes), rc}, nil
}

// unit is one running container.
type unit struct {
	e  *engine
	id string
}

var _ supervise.Execer = (*unit)(nil)

// lost is the exit code of a container that was removed behind the
// executor's back, or that the executor stopped waiting for.
const lost = -1

// Wait returns the container's exit code. A daemon that does not answer
// is asked again: the container may well run on.
func (u *unit) Wait() int {
	for {
		code, err := u.e.c.ContainerWait(u.e.ctx, u.id)
		switch {
		case err == nil:
			return code
		case errors.Is(err, ErrNotFound) || u.e.ctx.Err() != nil:
			return lost
		}
		select {
		case <-u.e.ctx.Done():
			return lost
		case <-time.After(time.Second):
		}
	}
}

// Stop asks the container to end and kills it after the grace period. It
// stays, exited, for its output, until its unit starts again or the
// workload is torn down.
func (u *unit) Stop() {
	ctx, cancel := context.WithTimeout(u.e.ctx, u.e.grace+30*time.Second)
	defer cancel()
	_ = u.e.c.ContainerStop(ctx, u.id, u.e.grace)
}

// Exec runs a readiness check inside the container.
func (u *unit) Exec(ctx context.Context, command []string) (int, error) {
	return u.e.c.Exec(ctx, u.id, command)
}

// Adopt finds the workloads whose containers an earlier executor left
// running: a sandbox that holds the Spec, with the main unit and every
// step that stays running in it. Anything less (a workload cut short
// while it started or stopped, a main unit that has exited) is torn
// down, for the caller to start again whole.
func (e *engine) Adopt(ctx context.Context) ([]supervise.Found, error) {
	all, err := e.c.ContainerList(ctx, e.c.mine(Managed)...)
	if err != nil {
		return nil, err
	}
	type group struct {
		ref   workload.Ref
		units map[string]ContainerSummary
	}
	groups := map[string]*group{}
	var order []string
	for _, have := range all {
		if isRun(have.Labels) {
			continue
		}
		ref := RefOf(have.Labels)
		k := ref.Namespace + "/" + ref.Name
		if groups[k] == nil {
			groups[k] = &group{ref: ref, units: map[string]ContainerSummary{}}
			order = append(order, k)
		}
		groups[k].units[have.Labels[LabelUnit]] = have
	}
	var out []supervise.Found
	for _, k := range order {
		g := groups[k]
		f, ok, err := e.found(ctx, g.units)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, f)
			continue
		}
		if err := Teardown(ctx, e.c, g.ref, e.gateway); err != nil {
			return nil, fmt.Errorf("docker: what is left of workload %s/%s: %w", g.ref.Namespace, g.ref.Name, err)
		}
	}
	return out, nil
}

// found reads a workload off its containers, by unit; false when it does
// not run whole.
func (e *engine) found(ctx context.Context, units map[string]ContainerSummary) (supervise.Found, bool, error) {
	running := func(name string) (Container, bool, error) {
		have, ok := units[name]
		if !ok || have.State != "running" {
			return Container{}, false, nil
		}
		c, err := e.c.ContainerInspect(ctx, have.ID)
		if errors.Is(err, ErrNotFound) {
			return Container{}, false, nil
		}
		return c, err == nil && c.State.Running, err
	}
	box, ok, err := running(Sandbox)
	if err != nil || !ok {
		return supervise.Found{}, false, err
	}
	var s workload.Spec
	if json.Unmarshal([]byte(box.Config.Labels[labelSpec]), &s) != nil || s.Ref != RefOf(box.Config.Labels) {
		return supervise.Found{}, false, nil
	}
	f := supervise.Found{Spec: s, Created: box.Created, Ground: &unit{e: e, id: box.ID}, Units: map[string]supervise.Unit{}}
	stays := []workload.Unit{s.Main}
	for _, u := range s.Steps {
		if u.Stays {
			stays = append(stays, u)
		}
	}
	for _, u := range stays {
		c, ok, err := running(u.Name)
		if err != nil || !ok {
			return supervise.Found{}, false, err
		}
		f.Units[u.Name] = &unit{e: e, id: c.ID}
		if u.Name == s.Main.Name {
			f.MainSince = c.State.StartedAt
		}
	}
	return f, true, nil
}

package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/supervise"
	"github.com/nebari-dev/javo/internal/workload"
)

const (
	// labelRecord is the label of a run's record container that holds
	// what is kept of the run (supervise.Keeper), as JSON. It holds the
	// run's Spec and no secret value.
	labelRecord = "javo.record"

	// messageVolume is the volume of a run's own that its main unit
	// leaves its last message in, and messageDir where that unit sees it.
	messageVolume = "javo-message"
	messageDir    = "/run/javo/message"
)

// Runs runs job runs (workload.Batch) as containers on the Docker host
// its Executor runs apps on: each run is a sandbox on a network of its
// own and a container per unit, made by the same code, from the same
// bodies, as an app's. It is an executor.Executor.
//
// Stop ends a run that is going: its containers stay, stopped, and the
// run stays to be observed. A run that has ended it lets go of, and all
// that was made for the run is removed.
//
// A run's containers outlive Javo, and so does what is kept of the run:
// Close leaves all of it, and the next Runs on the same daemon goes on
// waiting for a run that is going and still shows one that is done.
type Runs struct {
	*supervise.Supervisor
	x   *Executor
	eng *engine
}

var _ executor.Executor = (*Runs)(nil)

// Runs is the executor for job runs on the daemon x runs apps on, with
// x's gateway, pool, runtime and secrets. It adopts the runs an earlier
// one left there. Close it before x.
func (x *Executor) Runs(ctx context.Context) (*Runs, error) {
	eng := *x.eng
	eng.c, eng.runs = x.eng.c.with(LabelRun, "true"), true
	r := &Runs{
		Supervisor: supervise.New(supervise.Config{Engine: runEngine{&eng}, Every: x.in.every, Dial: x.in.dial}),
		x:          x, eng: &eng,
	}
	if err := r.Supervisor.Adopt(ctx); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// Start runs the run s. It refuses a Spec that cannot be run confined,
// as the Executor does for an app, and takes the run's network from the
// pool before it returns.
func (r *Runs) Start(ctx context.Context, s workload.Spec) error {
	if s.Batch == nil {
		return fmt.Errorf("docker executor: %s/%s is not a run", s.Ref.Namespace, s.Ref.Name)
	}
	// One network at a time, among apps and runs: they share the pool.
	r.x.starting.Lock()
	defer r.x.starting.Unlock()
	if st, err := r.Observe(ctx, s.Ref); err != nil || st != nil {
		return err
	}
	// A volume only this run's main unit mounts, for its last message.
	s.Volumes = append(slices.Clone(s.Volumes), workload.Volume{Name: messageVolume})
	s.Main.Mounts = append(slices.Clone(s.Main.Mounts), workload.Mount{Volume: messageVolume, Path: messageDir})
	if err := r.eng.confines(s); err != nil {
		return fmt.Errorf("docker executor: %w", err)
	}
	if err := Isolate(ctx, r.eng.c, s.Ref, r.eng.gateway, r.eng.pool); err != nil {
		return fmt.Errorf("docker executor: run %s/%s gets no network: %w", s.Ref.Namespace, s.Ref.Name, err)
	}
	if err := r.Supervisor.Start(ctx, s); err != nil {
		return errors.Join(err, Teardown(ctx, r.eng.c, s.Ref, r.eng.gateway))
	}
	return nil
}

// Close ends the executor and stops nothing: runs that are going go on,
// for the next one to adopt.
func (r *Runs) Close() { r.Supervisor.Leave() }

// List names every run that has something on the daemon or is being
// started.
func (r *Runs) List(ctx context.Context) ([]workload.Ref, error) {
	return r.eng.list(ctx, r.Supervisor)
}

// Watch reports the run of each change the supervisor makes or the
// daemon reports for a container of a run.
func (r *Runs) Watch(ctx context.Context, changed func(workload.Ref)) error {
	return r.eng.watch(ctx, r.Supervisor, changed)
}

// Addr is an error: a run serves nothing.
func (r *Runs) Addr(_ context.Context, ref workload.Ref, _ int32) (string, error) {
	return "", fmt.Errorf("run %s/%s has no address", ref.Namespace, ref.Name)
}

// Logs streams what a unit of the run's newest attempt printed, or
// returns nil while that unit has no container.
func (r *Runs) Logs(ctx context.Context, ref workload.Ref, o executor.LogOptions) (io.ReadCloser, error) {
	rc, err := r.Supervisor.Logs(ctx, ref, o)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return rc, err
}

// runEngine is the engine as the supervisor of runs sees it.
type runEngine struct{ *engine }

var _ supervise.Keeper = runEngine{}

// recordName is the name of a record container made at when. A later
// one sorts after an earlier one.
func recordName(ref workload.Ref, when time.Time) (string, error) {
	return sub(ref, "unit", fmt.Sprintf("%s-%020d", Record, when.UnixNano()))
}

// Keep holds record as a label of a container that is never started,
// since nothing else on a Docker host can be written to without running
// something. The new one is made before the ones before it are removed:
// at any moment the run has a record.
func (e runEngine) Keep(ctx context.Context, s workload.Spec, record []byte) error {
	if err := e.image(ctx, s, e.pause.Image, false); err != nil {
		return err
	}
	b, err := confined(s, Record, "")
	if err != nil {
		return err
	}
	b.Image = e.pause.Image
	b.HostConfig.NetworkMode = "none"
	b.HostConfig.Memory, b.HostConfig.MemorySwap = sandboxMemory, sandboxMemory
	b.Labels[labelRecord] = string(record)
	name, err := recordName(s.Ref, time.Now())
	if err != nil {
		return err
	}
	id, err := e.c.ContainerCreate(ctx, name, b)
	if err != nil {
		return err
	}
	old, err := e.c.ContainerList(ctx, append(Selector(s.Ref), LabelUnit+"="+Record)...)
	if err != nil {
		return err
	}
	var errs []error
	for _, have := range old {
		if have.ID != id {
			errs = append(errs, e.c.ContainerRemove(ctx, have.ID))
		}
	}
	return errors.Join(errs...)
}

// Release removes the run's containers, its network, which goes back to
// the pool, and its own volumes. Its bundles and the person's storage
// stay.
func (e runEngine) Release(ctx context.Context, ref workload.Ref) error {
	if _, err := NetworkName(ref); err != nil {
		// Nothing was ever made under such a name.
		return nil
	}
	return Teardown(ctx, e.c, ref, e.gateway)
}

func (e runEngine) MessageDir(workload.Spec) string { return messageDir }

// Message reads the file from the container of the run's main unit,
// which has usually exited by now: the daemon reads it, and nothing runs.
func (e runEngine) Message(ctx context.Context, s workload.Spec, name string) string {
	main, err := ContainerName(s.Ref, s.Main.Name)
	if err != nil {
		return ""
	}
	have, err := e.c.ContainerInspect(ctx, main)
	if err != nil || !Ours(have.Config.Labels) {
		return ""
	}
	raw, _ := e.c.ReadFile(ctx, have.ID, messageDir+"/"+name, supervise.MaxMessage)
	return string(raw)
}

// Adopt finds the runs an earlier executor kept: for each, its newest
// record, its sandbox when that runs, and its units' containers that run
// or have exited. What carries a run's labels and has no record is torn
// down: nothing is known of it.
func (e runEngine) Adopt(ctx context.Context) ([]supervise.Found, error) {
	all, err := e.c.ContainerList(ctx, e.c.mine(Managed)...)
	if err != nil {
		return nil, err
	}
	type run struct {
		ref    workload.Ref
		record ContainerSummary
		found  supervise.Found
	}
	runs := map[string]*run{}
	var order []string
	for _, have := range all {
		ref := RefOf(have.Labels)
		k := ref.Namespace + "/" + ref.Name
		if runs[k] == nil {
			runs[k] = &run{ref: ref, found: supervise.Found{Units: map[string]supervise.Unit{}}}
			order = append(order, k)
		}
		r, u := runs[k], &unit{e: e.engine, id: have.ID}
		switch name := have.Labels[LabelUnit]; {
		case name == Record:
			if len(r.record.Names) == 0 || slices.Max(have.Names) > slices.Max(r.record.Names) {
				r.record = have
			}
		case name == Sandbox && have.State == "running":
			r.found.Ground = u
		case name == Sandbox || name == Setup:
		case have.State == "running" || have.State == "exited":
			r.found.Units[name] = u
		}
	}
	var out []supervise.Found
	for _, k := range order {
		r := runs[k]
		if r.record.ID == "" {
			if err := Teardown(ctx, e.c, r.ref, e.gateway); err != nil {
				return nil, fmt.Errorf("docker: what is left of run %s/%s: %w", r.ref.Namespace, r.ref.Name, err)
			}
			continue
		}
		r.found.Record = []byte(r.record.Labels[labelRecord])
		out = append(out, r.found)
	}
	return out, nil
}

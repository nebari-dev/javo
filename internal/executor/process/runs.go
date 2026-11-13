//go:build linux || darwin

package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/supervise"
	"github.com/nebari-dev/javo/internal/workload"
)

// The files of a run, in its folder beside its logs: what is kept of it
// (supervise.Keeper), and the folder its main unit leaves its last
// message in, one file per attempt.
const (
	recordFile = "run.json"
	messageDir = "message"
)

// Runs runs job runs (workload.Batch) as child processes, by the rules
// the Executor it was made from runs apps by: what that refuses, this
// refuses. It is an executor.Executor. Stop ends a run that is going,
// which stays to be observed; a run that has ended it lets go of, and the
// run's folder goes.
type Runs struct {
	*supervise.Supervisor
	x *Executor
}

var _ executor.Executor = (*Runs)(nil)

// Runs is the executor for job runs on the machine x runs apps on, in
// the same state directory. It finds the runs an earlier Javo kept there:
// one that is done stays to be observed, and one that was going has lost
// that attempt, since no process outlives Javo. Close it before x.
func (x *Executor) Runs(ctx context.Context) (*Runs, error) {
	c := x.timing
	c.Engine = runEngine{x.eng}
	r := &Runs{Supervisor: supervise.New(c), x: x}
	if err := r.Supervisor.Adopt(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// Start runs the run s, or refuses it as the Executor refuses an app.
func (r *Runs) Start(ctx context.Context, s workload.Spec) error {
	if s.Batch == nil {
		return fmt.Errorf("process executor: %s/%s is not a run", s.Ref.Namespace, s.Ref.Name)
	}
	r.x.starting.Lock()
	defer r.x.starting.Unlock()
	if st, err := r.Observe(ctx, s.Ref); err != nil || st != nil {
		return err
	}
	s = withoutIdentity(s)
	if err := r.x.eng.prepare(ctx, s); err != nil {
		return err
	}
	wd, err := folder(s.Ref)
	if err != nil {
		return err
	}
	if err := r.x.eng.root.MkdirAll(path.Join(wd, messageDir), 0o700); err != nil {
		return err
	}
	return r.Supervisor.Start(ctx, s)
}

// Addr is an error: a run serves nothing.
func (r *Runs) Addr(_ context.Context, ref workload.Ref, _ int32) (string, error) {
	return "", fmt.Errorf("run %s/%s has no address", ref.Namespace, ref.Name)
}

// Logs streams the log of a unit of the run's newest attempt, or returns
// nil while that has printed nothing.
func (r *Runs) Logs(ctx context.Context, ref workload.Ref, o executor.LogOptions) (io.ReadCloser, error) {
	rc, err := r.Supervisor.Logs(ctx, ref, o)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return rc, err
}

// runEngine is the engine as the supervisor of runs sees it.
type runEngine struct{ *engine }

var _ supervise.Keeper = runEngine{}

// Adopt finds the runs whose record an earlier Javo kept. None has a
// unit that runs: New killed what was left of them (sweep).
func (e runEngine) Adopt(context.Context) ([]supervise.Found, error) {
	records, err := fs.Glob(e.root.FS(), path.Join("w", "*", "*", recordFile))
	if err != nil {
		return nil, err
	}
	var out []supervise.Found
	for _, rec := range records {
		raw, err := e.root.ReadFile(rec)
		if err != nil {
			return nil, err
		}
		out = append(out, supervise.Found{Record: raw})
	}
	return out, nil
}

// Keep writes the run's record in its folder, in one step: a reader
// finds the old record or the new one.
func (e runEngine) Keep(_ context.Context, s workload.Spec, record []byte) error {
	wd, err := folder(s.Ref)
	if err != nil {
		return err
	}
	if err := e.root.MkdirAll(wd, 0o700); err != nil {
		return err
	}
	fresh := path.Join(wd, recordFile+".new")
	if err := e.root.WriteFile(fresh, record, 0o600); err != nil {
		return err
	}
	return e.root.Rename(fresh, path.Join(wd, recordFile))
}

// Release removes the run's folder: its logs, its record and its
// messages.
func (e runEngine) Release(_ context.Context, ref workload.Ref) error {
	wd, err := folder(ref)
	if err != nil {
		return err
	}
	return e.root.RemoveAll(wd)
}

// MessageDir is a folder of the run's own. It is closed to other users
// and to nothing that runs as Javo's: this executor isolates nothing.
func (e runEngine) MessageDir(s workload.Spec) string {
	return filepath.Join(e.dir, "w", s.Ref.Namespace, s.Ref.Name, messageDir)
}

func (e runEngine) Message(_ context.Context, s workload.Spec, name string) string {
	wd, err := folder(s.Ref)
	if err != nil {
		return ""
	}
	f, err := e.root.Open(path.Join(wd, messageDir, name))
	if err != nil {
		return ""
	}
	defer f.Close()
	raw, _ := io.ReadAll(io.LimitReader(f, supervise.MaxMessage))
	return string(raw)
}

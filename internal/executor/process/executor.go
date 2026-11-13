//go:build linux || darwin

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/supervise"
	"github.com/nebari-dev/javo/internal/workload"
)

// Options is what an Executor is made of.
type Options struct {
	// Dir is the state directory, an absolute path: it is made if missing,
	// and must be the user's own and closed to everyone else.
	Dir string
	// Secrets is where the values workloads read come from.
	Secrets executor.Secrets
	// RunnerImage is the reference of the platform's own image. A unit of
	// that image that runs /javo runs this binary; empty means no unit does.
	RunnerImage string
	// Pixi is the pixi binary the step that installs an environment runs,
	// an absolute path; empty leaves it to that step's PATH.
	Pixi string
	// Files is the folder holding the platform's files (pod/files, the
	// terminal page and the tools), laid out as the platform's image holds
	// them; empty when no workload uses them.
	Files string
	// Grace is how long a unit has to exit once asked to, before it is
	// killed; zero is ten seconds.
	Grace time.Duration
}

// ErrBatch is Start's answer to a job run: Runs runs those.
var ErrBatch = errors.New("process executor: a job run is not an app")

// UnsupportedError is a part of a workload that cannot be run without
// containers.
type UnsupportedError = executor.UnsupportedError

// Executor runs apps as child processes.
type Executor struct {
	*supervise.Supervisor
	eng *engine
	// starting lets one Start prepare a workload at a time.
	starting sync.Mutex
}

var _ executor.Executor = (*Executor)(nil)

// euid is the user Javo runs as.
var euid = os.Geteuid

// New makes an Executor that keeps its state in o.Dir, and kills what an
// earlier Javo left running there. It refuses to run as root.
func New(o Options) (*Executor, error) {
	if euid() == 0 {
		return nil, errors.New("process executor: refusing to run apps as root (uid 0)")
	}
	if !filepath.IsAbs(o.Dir) {
		return nil, fmt.Errorf("process executor: the state directory %q is not an absolute path", o.Dir)
	}
	dir := filepath.Clean(o.Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ok || int(st.Uid) != euid() {
		return nil, fmt.Errorf("process executor: the state directory %s must be a folder of this user that no one else can open (mode 0700)", dir)
	}
	if o.Pixi != "" {
		if info, err := os.Stat(o.Pixi); !filepath.IsAbs(o.Pixi) || err != nil || info.IsDir() {
			return nil, fmt.Errorf("process executor: pixi: %q is not the absolute path of a program", o.Pixi)
		}
	}
	if o.Files != "" {
		if info, err := os.Stat(o.Files); !filepath.IsAbs(o.Files) || err != nil || !info.IsDir() {
			return nil, fmt.Errorf("process executor: the platform's files: %q is not the absolute path of a folder", o.Files)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	eng := &engine{root: root, dir: dir, secrets: o.Secrets, runner: o.RunnerImage, self: self, grace: o.Grace, pixi: o.Pixi, files: o.Files}
	if eng.grace <= 0 {
		eng.grace = 10 * time.Second
	}
	eng.sweep()
	return &Executor{Supervisor: supervise.New(supervise.Config{Engine: eng}), eng: eng}, nil
}

// Start runs the app s, or refuses it: with an *UnsupportedError when it
// cannot run without containers, with ErrBatch when it is a job run.
func (x *Executor) Start(ctx context.Context, s workload.Spec) error {
	if s.Batch != nil {
		return ErrBatch
	}
	x.starting.Lock()
	defer x.starting.Unlock()
	if st, err := x.Observe(ctx, s.Ref); err != nil || st != nil {
		return err
	}
	s = withoutIdentity(s)
	if err := x.eng.prepare(ctx, s); err != nil {
		return err
	}
	return x.Supervisor.Start(ctx, s)
}

// Stop ends the workload's processes and removes its folder: its logs,
// its empty volumes, its secret files and its port.
func (x *Executor) Stop(ctx context.Context, ref workload.Ref) error {
	if err := x.Supervisor.Stop(ctx, ref); err != nil {
		return err
	}
	wd, err := folder(ref)
	if err != nil {
		return err
	}
	return x.eng.root.RemoveAll(wd)
}

// Close stops every workload; their folders stay.
func (x *Executor) Close() {
	x.Supervisor.Close()
	_ = x.eng.root.Close()
}

// Host is what a workload of ref is planned with to run here: its own
// folders, the platform's files, the PATH of the step that installs an
// environment (the folder of tools.go first) and Javo's own uid and gid.
func (x *Executor) Host(ref workload.Ref) workload.Host {
	return workload.Host{
		Volumes:     filepath.Dir(x.VolumeDir(ref, "v")),
		Files:       x.eng.files,
		InstallPath: filepath.Join(x.eng.dir, toolsDir) + string(os.PathListSeparator) + os.Getenv("PATH"),
		UID:         int64(os.Getuid()), GID: int64(os.Getgid()),
		Platform: pixiPlatforms[runtime.GOOS+"/"+runtime.GOARCH],
	}
}

// pixiPlatforms is what pixi calls the machines this executor runs on.
var pixiPlatforms = map[string]string{
	"darwin/arm64": "osx-arm64", "darwin/amd64": "osx-64", "linux/amd64": "linux-64", "linux/arm64": "linux-aarch64",
}

// VolumeDir is the folder that is the workload's volume called name, for
// a volume without a Source. A unit mounts the volume at that path and no
// other.
func (x *Executor) VolumeDir(ref workload.Ref, name string) string {
	return x.eng.hostDir(ref, workload.Volume{Name: name})
}

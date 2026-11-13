//go:build linux || darwin

package process

import (
	"context"
	"fmt"
	"os"
	"path"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/supervise"
	"github.com/nebari-dev/javo/internal/workload"
)

// engine starts units as child processes. Every file it writes goes
// through root, which cannot be led out of the state directory.
type engine struct {
	root *os.Root
	// dir is the state directory's path, for what a child is told.
	dir     string
	secrets executor.Secrets
	// runner is the platform's own image and self the binary that stands
	// in for it.
	runner, self string
	// pixi is the pixi binary and files the platform's files (Options).
	pixi, files string
	grace       time.Duration
	// ports lets one caller pick a port at a time.
	ports sync.Mutex
}

var _ supervise.Engine = (*engine)(nil)

// drain is how long a unit that has exited is waited on for the last of
// its output; a process it left behind can keep the output open for ever.
const drain = time.Second

// Start runs u as a child process, the leader of a process group of its
// own, with its output going to its log file.
func (e *engine) Start(ctx context.Context, s workload.Spec, u workload.Unit) (supervise.Unit, error) {
	cmd, err := e.command(ctx, s, u)
	if err != nil {
		return nil, err
	}
	wd, err := folder(s.Ref)
	if err != nil {
		return nil, err
	}
	log, err := e.newLog(wd, u.Name)
	if err != nil {
		return nil, err
	}
	// A pipe of our own, not one os/exec makes: Wait then returns when the
	// process exits, also when something it started keeps the output open.
	out, in, err := os.Pipe()
	if err != nil {
		_ = log.Close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = in, in
	cmd.SysProcAttr = procAttr()

	p := &proc{grace: e.grace, exited: make(chan struct{})}
	guarded := u.Name == s.Main.Name && s.Port != 0
	started := make(chan error)
	go func() {
		// The process is killed when the thread that started it ends
		// (procAttr, on Linux): keep that thread for as long as it runs.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		err := cmd.Start()
		_ = in.Close()
		if err != nil {
			_ = out.Close()
			_ = log.Close()
			started <- err
			return
		}
		copied := make(chan struct{})
		go func() {
			defer close(copied)
			stamp(log, out)
			_ = out.Close()
			_ = log.Close()
		}()
		p.pid = cmd.Process.Pid
		e.record(wd, u.Name, p.pid)
		started <- nil
		if guarded {
			go p.guard(int(s.Port))
		}

		// Learn of the exit before the process is reaped: until then its
		// pid, and so its group, cannot be anyone else's. What it left
		// running in the group ends with it.
		err = awaitExit(p.pid)
		p.mu.Lock()
		p.gone = true
		if err == nil {
			_ = syscall.Kill(-p.pid, syscall.SIGKILL)
		}
		p.mu.Unlock()
		_ = cmd.Wait()
		e.forget(wd, u.Name)
		p.code = cmd.ProcessState.ExitCode()
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			p.code = 128 + int(ws.Signal())
		}
		select {
		case <-copied:
			// The log is closed: say why the unit was killed, for whoever
			// reads its last lines.
			if p.open != "" {
				e.note(wd, u.Name, fmt.Sprintf(openNote, p.open))
			}
		case <-time.After(drain):
		}
		close(p.exited)
	}()
	if err := <-started; err != nil {
		return nil, fmt.Errorf("%s: %w", path.Base(cmd.Path), err)
	}
	return p, nil
}

// proc is one child process and its group.
type proc struct {
	pid    int
	grace  time.Duration
	exited chan struct{}
	// code is the exit code, set before exited is closed.
	code int

	mu sync.Mutex
	// gone is set once the process has exited: its group is no longer
	// signalled.
	gone bool
	// open is the address beyond loopback the unit was killed for
	// listening on (guard); empty for a unit that was not.
	open string
}

func (p *proc) Wait() int {
	<-p.exited
	return p.code
}

// Stop asks the group to end, and kills it when the leader is still there
// after the grace period.
func (p *proc) Stop() {
	p.signal(syscall.SIGTERM)
	grace := time.NewTimer(p.grace)
	defer grace.Stop()
	select {
	case <-p.exited:
		return
	case <-grace.C:
	}
	p.signal(syscall.SIGKILL)
	<-p.exited
}

func (p *proc) signal(sig syscall.Signal) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.gone {
		_ = syscall.Kill(-p.pid, sig)
	}
}

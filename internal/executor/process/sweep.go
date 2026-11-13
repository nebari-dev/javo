//go:build linux || darwin

package process

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"syscall"
	"time"
)

// markEnv is the variable a unit is started with, its value (the mark)
// made up for that one start. Whatever the unit starts inherits it, also
// a daemon that leaves the unit's process group and session, whose
// parent is no longer the unit: the mark is how such a process is known
// to be the unit's. A process that drops its environment is not found.
const markEnv = "JAVO_PROCESS_MARK"

// record notes that the unit of the workload in wd runs as pid, with the
// time that process started (the two together name it for as long as the
// machine is up) and with its mark.
func (e *engine) record(wd, unit string, pid int, mark string) {
	if began, err := startTime(pid); err == nil {
		_ = e.root.WriteFile(path.Join(wd, "pids", unit), fmt.Appendf(nil, "%d %s %s\n", pid, began, mark), 0o600)
	}
}

// reapFor is how long reap goes on looking for processes that keep
// appearing or do not die.
const reapFor = 5 * time.Second

// reap kills every process of this user that carries mark in its
// environment, where no one but the unit started with that mark and what
// it started can have it. It looks until it finds none: a process may
// start another while it is looked for. Each is killed by its own pid,
// only when it started at the same time before and after its environment
// was read, so the pid was not given to another process in between.
func reap(mark string) {
	if mark == "" {
		return
	}
	want := []byte("\x00" + markEnv + "=" + mark + "\x00")
	for deadline := time.Now().Add(reapFor); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		found := false
		for _, pid := range processes() {
			// 0 and 1 are never a unit's, and neither is Javo.
			if pid <= 1 || pid == os.Getpid() {
				continue
			}
			began, err := startTime(pid)
			if err != nil {
				continue
			}
			// A process that has exited and is not reaped has none.
			if env, err := environ(pid); err != nil || !bytes.Contains(env, want) {
				continue
			}
			if now, err := startTime(pid); err == nil && now == began {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				found = true
			}
		}
		if !found {
			return
		}
	}
}

func (e *engine) forget(wd, unit string) {
	_ = e.root.Remove(path.Join(wd, "pids", unit))
}

// sweep kills the processes an earlier Javo recorded and did not stop,
// and what carries their marks. A record whose pid now belongs to another
// process (its start time differs) is dropped and that pid is not
// signalled.
func (e *engine) sweep() {
	records, _ := fs.Glob(e.root.FS(), "w/*/*/pids/*")
	for _, rec := range records {
		var pid int
		var began, mark string
		if b, err := e.root.ReadFile(rec); err == nil {
			_, _ = fmt.Sscanf(string(b), "%d %s %s", &pid, &began, &mark)
		}
		// 0 and 1 are never a unit's: as a group they would be everyone.
		if pid > 1 && began != "" {
			if now, err := startTime(pid); err == nil && now == began {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
		reap(mark)
		_ = e.root.Remove(rec)
	}
}

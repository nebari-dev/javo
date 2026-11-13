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
// to be the unit's.
const markEnv = "JAVO_PROCESS_MARK"

// running is a process that has not exited: its pid and the time it
// started, which together name it, and the pid of its parent.
type running struct {
	pid, parent int
	began       string
}

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

// reap kills the processes of the unit that was started with mark: every
// process that carries mark in its environment, where no one but that
// unit and what it started can have it, and every process one of those
// started, for as long as its parent is still there. The second finds
// what does not show its environment (macOS shows none of a program of
// the system's own) or was started with another. What is not found: a
// process without the mark whose parent has exited.
//
// It looks until it finds none, as a process may start another while it
// is looked for. Each is killed by its own pid, and only when it still
// has the start time it was found with: the pid was not given to another
// process in between.
func reap(mark string) {
	if mark == "" {
		return
	}
	want := []byte("\x00" + markEnv + "=" + mark + "\x00")
	for deadline := time.Now().Add(reapFor); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		all := processes()
		ours := map[int]bool{}
		for _, p := range all {
			if env, err := environ(p.pid); err == nil && bytes.Contains(env, want) {
				ours[p.pid] = true
			}
		}
		for grew := true; grew; {
			grew = false
			for _, p := range all {
				if !ours[p.pid] && ours[p.parent] {
					ours[p.pid], grew = true, true
				}
			}
		}
		found := false
		for _, p := range all {
			// 0 and 1 are never a unit's, and Javo is not killed by itself.
			if !ours[p.pid] || p.pid <= 1 || p.pid == os.Getpid() {
				continue
			}
			if now, err := startTime(p.pid); err == nil && now == p.began {
				_ = syscall.Kill(p.pid, syscall.SIGKILL)
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

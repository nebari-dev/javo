//go:build linux || darwin

package process

import (
	"fmt"
	"io/fs"
	"path"
	"syscall"
)

// record notes that the unit of the workload in wd runs as pid, with the
// time that process started: the two together name it for as long as the
// machine is up.
func (e *engine) record(wd, unit string, pid int) {
	if began, err := startTime(pid); err == nil {
		_ = e.root.WriteFile(path.Join(wd, "pids", unit), fmt.Appendf(nil, "%d %s\n", pid, began), 0o600)
	}
}

func (e *engine) forget(wd, unit string) {
	_ = e.root.Remove(path.Join(wd, "pids", unit))
}

// sweep kills the processes an earlier Javo recorded and did not stop. A
// record whose pid now belongs to another process (its start time
// differs) is dropped and nothing is signalled.
func (e *engine) sweep() {
	records, _ := fs.Glob(e.root.FS(), "w/*/*/pids/*")
	for _, rec := range records {
		var pid int
		var began string
		if b, err := e.root.ReadFile(rec); err == nil {
			_, _ = fmt.Sscanf(string(b), "%d %s", &pid, &began)
		}
		// 0 and 1 are never a unit's: as a group they would be everyone.
		if pid > 1 && began != "" {
			if now, err := startTime(pid); err == nil && now == began {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
		_ = e.root.Remove(rec)
	}
}

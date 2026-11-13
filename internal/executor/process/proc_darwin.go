package process

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// procAttr makes a child the leader of its own process group. macOS has
// no way to have it killed when Javo dies.
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// awaitExit returns once the child pid has exited, without reaping it.
func awaitExit(pid int) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer unix.Close(kq)
	watch := []unix.Kevent_t{{
		Ident: uint64(pid), Filter: unix.EVFILT_PROC,
		Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT,
	}}
	seen := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, watch, seen, nil)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ESRCH):
			// It exited before it could be watched; it is our child and
			// not reaped, so it is still there.
			return nil
		case err != nil:
			return err
		case n > 0 && seen[0].Flags&unix.EV_ERROR != 0:
			if unix.Errno(seen[0].Data) == unix.ESRCH {
				return nil
			}
			return unix.Errno(seen[0].Data)
		case n > 0:
			return nil
		}
	}
}

// startTime is when the process pid started.
func startTime(pid int) (string, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if int(k.Proc.P_pid) != pid {
		return "", fmt.Errorf("no process %d", pid)
	}
	t := k.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", t.Sec, t.Usec), nil
}

// processes are the pids of this user's processes.
func processes() []int {
	all, _ := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	pids := make([]int, len(all))
	for i := range all {
		pids[i] = int(all[i].Proc.P_pid)
	}
	return pids
}

// environ is what the process pid was started with: its arguments and its
// environment, each ended by a zero byte.
func environ(pid int) ([]byte, error) {
	return unix.SysctlRaw("kern.procargs2", pid)
}

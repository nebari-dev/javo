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
	return began(k), nil
}

func began(k *unix.KinfoProc) string {
	t := k.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", t.Sec, t.Usec)
}

// zombie is the state (p_stat, SZOMB) of a process that has exited and
// was not reaped.
const zombie = 5

// processes are this user's processes that have not exited.
func processes() []running {
	all, _ := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	var out []running
	for i := range all {
		if k := &all[i]; k.Proc.P_stat != zombie {
			out = append(out, running{pid: int(k.Proc.P_pid), parent: int(k.Eproc.Ppid), began: began(k)})
		}
	}
	return out
}

// environ is what the process pid was started with: its arguments and its
// environment, each ended by a zero byte. The system shows the arguments
// alone of a program of its own (one in /bin or /usr/bin).
func environ(pid int) ([]byte, error) {
	return unix.SysctlRaw("kern.procargs2", pid)
}

package process

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// procAttr makes a child the leader of its own process group, and has the
// kernel kill it when Javo dies.
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

// awaitExit returns once the child pid has exited, without reaping it.
func awaitExit(pid int) error {
	for {
		if err := unix.Waitid(unix.P_PID, pid, nil, unix.WEXITED|unix.WNOWAIT, nil); !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

// startTime is when the process pid started, in the kernel's own unit
// (field 22 of /proc/<pid>/stat).
func startTime(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	// The second field is the program's name in brackets and may hold
	// anything; the rest starts after the last bracket, at field 3.
	i := strings.LastIndex(string(b), ") ")
	if i < 0 {
		return "", fmt.Errorf("unexpected /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(b)[i+2:])
	if len(fields) < 20 {
		return "", fmt.Errorf("unexpected /proc/%d/stat", pid)
	}
	return fields[19], nil
}

// processes are the pids of the machine's processes.
func processes() []int {
	entries, _ := os.ReadDir("/proc")
	var pids []int
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// environ is the environment the process pid was started with, each
// variable ended by a zero byte, after a zero byte. Another user's cannot
// be read.
func environ(pid int) ([]byte, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	return append([]byte{0}, b...), err
}

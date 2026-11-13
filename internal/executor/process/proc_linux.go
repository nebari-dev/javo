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
	p, _, err := stat(pid)
	return p.began, err
}

// stat is the process pid as /proc/<pid>/stat has it, and its state (the
// file's field 3).
func stat(pid int) (running, string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return running{}, "", err
	}
	// The second field is the program's name in brackets and may hold
	// anything; the rest starts after the last bracket, at field 3.
	i := strings.LastIndex(string(b), ") ")
	if i < 0 {
		return running{}, "", fmt.Errorf("unexpected /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(b)[i+2:])
	if len(fields) < 20 {
		return running{}, "", fmt.Errorf("unexpected /proc/%d/stat", pid)
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return running{}, "", fmt.Errorf("unexpected /proc/%d/stat", pid)
	}
	return running{pid: pid, parent: parent, began: fields[19]}, fields[0], nil
}

// processes are the machine's processes that have not exited.
func processes() []running {
	entries, _ := os.ReadDir("/proc")
	var out []running
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		// Z is a process that has exited and was not reaped.
		if p, state, err := stat(pid); err == nil && state != "Z" {
			out = append(out, p)
		}
	}
	return out
}

// environ is the environment the process pid was started with, each
// variable ended by a zero byte, after a zero byte. Another user's cannot
// be read.
func environ(pid int) ([]byte, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	return append([]byte{0}, b...), err
}

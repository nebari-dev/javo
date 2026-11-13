package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// options is the parsed command line.
type options struct {
	rw, ro  []string
	minABI  int
	command []string
}

const usage = "usage: javo-sandbox [--rw DIR]... [--ro DIR]... [--min-abi N] -- COMMAND ARGS..."

func parse(args []string) (options, error) {
	// Version 3 is the first that can refuse truncating a file, so a
	// locked command cannot empty a file it may only read.
	o := options{minABI: 3}
	for len(args) > 0 {
		flag := args[0]
		if flag == "--" {
			o.command = args[1:]
			break
		}
		if len(args) < 2 {
			return o, fmt.Errorf("%s needs a value (%s)", flag, usage)
		}
		value := args[1]
		args = args[2:]
		switch flag {
		case "--rw":
			o.rw = append(o.rw, value)
		case "--ro":
			o.ro = append(o.ro, value)
		case "--min-abi":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return o, fmt.Errorf("--min-abi %q is not a Landlock version (a number from 1)", value)
			}
			o.minABI = n
		default:
			return o, fmt.Errorf("unknown flag %q (%s)", flag, usage)
		}
	}
	if len(o.command) == 0 {
		return o, fmt.Errorf("no command to run after -- (%s)", usage)
	}
	return o, nil
}

// run locks this process in and replaces it with the command. Landlock and
// no_new_privs apply to the calling OS thread only, so the goroutine stays on
// one thread from the restriction to the exec: the command then starts
// locked, and every process it starts inherits the lock.
func run(args []string) error {
	o, err := parse(args)
	if err != nil {
		return err
	}
	path, err := exec.LookPath(o.command[0])
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	abi, err := landlockABI()
	if err != nil {
		return err
	}
	if abi < o.minABI {
		return fmt.Errorf("this kernel has Landlock version %d, older than the %d needed to lock the command in; not running it", abi, o.minABI)
	}
	if err := restrict(handledRights(abi), o); err != nil {
		return err
	}
	return syscall.Exec(path, o.command, os.Environ())
}

// landlockABI is the kernel's Landlock version; an error when the kernel
// has none or it is turned off.
func landlockABI() (int, error) {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	switch errno {
	case 0:
		return int(v), nil
	case unix.ENOSYS:
		return 0, errors.New("this kernel has no Landlock (Linux 5.13 or later is needed); not running the command")
	case unix.EOPNOTSUPP:
		return 0, errors.New("Landlock is turned off on this machine (it must be in the kernel's lsm= boot parameter); not running the command")
	default:
		return 0, fmt.Errorf("checking for Landlock: %w; not running the command", errno)
	}
}

// fileRights are the rights that apply to a file itself; the others act on
// a folder's entries, and a rule on a file may not name them.
const fileRights = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
	unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV

// readRights are what --ro grants: run programs and read files and folders.
const readRights = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR

// handledRights are the file rights Landlock version abi knows. The lock
// takes all of them away outside the granted folders; a right the kernel
// does not know it cannot take away (truncate before version 3).
func handledRights(abi int) uint64 {
	// Version 1 has execute up to make_sym, one bit each.
	rights := uint64(unix.LANDLOCK_ACCESS_FS_MAKE_SYM<<1 - 1)
	if abi >= 2 {
		rights |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		rights |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		rights |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return rights
}

// restrict makes a ruleset that handles rights, grants o's folders, and
// locks the calling thread in with it.
func restrict(rights uint64, o options) error {
	attr := unix.LandlockRulesetAttr{Handled_access_fs: rights}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("creating the Landlock ruleset: %w", errno)
	}
	ruleset := int(fd)
	defer unix.Close(ruleset)
	for _, g := range []struct {
		paths  []string
		rights uint64
	}{{o.rw, rights}, {o.ro, readRights & rights}} {
		for _, p := range g.paths {
			if err := allow(ruleset, p, g.rights); err != nil {
				return err
			}
		}
	}
	// Landlock refuses to lock in a process that could gain privileges by
	// running a setuid program, unless it gives that up first.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("setting no_new_privs: %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), 0, 0); errno != 0 {
		return fmt.Errorf("locking in with Landlock: %w", errno)
	}
	return nil
}

// allow grants rights beneath path; on a file only the rights a file can
// have.
func allow(ruleset int, path string, rights uint64) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("granting %s: %w; not running the command", path, err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("granting %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		rights &= fileRights
	}
	rule := unix.LandlockPathBeneathAttr{Allowed_access: rights, Parent_fd: int32(fd)}
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("granting %s: %w", path, errno)
	}
	return nil
}

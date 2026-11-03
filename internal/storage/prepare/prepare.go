// Package prepare readies a freshly provisioned home volume or group folder:
// it sets the directory's mode and checks that the home's user, or the
// folder's group, can write there. A prepare Job runs it once per volume.
package prepare

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// Error is a volume root the home's user cannot write to (the usual cause is
// a storage driver that ignores fsGroup on a root that is not world-writable),
// or a group folder that did not keep its group, group write or setgid.
type Error struct {
	Dir string
	// UID and GID are the user and group that must be able to write.
	UID, GID       int
	GotUID, GotGID int
	GotMode        os.FileMode
	// Cause is the chmod error, when the mode could not be set.
	Cause error
	// Shared is a group folder: members write through group GID.
	Shared bool
}

func (e *Error) Error() string {
	if e.Shared {
		msg := fmt.Sprintf("folder at %s has group %d, want %d, and mode %v, want group write and setgid: the storage did not keep the group, so members could not edit each other's files",
			e.Dir, e.GotGID, e.GID, e.GotMode)
		if e.Cause != nil {
			msg += " (" + e.Cause.Error() + ")"
		}
		return msg
	}
	msg := fmt.Sprintf("volume at %s is owned by %d:%d with mode %v, which user %d in group %d cannot write: the storage driver did not apply fsGroup",
		e.Dir, e.GotUID, e.GotGID, e.GotMode, e.UID, e.GID)
	if e.Cause != nil {
		msg += " (" + e.Cause.Error() + ")"
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Cause }

// ParseMode reads an octal mode such as "0775" or "02775" (setgid). Setuid
// and sticky are refused: a home never needs them.
func ParseMode(s string) (os.FileMode, error) {
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("mode %q is not an octal number", s)
	}
	if n&^0o2777 != 0 {
		return 0, fmt.Errorf("mode %q sets bits other than permissions and setgid", s)
	}
	mode := os.FileMode(n & 0o777)
	if n&0o2000 != 0 {
		mode |= os.ModeSetgid
	}
	return mode, nil
}

// writeSearch is write and execute (search) for one class, shifted by class.
const writeSearch = 0o3

// Run asks for mode on dir (created first when mkdir is set), then checks that
// uid in group gid can create files there. The chmod is best effort: a process
// that does not own the root may not do it (fsGroup is not applied to hostPath
// volumes, so a local-path root stays root-owned 0777), and the check, not the
// chmod, decides. Setgid in mode marks a group folder: then dir must end up
// in group gid, group-writable and setgid, whoever owns it.
func Run(dir string, uid, gid int, mode os.FileMode, mkdir bool) error {
	if mkdir {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	chmodErr := os.Chmod(dir, mode)
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	return check(dir, info, uid, gid, mode&os.ModeSetgid != 0, chmodErr)
}

// check decides whether what Stat reported is usable; see Run.
func check(dir string, info os.FileInfo, uid, gid int, shared bool, chmodErr error) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read the owner of %s on this platform", dir)
	}
	perm := info.Mode().Perm()
	fail := &Error{Dir: dir, UID: uid, GID: gid, GotUID: int(st.Uid), GotGID: int(st.Gid), GotMode: info.Mode(), Cause: chmodErr, Shared: shared}
	if shared {
		if int(st.Gid) != gid || (perm>>3)&writeSearch != writeSearch || info.Mode()&os.ModeSetgid == 0 {
			return fail
		}
		return nil
	}
	// POSIX picks one class: owner, else group, else others.
	shift := 0
	switch {
	case int(st.Uid) == uid:
		shift = 6
	case int(st.Gid) == gid:
		shift = 3
	}
	if (perm>>shift)&writeSearch != writeSearch {
		return fail
	}
	return nil
}

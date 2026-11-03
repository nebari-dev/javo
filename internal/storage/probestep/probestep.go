// Package probestep holds the three steps the storage probe Jobs run against
// a volume: prepare, write, check and confirm. Each step asserts one thing a real
// home or group folder relies on.
package probestep

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Step names, also the values of the probe's failedStep for these steps.
const (
	StepPrepare = "prepare"
	StepWrite   = "write"
	StepCheck   = "check"
	StepConfirm = "confirm"
)

const (
	fileName  = "probe.txt"
	dirName   = "shared"
	firstLine = "written by write"
	nextLine  = "appended by check"
)

// Error is a failed assertion: what the step wanted and what it saw.
type Error struct {
	Step, Want, Got string
}

func (e *Error) Error() string {
	return fmt.Sprintf("probe step %s: want %s, got %s", e.Step, e.Want, e.Got)
}

// Run runs one step in dir, as uid and gid (the ids the files must belong to).
func Run(dir, step string, uid, gid int) error {
	switch step {
	case StepPrepare:
		return prepare(dir, gid)
	case StepWrite:
		return write(dir)
	case StepCheck:
		return check(dir, uid, gid)
	case StepConfirm:
		return confirm(dir)
	}
	return fmt.Errorf("unknown probe step %q", step)
}

// prepare runs in a pod whose fsGroup is gid, the way a home's prepare Job
// does, and asserts the volume root is then usable by the steps that follow
// without fsGroup: owned by the group and writable by it and by this user.
// A fresh volume root is often root-owned 0755; fsGroup is what fixes that.
func prepare(dir string, gid int) error {
	fail := func(want, got string) error { return &Error{StepPrepare, want, got} }
	info, err := os.Stat(dir)
	if err != nil {
		return fail("to see the volume root", err.Error())
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fail("to read the volume root owner", "no owner information")
	}
	if int(st.Gid) != gid {
		return fail(fmt.Sprintf("volume root group gid %d (fsGroup applied)", gid), fmt.Sprint(st.Gid))
	}
	if info.Mode().Perm()&0o020 == 0 {
		return fail("group write on the volume root", info.Mode().String())
	}
	if err := syscall.Access(dir, 2); err != nil { // 2 = W_OK
		return fail("the volume root to be writable", err.Error())
	}
	return nil
}

// write creates the probe file and a group-writable setgid directory under
// umask 0002, the way a user's app would.
func write(dir string) error {
	fail := func(want string, err error) error { return &Error{StepWrite, want, err.Error()} }
	old := syscall.Umask(0o002)
	defer syscall.Umask(old)
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(firstLine+"\n"), 0o664); err != nil {
		return fail("to create "+fileName, err)
	}
	shared := filepath.Join(dir, dirName)
	if err := os.Mkdir(shared, 0o775); err != nil {
		return fail("to create directory "+dirName, err)
	}
	// mkdir ignores the setgid bit, so set it explicitly.
	if err := os.Chmod(shared, os.ModeSetgid|0o775); err != nil {
		return fail("to set setgid on "+dirName, err)
	}
	return nil
}

// check runs on another node: it sees the file with the right owner, the
// directory with setgid and group write, and can append to the file.
func check(dir string, uid, gid int) error {
	fail := func(want, got string) error { return &Error{StepCheck, want, got} }
	path := filepath.Join(dir, fileName)
	info, err := os.Stat(path)
	if err != nil {
		return fail("to see "+fileName, err.Error())
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fail("to read file owner", "no owner information")
	}
	if int(st.Uid) != uid {
		return fail(fmt.Sprintf("file owner uid %d", uid), fmt.Sprint(st.Uid))
	}
	if int(st.Gid) != gid {
		return fail(fmt.Sprintf("file group gid %d", gid), fmt.Sprint(st.Gid))
	}
	d, err := os.Stat(filepath.Join(dir, dirName))
	if err != nil {
		return fail("to see directory "+dirName, err.Error())
	}
	if d.Mode()&os.ModeSetgid == 0 {
		return fail("setgid on "+dirName, d.Mode().String())
	}
	if d.Mode().Perm()&0o020 == 0 {
		return fail("group write on "+dirName, d.Mode().String())
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fail("to append to "+fileName, err.Error())
	}
	if _, err := f.WriteString(nextLine + "\n"); err != nil {
		_ = f.Close()
		return fail("to append to "+fileName, err.Error())
	}
	if err := f.Close(); err != nil {
		return fail("to append to "+fileName, err.Error())
	}
	return nil
}

// confirm runs back on the first node and reads the line check appended.
func confirm(dir string) error {
	b, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		return &Error{StepConfirm, "to read " + fileName, err.Error()}
	}
	if !strings.Contains(string(b), nextLine) {
		return &Error{StepConfirm, "the line appended by check", fmt.Sprintf("%q", string(b))}
	}
	return nil
}

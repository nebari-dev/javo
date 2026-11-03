// Package homecopy copies a home folder to a new volume and checks the copy:
// the work of the home mode-change Job (`javo storage copy`), which runs the
// javo image as the home's owner with the owner's folder gids as
// supplemental groups. It keeps what `rsync -rlptgDH --numeric-ids` kept:
// folders, files, symbolic links (as links), modes (setgid and sticky bits
// too), modification times, groups by number, FIFOs and sockets, and hard
// links within the tree. Owners are not set: the process is the owner.
// lost+found at the root (ext4 makes it, owned by root) is left out. Files
// are not made sparse, as rsync without -S did not.
//
// Verify then reads both trees again and lists every difference: a missing
// entry, another type, mode, group, modification time, size, content
// (sha256), link target, or a hard link that became two files.
package homecopy

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// lostFound is left out at the root of the old tree.
const lostFound = "lost+found"

// maxDetail is how many lines of detail the termination message carries.
const maxDetail = 20

type inode struct{ dev, ino uint64 }

type copier struct {
	old, new string
	log      io.Writer
	links    map[inode]string // first copy of each hard-linked file
	failed   []string
	files    int
	bytes    int64
	last     time.Time
	dirs     []string // folders whose times are set once they are filled
}

func copyTree(old, new string, log io.Writer) error {
	c := &copier{old: old, new: new, log: log, links: map[inode]string{}, last: time.Now()}
	err := filepath.WalkDir(old, c.visit)
	// Folder times last, deepest first, since filling a folder changes them.
	for i := len(c.dirs) - 1; i >= 0; i-- {
		rel := c.dirs[i]
		fi, err := os.Lstat(filepath.Join(old, rel))
		if err != nil {
			c.fail(rel, err)
			continue
		}
		mt := fi.ModTime()
		if err := os.Chtimes(filepath.Join(new, rel), mt, mt); err != nil {
			c.fail(rel, err)
		}
	}
	fmt.Fprintf(log, "copied %d files, %d bytes\n", c.files, c.bytes)
	if err != nil {
		return err
	}
	if len(c.failed) > 0 {
		return &PartialError{Paths: c.failed}
	}
	return nil
}

// PartialError is a copy that left some paths out.
type PartialError struct{ Paths []string }

func (e *PartialError) Error() string {
	return fmt.Sprintf("partial transfer: %d paths not copied: %s", len(e.Paths), strings.Join(e.Paths, "; "))
}

func (c *copier) fail(rel string, err error) {
	c.failed = append(c.failed, rel+": "+err.Error())
}

func (c *copier) visit(p string, d fs.DirEntry, walkErr error) error {
	rel, err := filepath.Rel(c.old, p)
	if err != nil {
		return err
	}
	if rel == lostFound && d != nil && d.IsDir() {
		return filepath.SkipDir
	}
	if walkErr != nil {
		if rel == "." {
			return walkErr
		}
		c.fail(rel, walkErr)
		if d != nil && d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	fi, err := os.Lstat(p)
	if err != nil {
		c.fail(rel, err)
		return nil
	}
	if err := c.entry(rel, fi); err != nil {
		c.fail(rel, err)
		if fi.IsDir() {
			return filepath.SkipDir
		}
	}
	if time.Since(c.last) > 10*time.Second {
		c.last = time.Now()
		fmt.Fprintf(c.log, "copied %d files, %d bytes so far\n", c.files, c.bytes)
	}
	return nil
}

func (c *copier) entry(rel string, fi fs.FileInfo) error {
	src, dst := filepath.Join(c.old, rel), filepath.Join(c.new, rel)
	st := fi.Sys().(*syscall.Stat_t)
	mode := fi.Mode()
	switch {
	case mode.IsDir():
		if err := os.Mkdir(dst, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		c.dirs = append(c.dirs, rel)
		return c.attrs(dst, mode, int(st.Gid), time.Time{}, false)
	case mode&fs.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := replace(dst); err != nil {
			return err
		}
		if err := os.Symlink(target, dst); err != nil {
			return err
		}
		if err := os.Lchown(dst, -1, int(st.Gid)); err != nil {
			return err
		}
		mt := unix.NsecToTimespec(fi.ModTime().UnixNano())
		return unix.UtimesNanoAt(unix.AT_FDCWD, dst, []unix.Timespec{mt, mt}, unix.AT_SYMLINK_NOFOLLOW)
	case mode.IsRegular():
		if uint64(st.Nlink) > 1 {
			key := inode{uint64(st.Dev), uint64(st.Ino)} //nolint:unconvert // Dev and Nlink differ in size by OS
			if first, ok := c.links[key]; ok {
				if err := replace(dst); err != nil {
					return err
				}
				return os.Link(filepath.Join(c.new, first), dst)
			}
			c.links[key] = rel
		}
		if same(dst, fi) {
			return c.attrs(dst, mode, int(st.Gid), fi.ModTime(), true)
		}
		if err := replace(dst); err != nil {
			return err
		}
		if err := copyFile(src, dst); err != nil {
			return err
		}
		c.files++
		c.bytes += fi.Size()
		return c.attrs(dst, mode, int(st.Gid), fi.ModTime(), true)
	case mode&(fs.ModeNamedPipe|fs.ModeSocket) != 0:
		if err := replace(dst); err != nil {
			return err
		}
		kind := uint32(unix.S_IFIFO)
		if mode&fs.ModeSocket != 0 {
			kind = unix.S_IFSOCK
		}
		if err := unix.Mknod(dst, kind|uint32(mode.Perm()), 0); err != nil {
			return err
		}
		return c.attrs(dst, mode, int(st.Gid), fi.ModTime(), true)
	default:
		return fmt.Errorf("a device file, which only root can make (mode %v)", mode)
	}
}

// same is rsync's quick check: dst is a regular file with the size and
// modification time of fi, so a retry does not copy it again.
func same(dst string, fi fs.FileInfo) bool {
	d, err := os.Lstat(dst)
	return err == nil && d.Mode().IsRegular() && d.Size() == fi.Size() && d.ModTime().Equal(fi.ModTime())
}

// replace removes what is at dst unless it is a folder.
func replace(dst string) error {
	fi, err := os.Lstat(dst)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("a folder is in the way")
	}
	return os.Remove(dst)
}

// attrs sets the group, the mode (after the group: chown clears setgid) and,
// unless zero, the modification time.
func (c *copier) attrs(dst string, mode fs.FileMode, gid int, mtime time.Time, times bool) error {
	if err := os.Lchown(dst, -1, gid); err != nil {
		return err
	}
	if err := os.Chmod(dst, mode&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky)); err != nil {
		return err
	}
	if times {
		return os.Chtimes(dst, mtime, mtime)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func verifyTree(old, new string) ([]string, error) {
	var diffs []string
	firsts := map[inode]string{} // hard-linked files of old, by inode, to their first path
	err := filepath.WalkDir(old, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(old, p)
		if rel == lostFound && d.IsDir() {
			return filepath.SkipDir
		}
		if rel == "." {
			return nil
		}
		a, err := os.Lstat(p)
		if err != nil {
			return err
		}
		b, err := os.Lstat(filepath.Join(new, rel))
		if err != nil {
			diffs = append(diffs, "missing "+rel)
			if a.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		as, bs := a.Sys().(*syscall.Stat_t), b.Sys().(*syscall.Stat_t)
		perm := fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky
		switch {
		case a.Mode().Type() != b.Mode().Type():
			diffs = append(diffs, "type "+rel)
			return nil
		case a.Mode()&perm != b.Mode()&perm && a.Mode()&fs.ModeSymlink == 0:
			diffs = append(diffs, "mode "+rel)
		case as.Gid != bs.Gid:
			diffs = append(diffs, "group "+rel)
		case !a.ModTime().Equal(b.ModTime()):
			diffs = append(diffs, "time "+rel)
		}
		switch {
		case a.Mode()&fs.ModeSymlink != 0:
			ta, err1 := os.Readlink(p)
			tb, err2 := os.Readlink(filepath.Join(new, rel))
			if err1 != nil || err2 != nil || ta != tb {
				diffs = append(diffs, "target "+rel)
			}
		case a.Mode().IsRegular():
			if uint64(as.Nlink) > 1 {
				key := inode{uint64(as.Dev), uint64(as.Ino)} //nolint:unconvert // sizes differ by OS
				if first, ok := firsts[key]; ok {
					f, err := os.Lstat(filepath.Join(new, first))
					if err != nil || f.Sys().(*syscall.Stat_t).Ino != bs.Ino {
						diffs = append(diffs, "link "+rel)
					}
					return nil
				}
				firsts[key] = rel
			}
			if a.Size() != b.Size() {
				diffs = append(diffs, "size "+rel)
				return nil
			}
			ha, err := hash(p)
			if err != nil {
				return err
			}
			hb, err := hash(filepath.Join(new, rel))
			if err != nil || !bytes.Equal(ha, hb) {
				diffs = append(diffs, "content "+rel)
			}
		}
		return nil
	})
	return diffs, err
}

func hash(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// Run copies old to new, then checks the copy, logging to log. It writes
// the outcome to termLog (the container's termination message): nothing
// when the copy verified, else a headline and at most 20 lines of detail.
func Run(old, new, termLog string, log io.Writer) error {
	fmt.Fprintf(log, "copying %s to %s\n", old, new)
	if err := copyTree(old, new, log); err != nil {
		var pe *PartialError
		if errors.As(err, &pe) {
			return report(termLog, "copy failed: partial transfer", pe.Paths)
		}
		return report(termLog, "copy failed", []string{err.Error()})
	}
	fmt.Fprintln(log, "checking the copy")
	diffs, err := verifyTree(old, new)
	if err != nil {
		return report(termLog, "copy failed while checking it", []string{err.Error()})
	}
	if len(diffs) > 0 {
		return report(termLog, "verify found differences", diffs)
	}
	if err := os.WriteFile(termLog, nil, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(log, "copy verified: no differences")
	return nil
}

// report writes headline and the first lines of detail to termLog and
// returns them as an error.
func report(termLog, headline string, detail []string) error {
	msg := headline
	if len(detail) > maxDetail {
		detail = detail[:maxDetail]
	}
	if len(detail) > 0 {
		msg += "\n" + strings.Join(detail, "\n")
	}
	_ = os.WriteFile(termLog, []byte(msg+"\n"), 0o644)
	return errors.New(msg)
}

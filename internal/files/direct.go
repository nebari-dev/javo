package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// Direct reads and writes homes that are folders of this machine, for the
// process executor: one person, with the gateway and their apps running as
// that person. There is no file pod and no rclone. addr is the home's
// folder, an absolute path (what Folders.Ensure returns), and fs must be
// the home: a host has no group folders.
//
// Nothing stands between the gateway and the person's other files here, so
// what the file pod's container gives the rclone path is done in every
// call instead:
//
//   - The home is opened as an os.Root, and each folder of a path as an
//     os.Root of its own inside the one before. The system refuses to
//     leave an os.Root, whatever the path or a symbolic link in it says.
//   - No call follows a symbolic link, also one that stays inside the
//     home: a folder is entered, and a file opened, only when it is the
//     very folder or plain file that was seen under that name without
//     following (enter, plainFile). A link swapped in between that look
//     and the open is refused, not followed; the Lister's checks before a
//     call are not what keeps a call inside the home.
//   - Only plain files are opened: a pipe or a device is not found.
//
// A call fails with ErrNotFound for a name that does not exist or is not a
// real file or folder, ErrOutsideHome for a path that climbs out of the
// home, and ErrExists where something is in the way.
type Direct struct{}

var _ Remote = Direct{}

// in runs do with the folder rel of the home at addr open. With mk the
// folders of rel that do not exist are made, as rclone makes them for an
// upload.
func (Direct) in(ctx context.Context, addr, fs, rel string, mk bool, do func(dir *os.Root) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fs != dataDir {
		return ErrNotFound
	}
	rel, err := insideHome(rel)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(addr) {
		return fmt.Errorf("files: home %q is not a folder of this machine", addr)
	}
	// The folders above the home are the machine's (a link among them is
	// followed); the home itself must be a real folder.
	above, err := os.OpenRoot(filepath.Dir(addr))
	if err != nil {
		return notFound(err)
	}
	dir, err := enter(above, filepath.Base(addr))
	above.Close()
	if err != nil {
		return err
	}
	for name := range strings.SplitSeq(rel, "/") {
		if name == "" {
			break // rel is the home itself
		}
		if mk {
			if err := dir.Mkdir(name, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				dir.Close()
				return err
			}
		}
		next, err := enter(dir, name)
		dir.Close()
		if err != nil {
			return err
		}
		dir = next
	}
	defer dir.Close()
	return do(dir)
}

// enter opens the folder name of dir, which must be a real folder. A
// symbolic link is ErrNotFound, and so is one put there after the look:
// os.Root follows a link that stays inside it, and the folder opened is
// then not the one that was seen.
func enter(dir *os.Root, name string) (*os.Root, error) {
	seen, err := dir.Lstat(name)
	if err != nil {
		return nil, notFound(err)
	}
	if !seen.IsDir() {
		return nil, ErrNotFound
	}
	sub, err := dir.OpenRoot(name)
	if err != nil {
		return nil, notFound(err)
	}
	if got, err := sub.Stat("."); err != nil || !os.SameFile(seen, got) {
		sub.Close()
		return nil, ErrNotFound
	}
	return sub, nil
}

// plainFile opens the plain file name of dir to be read. Anything else
// under that name (a link, a folder, a pipe, a device) is ErrNotFound,
// also when it was put there after the look, as for enter. The open never
// waits for the other end of a pipe swapped in.
func plainFile(dir *os.Root, name string) (*os.File, error) {
	seen, err := dir.Lstat(name)
	if err != nil {
		return nil, notFound(err)
	}
	if !seen.Mode().IsRegular() {
		return nil, ErrNotFound
	}
	f, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, notFound(err)
	}
	if got, err := f.Stat(); err != nil || !os.SameFile(seen, got) {
		f.Close()
		return nil, ErrNotFound
	}
	return f, nil
}

// notFound is err, as ErrNotFound when it says a name does not exist.
func notFound(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// split is the folder and the name of the entry rel names. The home
// itself has no name: ErrNotFound.
func split(rel string) (dir, name string, err error) {
	rel, err = insideHome(rel)
	if err != nil {
		return "", "", err
	}
	if rel == "" {
		return "", "", ErrNotFound
	}
	dir, name = path.Split(rel)
	return strings.TrimSuffix(dir, "/"), name, nil
}

func entryOf(fi os.FileInfo) Entry {
	return Entry{Name: fi.Name(), IsDir: fi.IsDir(), Size: fi.Size(), ModTime: fi.ModTime()}
}

// List lists the files and folders of remote, sorted by name: links, pipes
// and devices are left out, as rclone leaves them out. It stops at
// MaxEntries entries and marks the result Truncated; which entries of a
// longer folder those are is the system's order, not the names'.
func (d Direct) List(ctx context.Context, addr, fs, remote string) (Listing, error) {
	out := Listing{Entries: []Entry{}}
	err := d.in(ctx, addr, fs, remote, false, func(dir *os.Root) error {
		f, err := dir.Open(".")
		if err != nil {
			return err
		}
		defer f.Close()
		for {
			names, err := f.Readdirnames(256)
			for _, name := range names {
				fi, err := dir.Lstat(name)
				if errors.Is(err, os.ErrNotExist) {
					continue // deleted since it was read
				}
				if err != nil {
					return err
				}
				if !fi.IsDir() && !fi.Mode().IsRegular() {
					continue
				}
				if len(out.Entries) == MaxEntries {
					out.Truncated = true
					return nil
				}
				out.Entries = append(out.Entries, entryOf(fi))
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				return err
			}
		}
	})
	slices.SortFunc(out.Entries, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	return out, err
}

// lstat describes the entry remote names inside fs, without following it.
func (d Direct) lstat(ctx context.Context, addr, fs, remote string) (os.FileInfo, error) {
	dir, name, err := split(remote)
	if err != nil {
		return nil, err
	}
	var fi os.FileInfo
	err = d.in(ctx, addr, fs, dir, false, func(at *os.Root) error {
		fi, err = at.Lstat(name)
		return notFound(err)
	})
	return fi, err
}

// Stat describes the entry remote names without following it: a link is
// an entry that is not a folder. A link on the way to it is not followed
// either, so what is behind one is ErrNotFound.
func (d Direct) Stat(ctx context.Context, addr, fs, remote string) (Entry, error) {
	fi, err := d.lstat(ctx, addr, fs, remote)
	if err != nil {
		return Entry{}, err
	}
	return entryOf(fi), nil
}

// FindFile returns the plain file called name in folder dir, however many
// entries the folder holds.
func (d Direct) FindFile(ctx context.Context, addr, dir, name string) (Entry, error) {
	fi, err := d.lstat(ctx, addr, dataDir, path.Join(dir, name))
	if err != nil {
		return Entry{}, err
	}
	if !fi.Mode().IsRegular() {
		return Entry{}, ErrNotFound
	}
	return entryOf(fi), nil
}

// Open opens the plain file remote; the caller closes it.
func (d Direct) Open(ctx context.Context, addr, remote string) (io.ReadCloser, error) {
	dir, name, err := split(remote)
	if err != nil {
		return nil, err
	}
	var f *os.File
	err = d.in(ctx, addr, dataDir, dir, false, func(at *os.Root) error {
		f, err = plainFile(at, name)
		return err
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Upload writes data as the file name in folder dir.
func (d Direct) Upload(ctx context.Context, addr, dir, name string, data []byte) error {
	return d.UploadStream(ctx, addr, dir, name, bytes.NewReader(data))
}

// UploadStream writes what r holds as the file name in folder dir, making
// the folders that do not exist. Only a plain file of that name is
// replaced (its mode stays); anything else of that name is refused.
//
// The bytes go to a new file beside it, which takes the name only once
// all of them are written. So a write that does not finish (r fails, or
// ctx ends part way: the copy stops at its next read) changes nothing and
// leaves nothing, and nothing is ever written into the file that had the
// name: a file with a second name elsewhere on the disk (a hard link)
// keeps its content there. Taking the name does not follow a link put
// there since the look; the link itself is replaced.
func (d Direct) UploadStream(ctx context.Context, addr, dir, name string, r io.Reader) error {
	dir, name, err := split(path.Join(dir, name))
	if err != nil {
		return err
	}
	return d.in(ctx, addr, dataDir, dir, true, func(at *os.Root) error {
		seen, err := at.Lstat(name)
		switch {
		case errors.Is(err, os.ErrNotExist):
			seen = nil
		case err != nil:
			return err
		case !seen.Mode().IsRegular():
			return ErrNotFound
		}
		// A new name, made only if nothing has it: never a link.
		part := ".part-" + randomSuffix()
		f, err := at.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, &whileLive{ctx: ctx, r: r})
		if err == nil && seen != nil {
			err = f.Chmod(seen.Mode().Perm())
		}
		if err = errors.Join(err, f.Close()); err == nil {
			err = at.Rename(part, name)
		}
		if err != nil {
			return errors.Join(err, at.Remove(part))
		}
		return nil
	})
}

// whileLive reads r until ctx ends.
type whileLive struct {
	ctx context.Context
	r   io.Reader
}

func (w *whileLive) Read(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.r.Read(p)
}

// MoveDir renames the folder src to dst, a name in the same folder that
// nothing has yet (ErrExists). src must be a real folder. Every caller
// moves a folder within its parent; a move to another folder is refused,
// because renaming between two folders would name them by path again.
//
// An empty folder made at dst between the look and the rename is replaced:
// the system's rename allows that, and os.Root offers no rename that does
// not.
func (d Direct) MoveDir(ctx context.Context, addr, src, dst string) error {
	dir, from, err := split(src)
	if err != nil {
		return err
	}
	to, name, err := split(dst)
	if err != nil {
		return err
	}
	if to != dir {
		return fmt.Errorf("files: move of %q to another folder (%q)", src, dst)
	}
	return d.in(ctx, addr, dataDir, dir, false, func(at *os.Root) error {
		if fi, err := at.Lstat(from); err != nil {
			return notFound(err)
		} else if !fi.IsDir() {
			return ErrNotFound
		}
		if _, err := at.Lstat(name); err == nil {
			return ErrExists
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return notFound(at.Rename(from, name))
	})
}

// Purge deletes the real folder dir and everything in it. A link in it is
// deleted, never what it leads to.
func (d Direct) Purge(ctx context.Context, addr, dir string) error {
	parent, name, err := split(dir)
	if err != nil {
		return err
	}
	return d.in(ctx, addr, dataDir, parent, false, func(at *os.Root) error {
		if fi, err := at.Lstat(name); err != nil {
			return notFound(err)
		} else if !fi.IsDir() {
			return ErrNotFound
		}
		return at.RemoveAll(name)
	})
}

// About reports the volume the home is on, from the system's statfs of
// the home folder (Linux and macOS alike): its size, and of that what is
// not free. On a host that is the person's whole disk, not a home's share
// of it; Lister.Usage asks only for a home whose size is enforced, which a
// folder's never is.
func (d Direct) About(ctx context.Context, addr string) (Usage, error) {
	var u Usage
	err := d.in(ctx, addr, dataDir, "", false, func(home *os.Root) error {
		f, err := home.Open(".")
		if err != nil {
			return err
		}
		defer f.Close()
		var st syscall.Statfs_t
		if err := syscall.Fstatfs(int(f.Fd()), &st); err != nil {
			return fmt.Errorf("files: statfs of the home: %w", err)
		}
		size := int64(st.Bsize)
		u = Usage{Used: int64(st.Blocks-st.Bfree) * size, Total: int64(st.Blocks) * size}
		return nil
	})
	return u, err
}

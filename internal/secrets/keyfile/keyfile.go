// Package keyfile keeps one of Javo's keys as a file, for a Javo that runs
// without Kubernetes: the key-encryption key of people's secrets, the
// sessions key and the secrets' fingerprint key. On Kubernetes each is a
// Secret (controller/secretskey), made and kept the same way.
package keyfile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/nebari-dev/javo/internal/secrets"
)

// File is a key kept as a file, for a Javo that runs without Kubernetes:
// the file Name, mode 0600, in the folder Dir, mode 0700. Like secretskey.Secret,
// it makes the key on the first start and never changes it afterwards.
type File struct {
	// Dir is the folder holding Javo's keys.
	Dir string
	// Name is the key's file in Dir: a file name, not a path.
	Name string
	// New makes the value of a new file; nil means secrets.KEKSize random
	// bytes.
	New func() ([]byte, error)
	// Create makes the folder and the file, with a new random key, when
	// they do not exist. Off, a missing file is an error (one made by the
	// admin, such as a restored key, must never be replaced by a new one).
	Create bool
}

// Load returns the key in the file, making the file first when Create is
// set and it does not exist. An existing file is never changed. Its errors
// never contain key material.
func (f File) Load(ctx context.Context) (secrets.KEK, error) {
	b, err := f.Bytes(ctx)
	if err != nil {
		return nil, err
	}
	kek, err := secrets.KEKFromBytes(b)
	if err != nil {
		return nil, fmt.Errorf("the key in file %s: %w", f.path(), err)
	}
	return kek, nil
}

// Bytes returns the key bytes in the file, making the file first when
// Create is set and it does not exist. An existing file is never changed.
// Its errors never contain key material.
func (f File) Bytes(context.Context) ([]byte, error) {
	if f.Name == "" || f.Name == "." || f.Name == ".." || filepath.Base(f.Name) != f.Name {
		return nil, fmt.Errorf("the key file %q must be a file name in %s, not a path", f.Name, f.Dir)
	}
	root, err := f.open()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	b, err := f.read(root)
	if errors.Is(err, fs.ErrNotExist) && f.Create {
		b, err = f.create(root)
	}
	return b, err
}

func (f File) path() string { return filepath.Join(f.Dir, f.Name) }

// open opens the folder, making it first when Create is set, and refuses
// one that is not the running user's alone.
func (f File) open() (*os.Root, error) {
	// Cleaned: Lstat of a path ending in a slash follows a symbolic link.
	dir := filepath.Clean(f.Dir)
	if f.Create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("make the key folder: %w", err)
		}
	}
	named, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("read the key folder: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("read the key folder: %w", err)
	}
	opened, err := root.Stat(".")
	if err == nil {
		err = private(named, opened, fs.ModeDir, 0o700)
	}
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("the key folder %s: %w", dir, err)
	}
	return root, nil
}

// read returns what the key file holds, and refuses a file that is not the
// running user's alone.
func (f File) read(root *os.Root) ([]byte, error) {
	named, err := root.Lstat(f.Name)
	if err != nil {
		return nil, fmt.Errorf("read the key file in %s: %w", f.Dir, err)
	}
	// Checked before it is opened too: opening a pipe would wait for a
	// writer.
	if err := private(named, named, 0, 0o600); err != nil {
		return nil, fmt.Errorf("the key file %s: %w", f.path(), err)
	}
	fh, err := root.OpenFile(f.Name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("read the key file in %s: %w", f.Dir, err)
	}
	defer fh.Close()
	opened, err := fh.Stat()
	if err == nil {
		err = private(named, opened, 0, 0o600)
	}
	if err != nil {
		return nil, fmt.Errorf("the key file %s: %w", f.path(), err)
	}
	b, err := io.ReadAll(fh)
	if err != nil {
		return nil, fmt.Errorf("read the key file %s: %w", f.path(), err)
	}
	return b, nil
}

// private refuses a key file or folder that anyone but the running user
// could have read or replaced. named is what its name holds, not following
// a symbolic link, and opened is the file or folder that was opened: the
// checks are made on opened, which is what is then read, so nothing put
// under the name after the check is used. kind is fs.ModeDir for a folder
// and 0 for a file; want is the mode it should have.
func private(named, opened fs.FileInfo, kind, want fs.FileMode) error {
	switch {
	case named.Mode()&fs.ModeSymlink != 0:
		return errors.New("is a symbolic link; it must be a real " + noun(kind))
	case !os.SameFile(named, opened):
		return errors.New("was replaced while it was read")
	case opened.Mode().Type() != kind:
		return errors.New("is not a " + noun(kind))
	case opened.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("has mode %04o, so its group or others can use it; it must be %04o", opened.Mode().Perm(), want)
	}
	if st, ok := opened.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != euid() {
		return fmt.Errorf("is not owned by the user Javo runs as (uid %d)", euid())
	}
	return nil
}

func noun(kind fs.FileMode) string {
	if kind == fs.ModeDir {
		return "folder"
	}
	return "regular file"
}

// create makes the key file with a new random key. The key is written to
// a file of another name first and then linked to Name, which fails when
// Name exists: a start never sees half a key, and when another process made
// the file first, its key is the one used.
func (f File) create(root *os.Root) ([]byte, error) {
	b := make([]byte, secrets.KEKSize)
	if f.New != nil {
		var err error
		if b, err = f.New(); err != nil {
			return nil, err
		}
	} else if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, err
	}
	tmp := "." + f.Name + ".new-" + hex.EncodeToString(suffix[:])
	fh, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create the key file in %s: %w", f.Dir, err)
	}
	defer root.Remove(tmp)
	_, err = fh.Write(b)
	if err == nil {
		err = fh.Sync()
	}
	if cerr := fh.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Link(tmp, f.Name)
	}
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("create the key file %s: %w", f.path(), err)
	}
	return f.read(root)
}

// euid is the user Javo runs as.
var euid = os.Geteuid

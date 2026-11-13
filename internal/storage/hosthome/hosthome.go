// Package hosthome makes a person's home a folder of the machine Javo
// runs on, for the process executor: one person, their own machine, with
// Javo and their apps running as them.
//
// Every home is a folder directly in one root folder, named after its
// owner as a home's claim is on Kubernetes (naming.HomeClaimName). The
// root and each home must be a real folder (never a symbolic link) that
// the user Javo runs as owns and that nobody else can read, write or
// list; anything else is refused, not repaired. There is one folder per
// person and none per group: nothing mounts a group folder into an app
// that runs as plain processes (podstorage.Local).
package hosthome

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
)

// Root is the folder that holds every person's home.
type Root struct{ dir string }

// Open returns the root at dir, an absolute path, making the folder
// (mode 0700) when only it is missing. A root that is a symbolic link,
// that another user owns, or that others can read, write or list is
// refused. Symbolic links among the folders above it are the machine's
// own (/var on macOS) and are followed.
func Open(dir string) (Root, error) {
	if !filepath.IsAbs(dir) {
		return Root{}, fmt.Errorf("hosthome: the homes' folder %q is not an absolute path", dir)
	}
	dir = filepath.Clean(dir)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return Root{}, fmt.Errorf("hosthome: %w", err)
	}
	root, err := open(dir)
	if err != nil {
		return Root{}, err
	}
	return Root{dir: dir}, root.Close()
}

// Dir is the root's folder.
func (r Root) Dir() string { return r.dir }

// Ensure returns the folder of owner's home, an absolute path, making it
// (mode 0700) when there is none. The root is checked again as Open
// checks it, and what already has the home's name must be as private as
// the root. owner must be a name a home's owner may have; whatever it
// holds, the folder is made through an os.Root of the root, which no name
// leaves.
func (r Root) Ensure(owner string) (string, error) {
	if err := (&model.Home{Spec: model.HomeSpec{Owner: owner}}).Validate(); err != nil {
		return "", fmt.Errorf("hosthome: %w", err)
	}
	name := naming.HomeClaimName(owner)
	root, err := open(r.dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	// Making a folder never follows a link that has its name.
	if err := root.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("hosthome: %w", err)
	}
	fi, err := root.Lstat(name)
	if err != nil {
		return "", fmt.Errorf("hosthome: %w", err)
	}
	dir := filepath.Join(r.dir, name)
	return dir, private(dir, fi)
}

// open opens dir as an os.Root once it is known to be private: the folder
// opened is the one that was looked at, not one swapped in since.
func open(dir string) (*os.Root, error) {
	seen, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("hosthome: %w", err)
	}
	if err := private(dir, seen); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("hosthome: %w", err)
	}
	if got, err := root.Stat("."); err != nil || !os.SameFile(seen, got) {
		root.Close()
		return nil, fmt.Errorf("hosthome: %s changed while it was opened", dir)
	}
	return root, nil
}

// private is nil when fi, what dir names without following a link, is a
// folder of the user Javo runs as that no one else can read, write or
// list.
func private(dir string, fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("hosthome: %s is a symbolic link", dir)
	case !fi.IsDir():
		return fmt.Errorf("hosthome: %s is not a folder", dir)
	case !ok || int(st.Uid) != os.Geteuid():
		return fmt.Errorf("hosthome: %s is not owned by the user Javo runs as", dir)
	case fi.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("hosthome: %s is open to others (mode %04o); it must be 0700", dir, fi.Mode().Perm())
	}
	return nil
}

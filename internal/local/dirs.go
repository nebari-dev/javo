//go:build linux || darwin

// Package local is what `javo local` decides for one person on their own
// machine, in place of the config file, the flags and the folders a
// `javo server` with executor process is given: where everything is kept,
// the settings, the one account, which pixi installs environments and
// which requests the gateway answers. The command (internal/cli/servercmd)
// only wires it; a binary built without the tag local links none of it.
package local

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Dirs is where `javo local` keeps everything: one folder, Root, that only
// the person can open, and in it the database, Javo's keys, what the apps
// run with (their logs, ports, secret files and environments), each
// person's home, and the platform's files as the binary unpacked them.
type Dirs struct {
	Root, Database, Keys, State, Homes, Files string
}

// DefaultRoot is the folder `javo local` keeps everything in unless told
// another: $XDG_DATA_HOME/javo, or ~/.local/share/javo when that is not
// set, on Linux and on macOS alike. It is the place for a user's own data
// that the XDG base directory specification names, which command line
// tools use on macOS too. Not ~/Library/Application Support: the scripts
// that install and activate environments write paths unquoted, and that
// folder's name has a space in it.
func DefaultRoot() (string, error) {
	if dir := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "javo"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("where to keep Javo's files: %w (name a folder with --dir)", err)
	}
	return filepath.Join(home, ".local", "share", "javo"), nil
}

// euid is the user Javo runs as.
var euid = os.Geteuid

// Prepare makes root, mode 0700, with the folders above it when they are
// missing, and the folder the platform's files are unpacked into; the
// others are made by what uses them, which checks each as strictly. It
// refuses root (uid 0) before anything is written, a path that is not
// absolute or has white space in it, and a root that is not a folder,
// that another user owns or that its group or others can open: the keys,
// the database and every script apps run are in it.
func Prepare(root string) (Dirs, error) {
	if euid() == 0 {
		return Dirs{}, errors.New("refusing to run as root (uid 0): apps run as the user Javo runs as, with nothing between them")
	}
	if !filepath.IsAbs(root) {
		return Dirs{}, fmt.Errorf("the folder %q is not an absolute path", root)
	}
	root = filepath.Clean(root)
	if strings.ContainsAny(root, " \t\n") {
		return Dirs{}, fmt.Errorf("the folder %q has white space in its path, which the scripts that install environments do not keep together: name another with --dir", root)
	}
	d := Dirs{
		Root: root, Database: filepath.Join(root, "javo.db"), Keys: filepath.Join(root, "keys"),
		State: filepath.Join(root, "state"), Homes: filepath.Join(root, "homes"), Files: filepath.Join(root, "files"),
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Dirs{}, err
	}
	if err := private(root); err != nil {
		return Dirs{}, err
	}
	if err := os.Mkdir(d.Files, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return Dirs{}, err
	}
	return d, private(d.Files)
}

// private refuses a path that is not a folder of this user that no one
// else can open.
func private(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ok || int(st.Uid) != euid() {
		return fmt.Errorf("%s must be a folder of this user that no one else can open (mode 0700): it holds Javo's keys and the scripts apps run", dir)
	}
	return nil
}

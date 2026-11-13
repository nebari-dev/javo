//go:build linux || darwin

package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheDefaultFolderIsTheUsersDataFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	if got, err := DefaultRoot(); err != nil || got != filepath.Join(home, ".local", "share", "javo") {
		t.Errorf("without XDG_DATA_HOME: %q, %v", got, err)
	}
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	if got, err := DefaultRoot(); err != nil || got != filepath.Join(data, "javo") {
		t.Errorf("with XDG_DATA_HOME: %q, %v", got, err)
	}
	// The specification has a relative path ignored.
	t.Setenv("XDG_DATA_HOME", "relative")
	if got, _ := DefaultRoot(); got != filepath.Join(home, ".local", "share", "javo") {
		t.Errorf("with a relative XDG_DATA_HOME: %q", got)
	}
}

func TestPrepareMakesFoldersOnlyThePersonCanOpen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "share", "javo")
	d, err := Prepare(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{d.Root, d.Files, filepath.Dir(d.Root)} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v, %v; want mode 0700", dir, info.Mode(), err)
		}
	}
	for what, dir := range map[string]string{"database": d.Database, "keys": d.Keys, "state": d.State, "homes": d.Homes, "files": d.Files} {
		if filepath.Dir(dir) != root {
			t.Errorf("the %s is at %s, outside %s", what, dir, root)
		}
	}
	// A second start finds them as it left them.
	if _, err := Prepare(root); err != nil {
		t.Errorf("a second start: %v", err)
	}
}

func TestPrepareRefusesWhatItCannotKeepPrivate(t *testing.T) {
	open := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	openFiles := filepath.Join(t.TempDir(), "javo")
	if err := os.MkdirAll(filepath.Join(openFiles, "files"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(openFiles, "files"), 0o770); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ root, want string }{
		"a folder others can open":          {open, "mode 0700"},
		"a files folder its group can open": {openFiles, "mode 0700"},
		"a file":                            {file, ""},
		"a symbolic link":                   {link, "mode 0700"},
		"a relative path":                   {"javo", "absolute"},
		"a path with a space":               {filepath.Join(t.TempDir(), "Application Support", "javo"), "white space"},
	} {
		if _, err := Prepare(c.root); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want it refused (%s)", name, err, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(link), "Application Support")); err == nil {
		t.Error("a refused path was made")
	}
}

func TestPrepareRefusesRootBeforeItWritesAnything(t *testing.T) {
	was := euid
	euid = func() int { return 0 }
	defer func() { euid = was }()
	root := filepath.Join(t.TempDir(), "javo")
	if _, err := Prepare(root); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("as root: %v", err)
	}
	if _, err := os.Stat(root); err == nil {
		t.Error("the folder was made as root")
	}
}

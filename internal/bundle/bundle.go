// Package bundle is the files `javo local` needs beside the binary, carried
// inside it: the built web UI, the built-in templates and the platform's
// files (pod/files, as the platform's image holds them). They are one zip
// archive that `make local` writes (internal/bundle/pack) and a binary built
// with the tag local embeds; any other binary carries none, and Open says
// so. A zip, because its files are compressed (Go embeds a folder as it is,
// three times the size) and each can be read by itself: the UI and the
// templates are served straight from the archive, and only the platform's
// files, which apps run scripts from, are written to disk.
package bundle

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// The folders of the archive.
const (
	UIDir        = "ui"
	TemplatesDir = "templates"
	FilesDir     = "files"
)

// ErrNone is what Open answers in a binary that carries no archive.
var ErrNone = errors.New("this javo was built without the files javo local serves (the web UI, the templates and the platform's files); build one that carries them with `make local`")

// Bundle is the archive, opened.
type Bundle struct {
	zip *zip.Reader
	// sum names the archive by its content: the folder its files are
	// unpacked into.
	sum string
}

// Open is the archive this binary carries, or ErrNone.
func Open() (*Bundle, error) { return open(archive()) }

func open(data []byte) (*Bundle, error) {
	if len(data) == 0 {
		return nil, ErrNone
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("the files this javo carries: %w", err)
	}
	sum := sha256.Sum256(data)
	return &Bundle{zip: r, sum: hex.EncodeToString(sum[:8])}, nil
}

// UI is the built web UI, for net/http to serve.
func (b *Bundle) UI() fs.FS { return seekable{b.sub(UIDir)} }

// Templates is the built-in template files.
func (b *Bundle) Templates() fs.FS { return b.sub(TemplatesDir) }

func (b *Bundle) sub(dir string) fs.FS {
	// The name is a constant and valid, the only thing Sub refuses.
	sub, _ := fs.Sub(b.zip, dir)
	return sub
}

// seekable is a file system whose files can be sought in, which net/http
// needs to serve one and a file of a zip archive cannot be: each is read
// whole when it is opened. Its files have no date, so a browser never takes
// a new binary's page for the one it has.
type seekable struct{ fs.FS }

func (s seekable) Open(name string) (fs.File, error) {
	f, err := s.FS.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return f, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return &memFile{Reader: bytes.NewReader(data), info: undated{info}}, nil
}

type memFile struct {
	*bytes.Reader
	info fs.FileInfo
}

func (f *memFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *memFile) Close() error               { return nil }

type undated struct{ fs.FileInfo }

func (undated) ModTime() time.Time { return time.Time{} }

// Unpack writes the platform's files into a folder of dir named after the
// archive's content and returns it. A folder of that name is there since
// an earlier start of the same binary and is left as it is; one of any
// other name is another binary's, and is removed. The files are written
// into a new folder that takes the name only when it is complete, each
// through an os.Root, which follows no path or link out of that folder; an
// entry that is not a plain file or a folder is refused. Folders are made
// 0700, files 0600, or 0700 when the archive marks them executable. dir
// must be a folder only this user can write in: apps run these scripts.
func (b *Bundle) Unpack(dir string) (string, error) {
	target := filepath.Join(dir, b.sum)
	if info, err := os.Lstat(target); err != nil || !info.IsDir() {
		if err := b.unpack(dir, target); err != nil {
			return "", fmt.Errorf("unpack the platform's files into %s: %w", dir, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Name() != b.sum {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return "", err
			}
		}
	}
	return target, nil
}

func (b *Bundle) unpack(dir, target string) error {
	tmp, err := os.MkdirTemp(dir, ".unpacking-") // mode 0700
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp) // nothing, once it has its name
	root, err := os.OpenRoot(tmp)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, f := range b.zip.File {
		name, ok := strings.CutPrefix(f.Name, FilesDir+"/")
		if name = strings.TrimSuffix(name, "/"); !ok || name == "" {
			continue
		}
		if err := write(root, name, f); err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	return os.Rename(tmp, target)
}

func write(root *os.Root, name string, f *zip.File) error {
	mode := f.Mode()
	switch {
	case !fs.ValidPath(name):
		return errors.New("not a path inside the folder")
	case mode.IsDir():
		return root.MkdirAll(name, 0o700)
	case !mode.IsRegular():
		return errors.New("neither a file nor a folder")
	}
	if err := root.MkdirAll(path.Dir(name), 0o700); err != nil {
		return err
	}
	perm := fs.FileMode(0o600)
	if mode&0o111 != 0 {
		perm = 0o700
	}
	in, err := f.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

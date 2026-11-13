package files_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/files"
)

// testHome is a home a test fills, and the Remote that serves it. The
// Lister's tests run over each kind, so what it promises holds whichever
// way a home is reached.
type testHome interface {
	files.Remote
	// addr is what the file pod manager answers for the home.
	addr() string
	// tree is a copy of the home's files, by path.
	tree() map[string][]byte
	// leftovers are the staging and old folders an upload left behind.
	leftovers() []string
	// link puts a symbolic link that leads out of the home at rel.
	link(rel string)
	// escaped reports whether a call went through a link.
	escaped() bool
}

// eachHome runs test over a home holding tree (files by path) of each
// kind: behind a file pod, as rclone serves it (memRemote, the tests'
// model of rclone), and a folder of this machine served by files.Direct.
func eachHome(t *testing.T, tree map[string][]byte, test func(t *testing.T, h testHome)) {
	t.Helper()
	t.Run("file pod", func(t *testing.T) { test(t, newMemRemote(maps.Clone(tree))) })
	t.Run("folder", func(t *testing.T) { test(t, newDiskRemote(t, tree)) })
}

// listerOf is a Lister over h.
func listerOf(h testHome) *files.Lister {
	return files.NewLister(stubEnsurer{addr: h.addr()}, h)
}

// memRemote is a home held in memory, as rclone serves one: files by
// path, folders implied by them, and links (symlinks) that no listing
// shows, that stat reports as non-folders and that every other call
// follows out of the home.
type memRemote struct {
	mu    sync.Mutex
	files map[string][]byte
	// mods is when each file was last written; writes counts them, so
	// every write has its own time.
	mods   map[string]time.Time
	writes int
	links  []string
	// followed is set when a call went through a link: rclone would have
	// followed it out of the home.
	followed bool
}

func newMemRemote(files map[string][]byte) *memRemote {
	if files == nil {
		files = map[string][]byte{}
	}
	return &memRemote{files: files, mods: map[string]time.Time{}}
}

func (m *memRemote) addr() string { return "10.0.0.5:5572" }

func (m *memRemote) link(rel string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.links = append(m.links, rel)
}

func (m *memRemote) escaped() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.followed
}

// through reports whether p is a link or inside one, and if so notes it.
// The caller holds mu.
func (m *memRemote) through(p string) bool {
	for _, l := range m.links {
		if p == l || strings.HasPrefix(p, l+"/") {
			m.followed = true
			return true
		}
	}
	return false
}

// entry is the file at p. The caller holds mu.
func (m *memRemote) entry(p string) files.Entry {
	return files.Entry{Name: path.Base(p), Size: int64(len(m.files[p])), ModTime: mod.Add(m.mods[p].Sub(time.Time{}))}
}

// isDir reports whether files lie under p. The caller holds mu.
func (m *memRemote) isDir(p string) bool {
	if p == "" {
		return true
	}
	for f := range m.files {
		if strings.HasPrefix(f, p+"/") {
			return true
		}
	}
	return false
}

func (m *memRemote) Stat(_ context.Context, _, _ string, remote string) (files.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if slices.Contains(m.links, remote) {
		return files.Entry{Name: path.Base(remote)}, nil
	}
	if _, ok := m.files[remote]; ok {
		return m.entry(remote), nil
	}
	if m.isDir(remote) {
		return files.Entry{Name: path.Base(remote), IsDir: true}, nil
	}
	return files.Entry{}, files.ErrNotFound
}

func (m *memRemote) List(_ context.Context, _, _ string, remote string) (files.Listing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.through(remote) || !m.isDir(remote) {
		return files.Listing{}, files.ErrNotFound
	}
	out := files.Listing{Entries: []files.Entry{}}
	seen := map[string]bool{}
	for _, p := range slices.Sorted(maps.Keys(m.files)) {
		rest, ok := strings.CutPrefix(p, remote+"/")
		if remote == "" {
			rest, ok = p, true
		}
		name, _, deeper := strings.Cut(rest, "/")
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		if deeper {
			out.Entries = append(out.Entries, files.Entry{Name: name, IsDir: true})
		} else {
			out.Entries = append(out.Entries, m.entry(p))
		}
	}
	return out, nil
}

func (m *memRemote) FindFile(_ context.Context, _ string, dir, name string) (files.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := path.Join(dir, name)
	if _, ok := m.files[p]; !ok || m.through(dir) {
		return files.Entry{}, files.ErrNotFound
	}
	return m.entry(p), nil
}

func (m *memRemote) Open(_ context.Context, _ string, remote string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.through(remote) {
		return io.NopCloser(strings.NewReader(secret)), nil
	}
	data, ok := m.files[remote]
	if !ok {
		return nil, files.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memRemote) Upload(ctx context.Context, addr, dir, name string, data []byte) error {
	return m.UploadStream(ctx, addr, dir, name, bytes.NewReader(data))
}

func (m *memRemote) UploadStream(_ context.Context, _, dir, name string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p := path.Join(dir, name)
	if m.through(p) {
		return nil
	}
	m.writes++
	m.files[p], m.mods[p] = data, time.Time{}.Add(time.Duration(m.writes))
	return nil
}

func (m *memRemote) MoveDir(_ context.Context, _, src, dst string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.through(src) || m.through(dst) {
		return nil
	}
	moved := false
	for p, b := range m.files {
		if rest, ok := strings.CutPrefix(p, src+"/"); ok {
			delete(m.files, p)
			m.files[dst+"/"+rest] = b
			moved = true
		}
	}
	if !moved {
		return files.ErrNotFound
	}
	return nil
}

func (m *memRemote) Purge(_ context.Context, _, dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.through(dir) {
		return nil
	}
	for p := range m.files {
		if strings.HasPrefix(p, dir+"/") {
			delete(m.files, p)
		}
	}
	return nil
}

func (m *memRemote) About(context.Context, string) (files.Usage, error) {
	return files.Usage{Used: 12992283648, Total: 21474836480}, nil
}

func (m *memRemote) tree() map[string][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.files)
}

func (m *memRemote) leftovers() []string {
	var out []string
	for p := range m.tree() {
		if isLeftover(p) {
			out = append(out, p)
		}
	}
	return out
}

// isLeftover reports whether p is, or is inside, a staging or old folder
// of an upload.
func isLeftover(p string) bool {
	return strings.Contains(p, ".upload-") || strings.Contains(p, ".old-")
}

// diskRemote is a home that is a folder of this machine, served by
// files.Direct, with a folder beside it that links lead to.
type diskRemote struct {
	files.Direct
	t             *testing.T
	home, outside string
}

func newDiskRemote(t *testing.T, tree map[string][]byte) *diskRemote {
	home, outside := newDiskHome(t, tree)
	return &diskRemote{t: t, home: home, outside: outside}
}

func (d *diskRemote) addr() string { return d.home }

func (d *diskRemote) link(rel string) {
	d.t.Helper()
	must(d.t, os.MkdirAll(filepath.Dir(filepath.Join(d.home, rel)), 0o755))
	must(d.t, os.Symlink(d.outside, filepath.Join(d.home, rel)))
}

// escaped reports whether the folder the links lead to changed.
func (d *diskRemote) escaped() bool {
	entries, err := os.ReadDir(d.outside)
	got, rerr := os.ReadFile(filepath.Join(d.outside, "secret.txt"))
	return err != nil || rerr != nil || len(entries) != 1 || string(got) != secret
}

// walk calls see for every entry under the home, without following links.
func (d *diskRemote) walk(see func(rel string, fi os.FileInfo)) {
	d.t.Helper()
	must(d.t, filepath.Walk(d.home, func(p string, fi os.FileInfo, err error) error {
		if rel, _ := filepath.Rel(d.home, p); err == nil && rel != "." {
			see(filepath.ToSlash(rel), fi)
		}
		return err
	}))
}

func (d *diskRemote) tree() map[string][]byte {
	d.t.Helper()
	out := map[string][]byte{}
	d.walk(func(rel string, fi os.FileInfo) {
		if fi.Mode().IsRegular() {
			data, err := os.ReadFile(filepath.Join(d.home, rel))
			must(d.t, err)
			out[rel] = data
		}
	})
	return out
}

func (d *diskRemote) leftovers() []string {
	d.t.Helper()
	var out []string
	d.walk(func(rel string, _ os.FileInfo) {
		if isLeftover(rel) {
			out = append(out, rel)
		}
	})
	return out
}

// failMoves is a home where a move whose source contains from fails.
type failMoves struct {
	testHome
	from string
}

func (f failMoves) MoveDir(ctx context.Context, addr, src, dst string) error {
	if strings.Contains(src, f.from) {
		return errors.New("failMoves: move failed")
	}
	return f.testHome.MoveDir(ctx, addr, src, dst)
}

// gated is a home that holds every upload until release is closed, after
// telling entered.
type gated struct {
	testHome
	entered, release chan struct{}
}

func gate(h testHome) gated {
	return gated{h, make(chan struct{}, 2), make(chan struct{})}
}

func (g gated) Upload(ctx context.Context, addr, dir, name string, data []byte) error {
	g.entered <- struct{}{}
	<-g.release
	return g.testHome.Upload(ctx, addr, dir, name, data)
}

func (g gated) UploadStream(ctx context.Context, addr, dir, name string, r io.Reader) error {
	g.entered <- struct{}{}
	<-g.release
	return g.testHome.UploadStream(ctx, addr, dir, name, r)
}

// cut is a home whose listings hold only the first entry of a folder with
// several, and say so, like a folder past files.MaxEntries; every other
// call sees the whole home.
type cut struct{ testHome }

func (c cut) List(ctx context.Context, addr, fs, remote string) (files.Listing, error) {
	l, err := c.testHome.List(ctx, addr, fs, remote)
	if err == nil && len(l.Entries) > 1 {
		l.Entries, l.Truncated = l.Entries[:1], true
	}
	return l, err
}

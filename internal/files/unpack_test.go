package files_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/files"
)

// memRemote is a home held in memory: files by path, folders implied by
// them, and links (symlinks) that stat reports as non-folders.
type memRemote struct {
	stubRemote
	mu    sync.Mutex
	files map[string][]byte
	links []string
	// failMoveFrom fails a move whose source contains it.
	failMoveFrom string
	// gate, when set, holds every upload until it is closed, after
	// telling entered.
	gate, entered chan struct{}
	// followed is set when a call went through a link: rclone would have
	// followed it out of the home.
	followed bool
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

func newMemRemote(files map[string][]byte) *memRemote {
	if files == nil {
		files = map[string][]byte{}
	}
	return &memRemote{files: files}
}

func (m *memRemote) Stat(_ context.Context, _, _ string, remote string) (files.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := remote[strings.LastIndex(remote, "/")+1:]
	if slices.Contains(m.links, remote) {
		return files.Entry{Name: name}, nil
	}
	if b, ok := m.files[remote]; ok {
		return files.Entry{Name: name, Size: int64(len(b))}, nil
	}
	for p := range m.files {
		if strings.HasPrefix(p, remote+"/") {
			return files.Entry{Name: name, IsDir: true}, nil
		}
	}
	return files.Entry{}, files.ErrNotFound
}

func (m *memRemote) UploadStream(_ context.Context, _, dir, name string, r io.Reader) error {
	if m.gate != nil {
		m.entered <- struct{}{}
		<-m.gate
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.through(dir) {
		return nil
	}
	m.files[dir+"/"+name] = data
	return nil
}

func (m *memRemote) MoveDir(_ context.Context, _, src, dst string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failMoveFrom != "" && strings.Contains(src, m.failMoveFrom) {
		return errors.New("memRemote: move failed")
	}
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

// tree is a copy of the home's files.
func (m *memRemote) tree() map[string][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.files)
}

// leftovers are the paths of the home inside a staging or old folder.
func (m *memRemote) leftovers() []string {
	var out []string
	for p := range m.tree() {
		if strings.Contains(p, ".upload-") || strings.Contains(p, ".old-") {
			out = append(out, p)
		}
	}
	return out
}

// entry is one member of a test archive.
type entry struct {
	name string
	typ  byte
	body string
	link string
}

func file(name, body string) entry { return entry{name: name, typ: tar.TypeReg, body: body} }

// tgz is a gzipped tar of entries.
func tgz(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644, Size: int64(len(e.body)), Linkname: e.link}
		if e.typ == tar.TypeXGlobalHeader {
			// As git archive writes it: the commit the archive was made from.
			h = &tar.Header{Typeflag: e.typ, PAXRecords: map[string]string{"comment": "0123456789abcdef0123456789abcdef01234567"}}
		}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// unpack unpacks archive into rel with a limit of bytes and 10000 entries.
func unpack(l *files.Lister, rel string, archive []byte, bytesLimit int64, replace bool) (files.Unpacked, error) {
	return l.Unpack(context.Background(), owner, rel, bytes.NewReader(archive), files.UploadLimits{Bytes: bytesLimit, Entries: 10000}, replace)
}

func TestUnpackWritesFiles(t *testing.T) {
	m := newMemRemote(nil)
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
	bin := "a\x00b"
	got, err := unpack(l, "deploys/site", tgz(t,
		entry{name: "./", typ: tar.TypeDir},
		file("./app.py", "print(1)\n"),
		entry{name: "static/", typ: tar.TypeDir},
		file("static/img/logo.png", bin),
	), 1<<20, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != (files.Unpacked{Path: "deploys/site", Files: 2, Bytes: int64(len("print(1)\n") + len(bin))}) {
		t.Errorf("Unpacked = %+v", got)
	}
	want := map[string][]byte{"deploys/site/app.py": []byte("print(1)\n"), "deploys/site/static/img/logo.png": []byte(bin)}
	if tree := m.tree(); !maps.EqualFunc(tree, want, bytes.Equal) {
		t.Fatalf("home = %q", tree)
	}
}

// assertNothingWritten fails unless the home is still before and holds no
// staging folder.
func assertNothingWritten(t *testing.T, m *memRemote, before map[string][]byte) {
	t.Helper()
	if m.followed {
		t.Error("went through a link")
	}
	if left := m.leftovers(); len(left) > 0 {
		t.Errorf("staging left behind: %v", left)
	}
	if tree := m.tree(); !maps.EqualFunc(tree, before, bytes.Equal) {
		t.Errorf("home = %q, want %q", tree, before)
	}
}

// An entry that would land outside the folder, or that is not a plain file
// or folder, fails the whole upload, named in the error.
func TestUploadRefusesEscapes(t *testing.T) {
	for _, bad := range []entry{
		file("/etc/x", "x"),
		file("../x", "x"),
		file("a/../../x", "x"),
		file("a/../b/../../x", "x"),
		file("a/../b.py", "x"), // stays inside, but no ".." at all
		file(".", "x"),         // a file in place of the folder itself
		{name: "link", typ: tar.TypeSymlink, link: "/etc/passwd"},
		{name: "hard", typ: tar.TypeLink, link: "app.py"},
		{name: "tty", typ: tar.TypeChar},
		{name: "disk", typ: tar.TypeBlock},
		{name: "pipe", typ: tar.TypeFifo},
	} {
		t.Run(bad.name, func(t *testing.T) {
			m := newMemRemote(map[string][]byte{"notes/a.txt": []byte("keep")})
			before := m.tree()
			l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
			_, err := unpack(l, "deploys/site", tgz(t, file("app.py", "print(1)\n"), bad, file("z.py", "z")), 1<<20, false)
			var be *files.BadEntryError
			if !errors.Is(err, files.ErrBadArchive) || !errors.As(err, &be) || be.Name != bad.name {
				t.Fatalf("err = %v, want ErrBadArchive naming %q", err, bad.name)
			}
			assertNothingWritten(t, m, before)
		})
	}
}

// The limit is on the files' bytes together, not one file's.
func TestUnpackTooLarge(t *testing.T) {
	for name, archive := range map[string][]byte{
		"one file":  tgz(t, file("a.txt", "12345678901")),
		"two files": tgz(t, file("a.txt", "12345"), file("b.txt", "123456")),
	} {
		t.Run(name, func(t *testing.T) {
			m := newMemRemote(nil)
			l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
			if _, err := unpack(l, "deploys/site", archive, 10, false); !errors.Is(err, files.ErrTooLarge) {
				t.Fatalf("err = %v, want ErrTooLarge", err)
			}
			assertNothingWritten(t, m, map[string][]byte{})
		})
	}
	m := newMemRemote(nil)
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
	if _, err := unpack(l, "deploys/site", tgz(t, file("a.txt", "12345"), file("b.txt", "12345")), 10, false); err != nil {
		t.Fatalf("10 bytes in a limit of 10: %v", err)
	}
}

func TestUnpackExistsWithoutReplace(t *testing.T) {
	for name, home := range map[string]map[string][]byte{
		"a folder": {"deploys/site/old.py": []byte("old")},
		"a file":   {"deploys/site": []byte("old")},
	} {
		t.Run(name, func(t *testing.T) {
			m := newMemRemote(home)
			before := m.tree()
			l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
			if _, err := unpack(l, "deploys/site", tgz(t, file("app.py", "new")), 1<<20, false); !errors.Is(err, files.ErrExists) {
				t.Fatalf("err = %v, want ErrExists", err)
			}
			assertNothingWritten(t, m, before)
		})
	}
}

// A symlink on the way, or in place of the folder, is never written
// through or replaced: rclone would follow it out of the home.
func TestUnpackNeverWritesThroughALink(t *testing.T) {
	for _, tc := range []struct {
		name, link string
		replace    bool
	}{
		{"parent is a link", "deploys", false},
		{"parent is a link, replacing", "deploys", true},
		{"folder is a link", "deploys/site", false},
		{"folder is a link, replacing", "deploys/site", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMemRemote(map[string][]byte{"notes/a.txt": []byte("keep")})
			m.links = []string{tc.link}
			before := m.tree()
			l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
			if _, err := unpack(l, "deploys/site", tgz(t, file("app.py", "new")), 1<<20, tc.replace); err == nil {
				t.Fatal("unpacked through a link")
			}
			assertNothingWritten(t, m, before)
		})
	}
}

// A redeploy: the folder's old files are gone, the new ones in place.
func TestUnpackReplaceSwaps(t *testing.T) {
	m := newMemRemote(map[string][]byte{"deploys/site/old.py": []byte("old"), "deploys/site/app.py": []byte("v1")})
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
	got, err := unpack(l, "deploys/site", tgz(t, file("app.py", "v2")), 1<<20, true)
	if err != nil || !got.Replaced {
		t.Fatalf("Unpacked = %+v, %v; want Replaced", got, err)
	}
	if tree := m.tree(); !maps.EqualFunc(tree, map[string][]byte{"deploys/site/app.py": []byte("v2")}, bytes.Equal) {
		t.Fatalf("home = %q", tree)
	}
}

// Replacing with a bad archive keeps the old folder as it was.
func TestUnpackFailedReplaceKeepsOld(t *testing.T) {
	home := map[string][]byte{"deploys/site/app.py": []byte("v1")}
	m := newMemRemote(home)
	before := m.tree()
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
	bad := tgz(t, file("app.py", "v2"), entry{name: "link", typ: tar.TypeSymlink, link: "/etc/passwd"})
	if _, err := unpack(l, "deploys/site", bad, 1<<20, true); !errors.Is(err, files.ErrBadArchive) {
		t.Fatalf("err = %v, want ErrBadArchive", err)
	}
	assertNothingWritten(t, m, before)
}

// When the new folder cannot be moved in, the old one is moved back.
func TestUnpackFailedSwapPutsOldBack(t *testing.T) {
	m := newMemRemote(map[string][]byte{"deploys/site/app.py": []byte("v1")})
	m.failMoveFrom = ".upload-"
	before := m.tree()
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
	if _, err := unpack(l, "deploys/site", tgz(t, file("app.py", "v2")), 1<<20, true); err == nil {
		t.Fatal("swap failed, but Unpack did not say so")
	}
	assertNothingWritten(t, m, before)
}

// An archive without a single file makes no folder.
func TestUploadEmptyArchive(t *testing.T) {
	for name, archive := range map[string][]byte{
		"nothing":      tgz(t),
		"only folders": tgz(t, entry{name: "./", typ: tar.TypeDir}, entry{name: "a/", typ: tar.TypeDir}),
	} {
		t.Run(name, func(t *testing.T) {
			m := newMemRemote(nil)
			l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
			if _, err := unpack(l, "deploys/site", archive, 1<<20, false); !errors.Is(err, files.ErrEmptyArchive) {
				t.Fatalf("err = %v, want ErrEmptyArchive", err)
			}
			assertNothingWritten(t, m, map[string][]byte{})
		})
	}
}

func TestUnpackNotGzip(t *testing.T) {
	var plainTar bytes.Buffer
	tw := tar.NewWriter(&plainTar)
	_ = tw.WriteHeader(&tar.Header{Name: "a.txt", Typeflag: tar.TypeReg, Size: 1})
	_, _ = tw.Write([]byte("a"))
	_ = tw.Close()
	var gzText bytes.Buffer
	zw := gzip.NewWriter(&gzText)
	_, _ = zw.Write(bytes.Repeat([]byte("not a tar "), 100))
	_ = zw.Close()
	good := tgz(t, file("a.txt", "a"), file("b.txt", "b"))
	for name, body := range map[string][]byte{
		"text":         []byte("hello"),
		"tar, no gzip": plainTar.Bytes(),
		"gzip, no tar": gzText.Bytes(),
		"cut off":      good[:len(good)/2],
		"empty":        nil,
	} {
		t.Run(name, func(t *testing.T) {
			m := newMemRemote(nil)
			l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
			if _, err := unpack(l, "deploys/site", body, 1<<20, false); !errors.Is(err, files.ErrBadArchive) {
				t.Fatalf("err = %v, want ErrBadArchive", err)
			}
			assertNothingWritten(t, m, map[string][]byte{})
		})
	}
}

// failingReader gives err once its data is read.
type failingReader struct {
	data []byte
	err  error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

// When reading the upload itself fails (the body is over its limit, the
// client went away), that is the error, not a bad archive.
func TestUnpackPassesOnTheBodysError(t *testing.T) {
	big := make([]byte, 64<<10)
	for i := range big {
		big[i] = byte(i * 7)
	}
	good := tgz(t, file("a.bin", string(big)), file("b.bin", string(big)))
	for _, cut := range []int{5, len(good) / 2} {
		m := newMemRemote(nil)
		l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
		body := &failingReader{data: good[:cut], err: files.ErrTooLarge}
		_, err := l.Unpack(context.Background(), owner, "deploys/site", body, files.UploadLimits{Bytes: 1 << 20, Entries: 10000}, false)
		if !errors.Is(err, files.ErrTooLarge) || errors.Is(err, files.ErrBadArchive) {
			t.Fatalf("cut at %d: err = %v, want ErrTooLarge only", cut, err)
		}
		assertNothingWritten(t, m, map[string][]byte{})
	}
}

// Two uploads of the same new folder at once: one makes it, the other is
// told it exists, never the two merged.
func TestUnpackOfTheSameFolderAtOnceWritesOnlyOne(t *testing.T) {
	m := newMemRemote(nil)
	m.gate, m.entered = make(chan struct{}), make(chan struct{}, 2)
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
	errs := make(chan error, 2)
	up := func(name string) {
		_, err := unpack(l, "deploys/site", tgz(t, file(name, "x")), 1<<20, false)
		errs <- err
	}
	go up("first.py")
	<-m.entered
	go up("second.py")
	time.Sleep(50 * time.Millisecond) // the second upload gets as far as it can
	close(m.gate)
	var exists, ok int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, files.ErrExists):
			exists++
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || exists != 1 {
		t.Fatalf("%d written, %d refused; want one each", ok, exists)
	}
	if tree := m.tree(); len(tree) != 1 {
		t.Fatalf("home = %q", tree)
	}
}

// git archive starts with a pax global header: metadata, not a file.
func TestUnpackSkipsAGlobalHeader(t *testing.T) {
	m := newMemRemote(nil)
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
	got, err := unpack(l, "deploys/site", tgz(t, entry{name: "pax_global_header", typ: tar.TypeXGlobalHeader}, file("app.py", "x")), 1<<20, false)
	if err != nil || got.Files != 1 {
		t.Fatalf("Unpacked = %+v, %v", got, err)
	}
	if tree := m.tree(); !maps.EqualFunc(tree, map[string][]byte{"deploys/site/app.py": []byte("x")}, bytes.Equal) {
		t.Fatalf("home = %q", tree)
	}
}

// Every header counts, folders and global headers too: empty files cost no
// bytes, but each is a write to the home.
func TestUnpackTooManyEntries(t *testing.T) {
	limits := files.UploadLimits{Bytes: 1 << 20, Entries: 3}
	for name, archive := range map[string][]byte{
		"empty files": tgz(t, file("a", ""), file("b", ""), file("c", ""), file("d", "")),
		"with a folder and a global header": tgz(t, entry{name: "pax_global_header", typ: tar.TypeXGlobalHeader},
			entry{name: "./", typ: tar.TypeDir}, file("a", ""), file("b", "")),
	} {
		t.Run(name, func(t *testing.T) {
			m := newMemRemote(nil)
			l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
			_, err := l.Unpack(context.Background(), owner, "deploys/site", bytes.NewReader(archive), limits, false)
			if !errors.Is(err, files.ErrTooManyEntries) {
				t.Fatalf("err = %v, want ErrTooManyEntries", err)
			}
			assertNothingWritten(t, m, map[string][]byte{})
		})
	}
	m := newMemRemote(nil)
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, m)
	three := tgz(t, entry{name: "./", typ: tar.TypeDir}, file("a", ""), file("b", ""))
	if _, err := l.Unpack(context.Background(), owner, "deploys/site", bytes.NewReader(three), limits, false); err != nil {
		t.Fatalf("3 entries in a limit of 3: %v", err)
	}
}

package files_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/files"
)

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
	eachHome(t, nil, func(t *testing.T, h testHome) {
		bin := "a\x00b"
		got, err := unpack(listerOf(h), "deploys/site", tgz(t,
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
		if tree := h.tree(); !maps.EqualFunc(tree, want, bytes.Equal) {
			t.Fatalf("home = %q", tree)
		}
		if left := h.leftovers(); len(left) > 0 {
			t.Errorf("staging left behind: %v", left)
		}
	})
}

// assertNothingWritten fails unless the home is still before and holds no
// staging folder.
func assertNothingWritten(t *testing.T, h testHome, before map[string][]byte) {
	t.Helper()
	if h.escaped() {
		t.Error("went through a link")
	}
	if left := h.leftovers(); len(left) > 0 {
		t.Errorf("staging left behind: %v", left)
	}
	if tree := h.tree(); !maps.EqualFunc(tree, before, bytes.Equal) {
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
			eachHome(t, map[string][]byte{"notes/a.txt": []byte("keep")}, func(t *testing.T, h testHome) {
				before := h.tree()
				_, err := unpack(listerOf(h), "deploys/site", tgz(t, file("app.py", "print(1)\n"), bad, file("z.py", "z")), 1<<20, false)
				var be *files.BadEntryError
				if !errors.Is(err, files.ErrBadArchive) || !errors.As(err, &be) || be.Name != bad.name {
					t.Fatalf("err = %v, want ErrBadArchive naming %q", err, bad.name)
				}
				assertNothingWritten(t, h, before)
			})
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
			eachHome(t, nil, func(t *testing.T, h testHome) {
				if _, err := unpack(listerOf(h), "deploys/site", archive, 10, false); !errors.Is(err, files.ErrTooLarge) {
					t.Fatalf("err = %v, want ErrTooLarge", err)
				}
				assertNothingWritten(t, h, map[string][]byte{})
			})
		})
	}
	eachHome(t, nil, func(t *testing.T, h testHome) {
		if _, err := unpack(listerOf(h), "deploys/site", tgz(t, file("a.txt", "12345"), file("b.txt", "12345")), 10, false); err != nil {
			t.Fatalf("10 bytes in a limit of 10: %v", err)
		}
	})
}

func TestUnpackExistsWithoutReplace(t *testing.T) {
	for name, home := range map[string]map[string][]byte{
		"a folder": {"deploys/site/old.py": []byte("old")},
		"a file":   {"deploys/site": []byte("old")},
	} {
		t.Run(name, func(t *testing.T) {
			eachHome(t, home, func(t *testing.T, h testHome) {
				before := h.tree()
				if _, err := unpack(listerOf(h), "deploys/site", tgz(t, file("app.py", "new")), 1<<20, false); !errors.Is(err, files.ErrExists) {
					t.Fatalf("err = %v, want ErrExists", err)
				}
				assertNothingWritten(t, h, before)
			})
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
			eachHome(t, map[string][]byte{"notes/a.txt": []byte("keep")}, func(t *testing.T, h testHome) {
				h.link(tc.link)
				before := h.tree()
				if _, err := unpack(listerOf(h), "deploys/site", tgz(t, file("app.py", "new")), 1<<20, tc.replace); err == nil {
					t.Fatal("unpacked through a link")
				}
				assertNothingWritten(t, h, before)
			})
		})
	}
}

// A redeploy: the folder's old files are gone, the new ones in place.
func TestUnpackReplaceSwaps(t *testing.T) {
	home := map[string][]byte{"deploys/site/old.py": []byte("old"), "deploys/site/app.py": []byte("v1")}
	eachHome(t, home, func(t *testing.T, h testHome) {
		got, err := unpack(listerOf(h), "deploys/site", tgz(t, file("app.py", "v2")), 1<<20, true)
		if err != nil || !got.Replaced {
			t.Fatalf("Unpacked = %+v, %v; want Replaced", got, err)
		}
		assertNothingWritten(t, h, map[string][]byte{"deploys/site/app.py": []byte("v2")})
	})
}

// Replacing with a bad archive keeps the old folder as it was.
func TestUnpackFailedReplaceKeepsOld(t *testing.T) {
	eachHome(t, map[string][]byte{"deploys/site/app.py": []byte("v1")}, func(t *testing.T, h testHome) {
		before := h.tree()
		bad := tgz(t, file("app.py", "v2"), entry{name: "link", typ: tar.TypeSymlink, link: "/etc/passwd"})
		if _, err := unpack(listerOf(h), "deploys/site", bad, 1<<20, true); !errors.Is(err, files.ErrBadArchive) {
			t.Fatalf("err = %v, want ErrBadArchive", err)
		}
		assertNothingWritten(t, h, before)
	})
}

// When the new folder cannot be moved in, the old one is moved back.
func TestUnpackFailedSwapPutsOldBack(t *testing.T) {
	eachHome(t, map[string][]byte{"deploys/site/app.py": []byte("v1")}, func(t *testing.T, h testHome) {
		before := h.tree()
		l := files.NewLister(stubEnsurer{addr: h.addr()}, failMoves{h, ".upload-"})
		if _, err := unpack(l, "deploys/site", tgz(t, file("app.py", "v2")), 1<<20, true); err == nil {
			t.Fatal("swap failed, but Unpack did not say so")
		}
		assertNothingWritten(t, h, before)
	})
}

// An archive without a single file makes no folder.
func TestUploadEmptyArchive(t *testing.T) {
	for name, archive := range map[string][]byte{
		"nothing":      tgz(t),
		"only folders": tgz(t, entry{name: "./", typ: tar.TypeDir}, entry{name: "a/", typ: tar.TypeDir}),
	} {
		t.Run(name, func(t *testing.T) {
			eachHome(t, nil, func(t *testing.T, h testHome) {
				if _, err := unpack(listerOf(h), "deploys/site", archive, 1<<20, false); !errors.Is(err, files.ErrEmptyArchive) {
					t.Fatalf("err = %v, want ErrEmptyArchive", err)
				}
				assertNothingWritten(t, h, map[string][]byte{})
			})
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
			eachHome(t, nil, func(t *testing.T, h testHome) {
				if _, err := unpack(listerOf(h), "deploys/site", body, 1<<20, false); !errors.Is(err, files.ErrBadArchive) {
					t.Fatalf("err = %v, want ErrBadArchive", err)
				}
				assertNothingWritten(t, h, map[string][]byte{})
			})
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
		eachHome(t, nil, func(t *testing.T, h testHome) {
			body := &failingReader{data: good[:cut], err: files.ErrTooLarge}
			_, err := listerOf(h).Unpack(context.Background(), owner, "deploys/site", body, files.UploadLimits{Bytes: 1 << 20, Entries: 10000}, false)
			if !errors.Is(err, files.ErrTooLarge) || errors.Is(err, files.ErrBadArchive) {
				t.Fatalf("cut at %d: err = %v, want ErrTooLarge only", cut, err)
			}
			assertNothingWritten(t, h, map[string][]byte{})
		})
	}
}

// Two uploads of the same new folder at once: one makes it, the other is
// told it exists, never the two merged.
func TestUnpackOfTheSameFolderAtOnceWritesOnlyOne(t *testing.T) {
	eachHome(t, nil, func(t *testing.T, h testHome) {
		g := gate(h)
		l := files.NewLister(stubEnsurer{addr: h.addr()}, g)
		errs := make(chan error, 2)
		up := func(name string) {
			_, err := unpack(l, "deploys/site", tgz(t, file(name, "x")), 1<<20, false)
			errs <- err
		}
		go up("first.py")
		<-g.entered
		go up("second.py")
		time.Sleep(50 * time.Millisecond) // the second upload gets as far as it can
		close(g.release)
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
		if tree := h.tree(); len(tree) != 1 {
			t.Fatalf("home = %q", tree)
		}
	})
}

// git archive starts with a pax global header: metadata, not a file.
func TestUnpackSkipsAGlobalHeader(t *testing.T) {
	eachHome(t, nil, func(t *testing.T, h testHome) {
		got, err := unpack(listerOf(h), "deploys/site", tgz(t, entry{name: "pax_global_header", typ: tar.TypeXGlobalHeader}, file("app.py", "x")), 1<<20, false)
		if err != nil || got.Files != 1 {
			t.Fatalf("Unpacked = %+v, %v", got, err)
		}
		if tree := h.tree(); !maps.EqualFunc(tree, map[string][]byte{"deploys/site/app.py": []byte("x")}, bytes.Equal) {
			t.Fatalf("home = %q", tree)
		}
	})
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
			eachHome(t, nil, func(t *testing.T, h testHome) {
				_, err := listerOf(h).Unpack(context.Background(), owner, "deploys/site", bytes.NewReader(archive), limits, false)
				if !errors.Is(err, files.ErrTooManyEntries) {
					t.Fatalf("err = %v, want ErrTooManyEntries", err)
				}
				assertNothingWritten(t, h, map[string][]byte{})
			})
		})
	}
	eachHome(t, nil, func(t *testing.T, h testHome) {
		three := tgz(t, entry{name: "./", typ: tar.TypeDir}, file("a", ""), file("b", ""))
		if _, err := listerOf(h).Unpack(context.Background(), owner, "deploys/site", bytes.NewReader(three), limits, false); err != nil {
			t.Fatalf("3 entries in a limit of 3: %v", err)
		}
	})
}

// paxHeaders is a gzipped tar of n pax extended headers of about size
// bytes each, one after another: metadata archive/tar reads inside Next
// without ever returning an entry.
func paxHeaders(t *testing.T, n, size int) []byte {
	t.Helper()
	value := strings.Repeat("a", size)
	record := ""
	for l := len(value); ; l++ {
		record = fmt.Sprintf("%d comment=%s\n", l, value)
		if len(record) == l {
			break
		}
	}
	var raw bytes.Buffer
	for range n {
		var one bytes.Buffer
		tw := tar.NewWriter(&one)
		if err := tw.WriteHeader(&tar.Header{Name: "PaxHeader", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(record))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(record)); err != nil {
			t.Fatal(err)
		}
		if err := tw.Flush(); err != nil {
			t.Fatal(err)
		}
		block := one.Bytes()
		// Make the file's header a pax extended header, with its checksum.
		block[156] = tar.TypeXHeader
		copy(block[148:156], "        ")
		sum := 0
		for _, b := range block[:512] {
			sum += int(b)
		}
		copy(block[148:156], fmt.Sprintf("%06o\x00 ", sum))
		raw.Write(block)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(raw.Bytes())
	_ = zw.Close()
	return buf.Bytes()
}

// A small upload that inflates to a long run of metadata stops at
// limits.Bytes plus 64 KiB per allowed entry, as too large.
func TestUnpackCapsTheInflatedStream(t *testing.T) {
	archive := paxHeaders(t, 200, 60<<10) // about 12 MiB of headers
	if len(archive) > 1<<20 {
		t.Fatalf("test archive is %d bytes; want a small one", len(archive))
	}
	eachHome(t, nil, func(t *testing.T, h testHome) {
		start := time.Now()
		_, err := listerOf(h).Unpack(context.Background(), owner, "deploys/site", bytes.NewReader(archive), files.UploadLimits{Bytes: 1 << 20, Entries: 10}, false)
		if !errors.Is(err, files.ErrTooLarge) {
			t.Fatalf("err = %v, want ErrTooLarge", err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("took %s", d)
		}
		assertNothingWritten(t, h, map[string][]byte{})
	})
}

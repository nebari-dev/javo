package files_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/files"
)

// homeFS is the fs every call about the home names.
const homeFS = "/home/user"

// secret is what the folder beside a disk home holds: nothing served from
// the home may ever carry it, and nothing written to the home may change it.
const secret = "secret outside the home"

// newDiskHome makes a home folder holding tree (files by path) and, beside
// it, a folder "outside" with secret.txt. It returns both paths.
func newDiskHome(t *testing.T, tree map[string][]byte) (home, outside string) {
	t.Helper()
	base := t.TempDir()
	home, outside = filepath.Join(base, "home-alice"), filepath.Join(base, "outside")
	must(t, os.Mkdir(home, 0o700))
	must(t, os.Mkdir(outside, 0o700))
	must(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte(secret), 0o600))
	for p, data := range tree {
		must(t, os.MkdirAll(filepath.Join(home, filepath.Dir(p)), 0o755))
		must(t, os.WriteFile(filepath.Join(home, p), data, 0o644))
	}
	return home, outside
}

// untouched fails unless outside holds its secret and nothing else.
func untouched(t *testing.T, outside string) {
	t.Helper()
	untouchedBut(t, outside)
}

// untouchedBut is untouched for a folder a test put more files in.
func untouchedBut(t *testing.T, outside string, more ...string) {
	t.Helper()
	entries, err := os.ReadDir(outside)
	must(t, err)
	got, err := os.ReadFile(filepath.Join(outside, "secret.txt"))
	if err != nil || string(got) != secret || len(entries) != 1+len(more) {
		t.Errorf("the folder outside the home changed: %d entries, secret.txt = %q, %v", len(entries), got, err)
	}
}

// readAll is what Open of remote serves, "" with the error when it does not.
func readAll(d files.Direct, home, remote string) (string, error) {
	rc, err := d.Open(context.Background(), home, remote)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	return string(b), err
}

func names(l files.Listing) []string {
	out := []string{}
	for _, e := range l.Entries {
		out = append(out, e.Name)
	}
	return out
}

// Every call refuses a symbolic link, the last name of the path or one on
// the way, whether it leads out of the home or stays inside it.
func TestDirectNeverFollowsASymlink(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		// target of the linked folder and of the linked file, as written
		// in the link; %s is the folder outside.
		dir, file string
	}{
		{"leading out, absolute", "%s", "%s/secret.txt"},
		{"leading out, relative", "../outside", "../outside/secret.txt"},
		{"staying inside", "notes", "notes/a.py"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, outside := newDiskHome(t, map[string][]byte{"notes/a.py": []byte("abc"), "notes/sub/b.txt": []byte("b")})
			target := func(s string) string {
				if strings.Contains(s, "%s") {
					return fmt.Sprintf(s, outside)
				}
				return s
			}
			must(t, os.Symlink(target(tc.dir), filepath.Join(home, "dirlink")))
			must(t, os.Symlink(target(tc.file), filepath.Join(home, "filelink")))
			before := snapshot(t, home)
			var d files.Direct

			l, err := d.List(ctx, home, homeFS, "")
			if err != nil || fmt.Sprint(names(l)) != "[notes]" {
				t.Errorf("List of the home = %v, %v; want only notes", names(l), err)
			}
			for _, link := range []string{"dirlink", "filelink"} {
				if e, err := d.Stat(ctx, home, homeFS, link); err != nil || e.IsDir || e.Name != link {
					t.Errorf("Stat(%s) = %+v, %v; want the link itself, not a folder", link, e, err)
				}
				if _, err := d.FindFile(ctx, home, "", link); !errors.Is(err, files.ErrNotFound) {
					t.Errorf("FindFile(%s) err = %v, want ErrNotFound", link, err)
				}
				if got, err := readAll(d, home, link); !errors.Is(err, files.ErrNotFound) {
					t.Errorf("Open(%s) = %q, %v; want ErrNotFound", link, got, err)
				}
				if err := d.Upload(ctx, home, "", link, []byte("written")); err == nil {
					t.Errorf("Upload over %s wrote", link)
				}
				if err := d.Purge(ctx, home, link); err == nil {
					t.Errorf("Purge(%s) deleted through a link", link)
				}
				if err := d.MoveDir(ctx, home, link, "moved"); err == nil {
					t.Errorf("MoveDir(%s) moved a link", link)
				}
			}
			// A link in the middle of a path.
			if _, err := d.List(ctx, home, homeFS, "dirlink"); !errors.Is(err, files.ErrNotFound) {
				t.Errorf("List(dirlink) err = %v, want ErrNotFound", err)
			}
			for _, through := range []string{"dirlink/secret.txt", "dirlink/a.py", "dirlink/sub/b.txt"} {
				if got, err := readAll(d, home, through); !errors.Is(err, files.ErrNotFound) {
					t.Errorf("Open(%s) = %q, %v; want ErrNotFound", through, got, err)
				}
				if _, err := d.Stat(ctx, home, homeFS, through); !errors.Is(err, files.ErrNotFound) {
					t.Errorf("Stat(%s) err = %v, want ErrNotFound", through, err)
				}
			}
			if _, err := d.FindFile(ctx, home, "dirlink", "a.py"); !errors.Is(err, files.ErrNotFound) {
				t.Errorf("FindFile in dirlink err = %v, want ErrNotFound", err)
			}
			if err := d.UploadStream(ctx, home, "dirlink/new", "x.txt", strings.NewReader("written")); err == nil {
				t.Error("UploadStream wrote through dirlink")
			}
			if err := d.MoveDir(ctx, home, "dirlink/sub", "dirlink/moved"); err == nil {
				t.Error("MoveDir moved a folder through dirlink")
			}
			if err := d.Purge(ctx, home, "dirlink/sub"); err == nil {
				t.Error("Purge deleted through dirlink")
			}
			untouched(t, outside)
			if after := snapshot(t, home); after != before {
				t.Errorf("the home changed:\n%s\nwas:\n%s", after, before)
			}
		})
	}
}

// snapshot describes every entry under dir: its kind, and a file's content
// or a link's target.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			to, _ := os.Readlink(p)
			fmt.Fprintf(&b, "%s -> %s\n", rel, to)
		case fi.Mode().IsRegular():
			data, _ := os.ReadFile(p)
			fmt.Fprintf(&b, "%s = %q\n", rel, data)
		default:
			fmt.Fprintf(&b, "%s (%s)\n", rel, fi.Mode().Type())
		}
		return nil
	})
	must(t, err)
	return b.String()
}

// A path cannot climb out of the home, and an absolute one is read as
// relative to the home.
func TestDirectRefusesPathsThatLeaveTheHome(t *testing.T) {
	ctx := context.Background()
	home, outside := newDiskHome(t, map[string][]byte{"notes/a.py": []byte("abc")})
	var d files.Direct
	for _, p := range []string{"../outside/secret.txt", "notes/../../outside/secret.txt", "..", "../../../../../../etc/hosts"} {
		if got, err := readAll(d, home, p); !errors.Is(err, files.ErrOutsideHome) {
			t.Errorf("Open(%q) = %q, %v; want ErrOutsideHome", p, got, err)
		}
		if _, err := d.Stat(ctx, home, homeFS, p); !errors.Is(err, files.ErrOutsideHome) {
			t.Errorf("Stat(%q) err = %v, want ErrOutsideHome", p, err)
		}
		if _, err := d.List(ctx, home, homeFS, p); !errors.Is(err, files.ErrOutsideHome) {
			t.Errorf("List(%q) err = %v, want ErrOutsideHome", p, err)
		}
		if err := d.UploadStream(ctx, home, p, "x.txt", strings.NewReader("written")); !errors.Is(err, files.ErrOutsideHome) {
			t.Errorf("UploadStream into %q err = %v, want ErrOutsideHome", p, err)
		}
		if err := d.Purge(ctx, home, p); !errors.Is(err, files.ErrOutsideHome) {
			t.Errorf("Purge(%q) err = %v, want ErrOutsideHome", p, err)
		}
		if err := d.MoveDir(ctx, home, "notes", p); !errors.Is(err, files.ErrOutsideHome) {
			t.Errorf("MoveDir to %q err = %v, want ErrOutsideHome", p, err)
		}
	}
	// A name that climbs is no better than a folder that does.
	if err := d.Upload(ctx, home, "notes", "../../outside/x.txt", []byte("written")); !errors.Is(err, files.ErrOutsideHome) {
		t.Errorf("Upload of a climbing name err = %v, want ErrOutsideHome", err)
	}
	if got, err := readAll(d, home, filepath.Join(outside, "secret.txt")); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("Open of an absolute path = %q, %v; want ErrNotFound (read inside the home)", got, err)
	}
	if got, err := readAll(d, home, "/notes/a.py"); err != nil || got != "abc" {
		t.Errorf("Open(/notes/a.py) = %q, %v", got, err)
	}
	untouched(t, outside)
}

// The home folder itself must be a real folder: a link in its place is
// not served, even though it would be the home the row names.
func TestDirectRefusesAHomeThatIsASymlink(t *testing.T) {
	ctx := context.Background()
	home, outside := newDiskHome(t, nil)
	link := filepath.Join(filepath.Dir(home), "home-bob")
	must(t, os.Symlink(outside, link))
	var d files.Direct
	if l, err := d.List(ctx, link, homeFS, ""); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("List = %v, %v; want ErrNotFound", names(l), err)
	}
	if got, err := readAll(d, link, "secret.txt"); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("Open = %q, %v; want ErrNotFound", got, err)
	}
	if err := d.Upload(ctx, link, "", "x.txt", []byte("written")); err == nil {
		t.Error("Upload wrote through a home that is a link")
	}
	if _, err := d.List(ctx, "home-alice", homeFS, ""); err == nil {
		t.Error("a home that is not an absolute path was listed")
	}
	untouched(t, outside)
}

// swapping is a Remote that changes the home right before the call that
// uses a path a Lister has just checked: the swap a program in the home
// could make between the check and the use.
type swapping struct {
	files.Direct
	swap func()
}

func (s swapping) Open(ctx context.Context, addr, remote string) (io.ReadCloser, error) {
	s.swap()
	return s.Direct.Open(ctx, addr, remote)
}

func (s swapping) Upload(ctx context.Context, addr, dir, name string, data []byte) error {
	s.swap()
	return s.Direct.Upload(ctx, addr, dir, name, data)
}

func (s swapping) UploadStream(ctx context.Context, addr, dir, name string, r io.Reader) error {
	s.swap()
	return s.Direct.UploadStream(ctx, addr, dir, name, r)
}

func (s swapping) List(ctx context.Context, addr, fs, remote string) (files.Listing, error) {
	s.swap()
	return s.Direct.List(ctx, addr, fs, remote)
}

func (s swapping) MoveDir(ctx context.Context, addr, src, dst string) error {
	s.swap()
	return s.Direct.MoveDir(ctx, addr, src, dst)
}

// replaceWithLink puts a link to target where path is.
func replaceWithLink(t *testing.T, path, target string) {
	t.Helper()
	must(t, os.RemoveAll(path))
	must(t, os.Symlink(target, path))
}

// The Lister checks a path and then uses it. A link put there in between
// is not followed: with no container around the gateway, it would reach
// the person's other files.
func TestALinkSwappedInAfterTheCheckIsNotFollowed(t *testing.T) {
	ctx := context.Background()
	tree := map[string][]byte{"notes/a.py": []byte("abc")}
	for _, tc := range []struct {
		name string
		// what is replaced by a link (relative to the home), and whether the
		// link leads to a folder or to a file.
		at     string
		folder bool
	}{
		{"the file", "notes/a.py", false},
		{"its folder", "notes", true},
	} {
		for _, where := range []string{"outside", "inside"} {
			t.Run(tc.name+" for a link leading "+where, func(t *testing.T) {
				setup := func() (*files.Lister, string, string) {
					home, outside := newDiskHome(t, tree)
					for _, p := range []string{"other/a.py", "notes/b.py"} {
						must(t, os.MkdirAll(filepath.Join(home, filepath.Dir(p)), 0o755))
						must(t, os.WriteFile(filepath.Join(home, p), []byte("other file"), 0o644))
					}
					// A link that stays inside is relative: os.Root itself
					// follows that kind.
					target := map[string]string{
						"outside folder": outside, "outside file": filepath.Join(outside, "secret.txt"),
						"inside folder": "other", "inside file": "b.py",
					}[where+map[bool]string{true: " folder", false: " file"}[tc.folder]]
					r := swapping{swap: func() { replaceWithLink(t, filepath.Join(home, tc.at), target) }}
					return files.NewLister(stubEnsurer{addr: home}, r), home, outside
				}
				intact := func(home, outside string) {
					t.Helper()
					untouched(t, outside)
					for _, p := range []string{"other/a.py", "notes/b.py"} {
						if got, err := os.ReadFile(filepath.Join(home, p)); err == nil && string(got) != "other file" {
							t.Errorf("%s, where the link leads, is now %q", p, got)
						}
					}
				}

				l, home, outside := setup()
				rc, _, err := l.Open(ctx, owner, "notes/a.py")
				if err == nil {
					got, _ := io.ReadAll(rc)
					rc.Close()
					t.Errorf("Open served %q through the link", got)
				}
				intact(home, outside)

				l, home, outside = setup()
				version := "3-" + fmt.Sprint(modTime(t, filepath.Join(home, "notes/a.py")).UnixNano())
				if _, err := l.Save(ctx, owner, "notes/a.py", []byte("written"), version); err == nil {
					t.Error("Save wrote through the link")
				}
				intact(home, outside)

				l, home, outside = setup()
				if _, err := l.Save(ctx, owner, "notes/new.py", []byte("written"), ""); err == nil && tc.folder {
					t.Error("Save made a file through the link")
				}
				intact(home, outside)

				l, home, outside = setup()
				if got, err := l.List(ctx, owner, "notes"); err == nil && tc.folder {
					t.Errorf("List listed %v through the link", names(got))
				}

				l, home, outside = setup()
				archive := tgz(t, file("app.py", "written"))
				if _, err := unpack(l, "notes/a.py/site", archive, 1<<20, false); err == nil {
					t.Error("Unpack wrote under a file or through the link")
				}
				if _, err := unpack(l, "notes/site", archive, 1<<20, false); err == nil && tc.folder {
					t.Error("Unpack wrote through the link")
				}
				intact(home, outside)
			})
		}
	}
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Lstat(path)
	must(t, err)
	return fi.ModTime()
}

// A program in the home swaps a file, or its folder, for a link and back as
// fast as it can while the file is read and written. No read ever serves
// what the link leads to and no write ever reaches it. A link that leaves
// the folder it is in (absolute, or with "..") the system refuses, since
// each folder is an os.Root of its own; one that stays in its folder
// os.Root would follow, and the look before every open refuses.
func TestDirectUnderALinkBeingSwappedInAndOut(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		// swap puts a link where the file or its folder is, and back.
		swap func(home, outside string)
	}{
		{"the file, for a link leading outside", func(home, outside string) {
			flipFile(filepath.Join(home, "notes/a.py"), filepath.Join(outside, "secret.txt"))
		}},
		{"the file, for a link staying inside", func(home, _ string) {
			flipFile(filepath.Join(home, "notes/a.py"), "b.py")
		}},
		{"the file, for a pipe no program reads or writes", func(home, _ string) {
			path := filepath.Join(home, "notes/a.py")
			_ = syscall.Mkfifo(path+".spare", 0o644)
			_ = os.Rename(path+".spare", path)
			_ = os.WriteFile(path+".spare", []byte("abc"), 0o644)
			_ = os.Rename(path+".spare", path)
		}},
		{"its folder, for a link leading outside", func(home, outside string) {
			flipFolder(filepath.Join(home, "notes"), outside)
		}},
		{"its folder, for a link staying inside", func(home, _ string) {
			flipFolder(filepath.Join(home, "notes"), "other")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, outside := newDiskHome(t, map[string][]byte{"notes/a.py": []byte("abc"), "notes/b.py": []byte(secret), "other/a.py": []byte(secret)})
			must(t, os.WriteFile(filepath.Join(outside, "a.py"), []byte(secret), 0o600))
			var stop atomic.Bool
			done := make(chan struct{})
			go func() {
				defer close(done)
				for !stop.Load() {
					tc.swap(home, outside)
				}
			}()
			var d files.Direct
			served, written := 0, 0
			for deadline := time.Now().Add(300 * time.Millisecond); time.Now().Before(deadline); {
				if got, err := readAll(d, home, "notes/a.py"); err == nil {
					served++
					if got != "abc" {
						t.Fatalf("Open served %q through the link", got)
					}
				}
				if err := d.Upload(ctx, home, "notes", "a.py", []byte("abc")); err == nil {
					written++
				}
			}
			stop.Store(true)
			<-done
			// Where a link led, nothing was written. (A folder swapped out
			// may have been made anew by a write, so its old files are
			// looked for under both names.)
			for _, p := range []string{"outside/secret.txt", "outside/a.py", "home-alice/other/a.py", "home-alice/notes/b.py", "home-alice/notes.real/b.py"} {
				got, err := os.ReadFile(filepath.Join(filepath.Dir(home), p))
				if err == nil && string(got) != secret || err != nil && !os.IsNotExist(err) {
					t.Errorf("%s was written through the link: %q, %v", p, got, err)
				}
			}
			untouchedBut(t, outside, "a.py")
			if served == 0 || written == 0 {
				t.Errorf("%d reads and %d writes got through; the test proves nothing unless some do", served, written)
			}
		})
	}
}

// flipFile replaces the file at path by a link to target, then by a file
// again. Each rename is atomic, so the name is always there.
func flipFile(path, target string) {
	spare := path + ".spare"
	_ = os.Symlink(target, spare)
	_ = os.Rename(spare, path)
	_ = os.WriteFile(spare, []byte("abc"), 0o644)
	_ = os.Rename(spare, path)
}

// flipFolder replaces the folder at path by a link to target, then puts
// the folder back.
func flipFolder(path, target string) {
	_ = os.Rename(path, path+".real")
	// A write that found no folder between two steps made one: it goes.
	_ = os.RemoveAll(path)
	_ = os.Symlink(target, path)
	_ = os.Remove(path)
	_ = os.Rename(path+".real", path)
}

// A pipe in the home is not listed, not opened and not written to, and no
// call waits on it for a program at its other end.
func TestDirectLeavesAPipeAlone(t *testing.T) {
	ctx := context.Background()
	home, _ := newDiskHome(t, map[string][]byte{"notes/a.py": []byte("abc")})
	must(t, syscall.Mkfifo(filepath.Join(home, "notes/pipe"), 0o644))
	var d files.Direct
	done := make(chan struct{})
	go func() {
		defer close(done)
		if l, err := d.List(ctx, home, homeFS, "notes"); err != nil || fmt.Sprint(names(l)) != "[a.py]" {
			t.Errorf("List = %v, %v; want only a.py", names(l), err)
		}
		if e, err := d.Stat(ctx, home, homeFS, "notes/pipe"); err != nil || e.IsDir {
			t.Errorf("Stat = %+v, %v; want an entry that is not a folder", e, err)
		}
		if _, err := d.FindFile(ctx, home, "notes", "pipe"); !errors.Is(err, files.ErrNotFound) {
			t.Errorf("FindFile err = %v, want ErrNotFound", err)
		}
		if got, err := readAll(d, home, "notes/pipe"); !errors.Is(err, files.ErrNotFound) {
			t.Errorf("Open = %q, %v; want ErrNotFound", got, err)
		}
		if err := d.Upload(ctx, home, "notes", "pipe", []byte("written")); err == nil {
			t.Error("Upload wrote to the pipe")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a call is waiting on the pipe")
	}
}

// A path far longer than the system allows in one name is still walked,
// one folder at a time.
func TestDirectReachesAVeryDeepPath(t *testing.T) {
	ctx := context.Background()
	home, _ := newDiskHome(t, nil)
	deep := strings.TrimSuffix(strings.Repeat("a-folder-with-a-name/", 250), "/")
	var d files.Direct
	if err := d.UploadStream(ctx, home, deep, "x.txt", strings.NewReader("deep")); err != nil {
		t.Fatal(err)
	}
	if got, err := readAll(d, home, deep+"/x.txt"); err != nil || got != "deep" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if e, err := d.FindFile(ctx, home, deep, "x.txt"); err != nil || e.Size != 4 {
		t.Fatalf("FindFile = %+v, %v", e, err)
	}
	if err := d.Purge(ctx, home, "a-folder-with-a-name"); err != nil {
		t.Fatal(err)
	}
	if l, err := d.List(ctx, home, homeFS, ""); err != nil || len(l.Entries) != 0 {
		t.Fatalf("after Purge: %v, %v", names(l), err)
	}
	// The Lister itself takes no path deeper than files.MaxPathDepth.
	if _, err := files.CleanPath(deep); !errors.Is(err, files.ErrPathTooDeep) {
		t.Fatalf("CleanPath err = %v, want ErrPathTooDeep", err)
	}
}

func TestDirectListsFilesAndFoldersUpToTheCap(t *testing.T) {
	ctx := context.Background()
	home, _ := newDiskHome(t, map[string][]byte{"b.txt": []byte("12345"), "a/x": nil})
	var d files.Direct
	l, err := d.List(ctx, home, homeFS, "")
	if err != nil || l.Truncated || fmt.Sprint(names(l)) != "[a b.txt]" {
		t.Fatalf("List = %+v, %v", l, err)
	}
	if a, b := l.Entries[0], l.Entries[1]; !a.IsDir || b.IsDir || b.Size != 5 || !b.ModTime.Equal(modTime(t, filepath.Join(home, "b.txt"))) {
		t.Errorf("entries = %+v", l.Entries)
	}
	if l, err := d.List(ctx, home, homeFS, "a/x"); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("List of a file = %v, %v; want ErrNotFound", names(l), err)
	}
	if _, err := d.List(ctx, home, homeFS, "nope"); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("List of a missing folder err = %v, want ErrNotFound", err)
	}
	if l, err := d.List(ctx, home, homeFS, "a"); err != nil || l.Entries == nil {
		t.Errorf("List(a) = %+v, %v", l, err)
	}

	must(t, os.Mkdir(filepath.Join(home, "big"), 0o755))
	for i := range files.MaxEntries {
		must(t, os.WriteFile(filepath.Join(home, "big", fmt.Sprintf("f%05d", i)), nil, 0o644))
	}
	// Links do not count towards the cap.
	must(t, os.Symlink("f00000", filepath.Join(home, "big", "link")))
	if l, err := d.List(ctx, home, homeFS, "big"); err != nil || l.Truncated || len(l.Entries) != files.MaxEntries {
		t.Fatalf("at the cap: %d entries, truncated %v, %v", len(l.Entries), l.Truncated, err)
	}
	must(t, os.WriteFile(filepath.Join(home, "big", "one-more"), nil, 0o644))
	if l, err := d.List(ctx, home, homeFS, "big"); err != nil || !l.Truncated || len(l.Entries) != files.MaxEntries {
		t.Fatalf("over the cap: %d entries, truncated %v, %v", len(l.Entries), l.Truncated, err)
	}
	// A name past the cut is still found.
	if _, err := d.FindFile(ctx, home, "big", "one-more"); err != nil {
		t.Errorf("FindFile past the cap: %v", err)
	}
}

// Upload writes over a file in place (its mode stays) and makes the
// folders a new file needs.
func TestDirectUploadWritesInPlaceAndMakesFolders(t *testing.T) {
	ctx := context.Background()
	home, _ := newDiskHome(t, map[string][]byte{"notes/a.py": []byte("a long first version")})
	must(t, os.Chmod(filepath.Join(home, "notes/a.py"), 0o600))
	var d files.Direct
	must(t, d.Upload(ctx, home, "notes", "a.py", []byte("v2")))
	fi, err := os.Stat(filepath.Join(home, "notes/a.py"))
	if got, _ := readAll(d, home, "notes/a.py"); err != nil || got != "v2" || fi.Mode().Perm() != 0o600 {
		t.Errorf("a.py = %q, mode %v, %v", got, fi.Mode(), err)
	}
	must(t, d.UploadStream(ctx, home, "x/y/z", "n.txt", strings.NewReader("new")))
	if got, err := readAll(d, home, "x/y/z/n.txt"); err != nil || got != "new" {
		t.Errorf("n.txt = %q, %v", got, err)
	}
	if err := d.Upload(ctx, home, "notes/a.py", "under-a-file.txt", nil); err == nil {
		t.Error("Upload made a file under a file")
	}
	if err := d.Upload(ctx, home, "", "notes", nil); err == nil {
		t.Error("Upload wrote over a folder")
	}
	// What the reader fails with is the upload's error.
	broken := io.MultiReader(strings.NewReader("half"), &failingReader{err: files.ErrTooLarge})
	if err := d.UploadStream(ctx, home, "", "half.bin", broken); !errors.Is(err, files.ErrTooLarge) {
		t.Errorf("err = %v, want the reader's", err)
	}
}

func TestDirectMoveDirNeedsARealFolderAndAFreeName(t *testing.T) {
	ctx := context.Background()
	home, _ := newDiskHome(t, map[string][]byte{"deploys/.site.upload-1/app.py": []byte("v2"), "deploys/site/app.py": []byte("v1"), "deploys/file": nil})
	must(t, os.Mkdir(filepath.Join(home, "deploys/empty"), 0o755))
	var d files.Direct
	for _, dst := range []string{"deploys/site", "deploys/file", "deploys/empty"} {
		if err := d.MoveDir(ctx, home, "deploys/.site.upload-1", dst); !errors.Is(err, files.ErrExists) {
			t.Errorf("MoveDir onto %s err = %v, want ErrExists", dst, err)
		}
	}
	if err := d.MoveDir(ctx, home, "deploys/nope", "deploys/new"); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("MoveDir of a missing folder err = %v, want ErrNotFound", err)
	}
	if err := d.MoveDir(ctx, home, "deploys/file", "deploys/new"); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("MoveDir of a file err = %v, want ErrNotFound", err)
	}
	if err := d.MoveDir(ctx, home, "deploys/site", "elsewhere/site"); err == nil {
		t.Error("MoveDir moved a folder to another folder")
	}
	must(t, d.MoveDir(ctx, home, "deploys/.site.upload-1", "deploys/new"))
	if got, err := readAll(d, home, "deploys/new/app.py"); err != nil || got != "v2" {
		t.Errorf("moved app.py = %q, %v", got, err)
	}
	if _, err := d.Stat(ctx, home, homeFS, "deploys/.site.upload-1"); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("the source is still there: %v", err)
	}
}

// Purge deletes a folder and all in it; a link inside is deleted, not what
// it leads to.
func TestDirectPurgeDeletesAFolderButNothingALinkLeadsTo(t *testing.T) {
	ctx := context.Background()
	home, outside := newDiskHome(t, map[string][]byte{"old/a/b.txt": []byte("b"), "keep.txt": []byte("keep")})
	must(t, os.Symlink(outside, filepath.Join(home, "old/a/out")))
	must(t, os.Symlink(filepath.Join(home, "keep.txt"), filepath.Join(home, "old/keep")))
	var d files.Direct
	must(t, d.Purge(ctx, home, "old"))
	if got := snapshot(t, home); got != ". (d---------)\nkeep.txt = \"keep\"\n" {
		t.Errorf("home after Purge:\n%s", got)
	}
	untouched(t, outside)
	if err := d.Purge(ctx, home, "old"); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("Purge of a missing folder err = %v, want ErrNotFound", err)
	}
	if err := d.Purge(ctx, home, "keep.txt"); err == nil {
		t.Error("Purge deleted a file")
	}
	if err := d.Purge(ctx, home, ""); err == nil {
		t.Error("Purge deleted the home itself")
	}
}

// About reports the volume the home is on, as the system does.
func TestDirectAboutReportsTheHomesVolume(t *testing.T) {
	home, _ := newDiskHome(t, nil)
	u, err := files.Direct{}.About(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	var st syscall.Statfs_t
	must(t, syscall.Statfs(home, &st))
	if want := int64(st.Blocks) * int64(st.Bsize); u.Total != want || u.Used <= 0 || u.Used > u.Total {
		t.Errorf("About = %+v, want a total of %d and a part of it used", u, want)
	}
}

// There are no group folders on a host: only the home is served.
func TestDirectServesOnlyTheHome(t *testing.T) {
	home, _ := newDiskHome(t, map[string][]byte{"a.txt": nil})
	var d files.Direct
	if _, err := d.List(context.Background(), home, "/shared/ml", ""); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("List of a group folder err = %v, want ErrNotFound", err)
	}
	if _, err := d.Stat(context.Background(), home, "/shared/ml", "a.txt"); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("Stat in a group folder err = %v, want ErrNotFound", err)
	}
}

// The Lister's own caps hold over a home on disk: a file over the cap is
// not opened, one at the cap is.
func TestListerOverDirectRefusesFilesOverTheCap(t *testing.T) {
	home, _ := newDiskHome(t, map[string][]byte{"big.bin": nil, "edge.bin": nil})
	must(t, os.Truncate(filepath.Join(home, "big.bin"), files.MaxOpenBytes+1))
	must(t, os.Truncate(filepath.Join(home, "edge.bin"), files.MaxOpenBytes))
	l := files.NewLister(stubEnsurer{addr: home}, files.Direct{})
	if _, _, err := l.Open(context.Background(), owner, "big.bin"); !errors.Is(err, files.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	rc, info, err := l.Open(context.Background(), owner, "edge.bin")
	if err != nil || info.Size != files.MaxOpenBytes {
		t.Fatalf("file exactly at the cap: %+v, %v", info, err)
	}
	rc.Close()
}

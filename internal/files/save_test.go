package files_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/storage/ids"
)

// Upload sends the bytes as one multipart file to operations/uploadfile,
// which writes it into the folder remote names, under the part's file name.
func TestRcloneUploadWritesOneFileIntoAFolderOfTheHome(t *testing.T) {
	var fs, remote, name, body, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fs, remote = r.URL.Query().Get("fs"), r.URL.Query().Get("remote")
		f, h, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(f)
		name, body = h.Filename, string(b)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	err := files.Rclone{HTTP: srv.Client()}.Upload(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "notes/a b", "x.py", []byte("print(1)\n"))
	if err != nil {
		t.Fatal(err)
	}
	if path != "/operations/uploadfile" || fs != "/home/user" || remote != "notes/a b" || name != "x.py" || body != "print(1)\n" {
		t.Fatalf("got %s fs=%q remote=%q name=%q body=%q", path, fs, remote, name, body)
	}
}

func TestRcloneUploadFailureIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"permission denied"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	err := files.Rclone{HTTP: srv.Client()}.Upload(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "", "x.py", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v", err)
	}
}

// uploadedAt is the modification time stubRemote gives a file it writes.
var uploadedAt = time.Date(2026, 11, 3, 12, 0, 0, 7, time.UTC)

// Upload writes into the tree: the file gets the data's size and uploadedAt.
func (s stubRemote) Upload(_ context.Context, _, dir, name string, data []byte) error {
	e := files.Entry{Name: name, Size: int64(len(data)), ModTime: uploadedAt}
	if i := slices.IndexFunc(s[dir], func(e files.Entry) bool { return e.Name == name }); i >= 0 {
		s[dir][i] = e
	} else {
		s[dir] = append(s[dir], e)
	}
	return nil
}

func (s stubRemote) UploadStream(ctx context.Context, addr, dir, name string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return s.Upload(ctx, addr, dir, name, data)
}

// The tree holds no folder moves; memRemote (unpack_test.go) does.
func (stubRemote) MoveDir(context.Context, string, string, string) error {
	return errors.New("stubRemote: no MoveDir")
}

func (stubRemote) Purge(context.Context, string, string) error {
	return errors.New("stubRemote: no Purge")
}

var mod = time.Date(2026, 10, 30, 19, 22, 27, 123, time.UTC)

// editTree is a home with notes/a.py (a real file), a symlink "link.py" in
// notes (rclone lists it nowhere, stat reports it as a non-folder) and a
// symlinked folder "out" (stat reports a non-folder).
func editTree() stubRemote {
	return stubRemote{
		"":          {{Name: "notes", IsDir: true}, {Name: "out", Size: 4, ModTime: mod}},
		"notes":     {{Name: "a.py", Size: 3, ModTime: mod}, {Name: "sub", IsDir: true}},
		"notes/sub": {},
	}
}

func versionOf(t *testing.T, l *files.Lister, rel string) string {
	t.Helper()
	rc, info, err := l.Open(context.Background(), owner, rel)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	return info.Version()
}

func TestSaveWritesTheFileWhenItIsTheVersionTheEditorRead(t *testing.T) {
	tree := editTree()
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, tree)
	v := versionOf(t, l, "notes/a.py")
	info, err := l.Save(context.Background(), owner, "notes/a.py", []byte("print(2)\n"), v)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 9 || !info.ModTime.Equal(uploadedAt) || info.Version() == v || info.Version() != versionOf(t, l, "notes/a.py") {
		t.Fatalf("info = %+v (version %q, was %q)", info, info.Version(), v)
	}
}

func TestSaveRefusesAFileThatChangedSinceItWasRead(t *testing.T) {
	tree := editTree()
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, tree)
	v := versionOf(t, l, "notes/a.py")
	if _, err := l.Save(context.Background(), owner, "notes/a.py", []byte("first\n"), v); err != nil {
		t.Fatal(err)
	}
	_, err := l.Save(context.Background(), owner, "notes/a.py", []byte("second\n"), v)
	if !errors.Is(err, files.ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	if tree["notes"][0].Size != int64(len("first\n")) {
		t.Fatalf("the second save was written: %+v", tree["notes"][0])
	}
}

// linkRemote adds symlinks to a tree as rclone sees them: no listing shows
// them, stat reports them as non-folders.
type linkRemote struct {
	stubRemote
	links []string
}

func (r linkRemote) Stat(ctx context.Context, addr, fs, remote string) (files.Entry, error) {
	if slices.Contains(r.links, remote) {
		return files.Entry{Name: path.Base(remote), Size: 12}, nil
	}
	return r.stubRemote.Stat(ctx, addr, fs, remote)
}

// The home holds notes/link.py, a symlink to a file, and out, a symlink to
// a folder: a save through either would write outside the home.
func TestSaveNeverWritesThroughASymlinkOrIntoAFolder(t *testing.T) {
	tree := editTree()
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, linkRemote{tree, []string{"notes/link.py", "out"}})
	for _, tc := range []struct{ rel, version string }{
		{"notes/link.py", "12-0"},
		{"notes/link.py", ""},
		{"out/x.py", ""},
		{"out/x.py", "3-0"},
		{"notes/sub", ""},
		{"notes/sub", "0-0"},
		{"notes/nope/x.py", ""},
		{"", ""},
	} {
		if _, err := l.Save(context.Background(), owner, tc.rel, []byte("x"), tc.version); err == nil {
			t.Errorf("Save(%q, %q) wrote", tc.rel, tc.version)
		}
	}
	if !reflect.DeepEqual(tree, editTree()) {
		t.Fatalf("tree changed: %v", tree)
	}
}

func TestSaveCreatesANewFileOnlyWhereNothingIs(t *testing.T) {
	tree := editTree()
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, linkRemote{tree, []string{"notes/link.py"}})
	info, err := l.Save(context.Background(), owner, "notes/sub/new.md", nil, "")
	if err != nil || info.Name != "new.md" || info.Size != 0 {
		t.Fatalf("create = %+v, %v", info, err)
	}
	for _, rel := range []string{"notes/sub/new.md", "notes/a.py", "notes/link.py", "notes/sub"} {
		if _, err := l.Save(context.Background(), owner, rel, []byte("x"), ""); !errors.Is(err, files.ErrExists) {
			t.Errorf("create over %q: err = %v, want ErrExists", rel, err)
		}
	}
}

func TestSaveRefusesWhatIsNotTextOrTooLong(t *testing.T) {
	tree := editTree()
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, tree)
	v := versionOf(t, l, "notes/a.py")
	for name, data := range map[string][]byte{
		"NUL byte":      []byte("a\x00b"),
		"invalid UTF-8": {0xff, 0xfe, 'a'},
	} {
		if _, err := l.Save(context.Background(), owner, "notes/a.py", data, v); !errors.Is(err, files.ErrNotText) {
			t.Errorf("%s: err = %v, want ErrNotText", name, err)
		}
	}
	if _, err := l.Save(context.Background(), owner, "notes/a.py", bytes.Repeat([]byte("a"), files.MaxEditBytes+1), v); !errors.Is(err, files.ErrTooLarge) {
		t.Errorf("too long: err = %v, want ErrTooLarge", err)
	}
	if !reflect.DeepEqual(tree, editTree()) {
		t.Fatalf("tree changed: %v", tree)
	}
	if _, err := l.Save(context.Background(), owner, "notes/a.py", []byte("héllo ✓\n"), v); err != nil {
		t.Fatalf("UTF-8 text: %v", err)
	}
}

// gatedRemote holds every upload until release is closed.
type gatedRemote struct {
	stubRemote
	entered chan struct{}
	release chan struct{}
}

func (g gatedRemote) Upload(ctx context.Context, addr, dir, name string, data []byte) error {
	g.entered <- struct{}{}
	<-g.release
	return g.stubRemote.Upload(ctx, addr, dir, name, data)
}

// Two saves of the same version at once (two tabs): one is written, the
// other is told the file changed, never both written.
func TestSaveOfTheSameVersionAtOnceWritesOnlyOne(t *testing.T) {
	g := gatedRemote{editTree(), make(chan struct{}, 2), make(chan struct{})}
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, g)
	v := versionOf(t, l, "notes/a.py")
	errs := make(chan error, 2)
	save := func(text string) {
		_, err := l.Save(context.Background(), owner, "notes/a.py", []byte(text), v)
		errs <- err
	}
	go save("first\n")
	<-g.entered
	go save("second\n")
	time.Sleep(50 * time.Millisecond) // the second save gets as far as it can
	close(g.release)
	var changed, ok int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, files.ErrChanged):
			changed++
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || changed != 1 {
		t.Fatalf("%d written, %d refused; want one each", ok, changed)
	}
}

// ownerUID is the uid the owner's apps run as, and so their file pod.
const ownerUID = 20001

type fixedUID int64

func (u fixedUID) UID(context.Context, string) (int64, int64, error) { return int64(u), 100, nil }

type noUID struct{}

func (noUID) UID(context.Context, string) (int64, int64, error) { return 0, 0, ids.ErrNoUID }

// A file pod made before saves existed mounts the home read-only and runs as
// the fixed uid; one made before the owner's uid changed runs as the old
// one. Either is replaced, so a save never meets a pod that cannot write.
func TestEnsureReplacesAPodThatCannotWriteAsTheOwner(t *testing.T) {
	readOnly := filePod(corev1.PodRunning, true, "10.0.0.5")
	readOnly.Spec.Containers[0].VolumeMounts[0].ReadOnly = true
	otherUID := filePod(corev1.PodRunning, true, "10.0.0.5")
	otherUID.Spec.SecurityContext.RunAsUser = ptr.To[int64](1000)
	for name, pod := range map[string]*corev1.Pod{"read-only home": readOnly, "other uid": otherUID} {
		t.Run(name, func(t *testing.T) {
			c := newClient(t, homeClaim(), pod)
			if _, err := newPods(t, c, &clock{time.Now()}).Ensure(context.Background(), owner); !errors.Is(err, files.ErrHomeStarting) {
				t.Fatalf("err = %v, want ErrHomeStarting", err)
			}
			if !gone(t, c, pod) {
				t.Fatal("the old pod was kept")
			}
		})
	}
}

// In allocated uid mode a person has no uid for a few seconds after their
// first sign-in; no pod is made until they do.
func TestEnsureWaitsForTheOwnersUID(t *testing.T) {
	c := newClient(t, homeClaim())
	p, err := files.NewPods(files.PodsConfig{Client: c, Namespace: ns, Image: image, UIDs: noUID{}, Seal: testKey, Storage: stubStorage{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Ensure(context.Background(), owner); !errors.Is(err, files.ErrHomeStarting) {
		t.Fatalf("err = %v, want ErrHomeStarting", err)
	}
	var pods corev1.PodList
	_ = c.List(context.Background(), &pods)
	if len(pods.Items) != 0 {
		t.Fatalf("made %d pods", len(pods.Items))
	}
}

// slowFor answers every call for one pod address only once release is
// closed (that person's file pod is slow, as a folder of millions of entries
// makes it); other addresses answer at once.
type slowFor struct {
	stubRemote
	addr    string
	entered chan struct{}
	release chan struct{}
}

func (s slowFor) wait(addr string) {
	if addr == s.addr {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		<-s.release
	}
}

func (s slowFor) Stat(ctx context.Context, addr, fs, remote string) (files.Entry, error) {
	s.wait(addr)
	return s.stubRemote.Stat(ctx, addr, fs, remote)
}

// podPerUser gives every user a file pod at an address named after them.
type podPerUser struct{ stubEnsurer }

func (podPerUser) Ensure(_ context.Context, user string) (string, error) { return user, nil }

// One person's slow save never holds up another person's save, whatever
// path either picks: before, both shared one of 64 locks chosen by a hash
// of user and path, so a path chosen to collide stalled the other save.
func TestSaveOfOnePersonNeverWaitsForAnothersSlowSave(t *testing.T) {
	s := slowFor{editTree(), "bob", make(chan struct{}, 1), make(chan struct{})}
	defer close(s.release)
	l := files.NewLister(podPerUser{}, s)
	victim := "notes/new.md"
	bucket := func(user, rel string) uint32 {
		h := fnv.New32a()
		_, _ = h.Write([]byte(user + "\x00" + rel))
		return h.Sum32() % 64
	}
	var mine string
	for i := 0; mine == ""; i++ {
		if p := fmt.Sprintf("x%d.txt", i); bucket("bob", p) == bucket("alice", victim) {
			mine = p
		}
	}
	go func() { _, _ = l.Save(context.Background(), "bob", mine, []byte("a"), "") }()
	<-s.entered
	done := make(chan error, 1)
	go func() {
		_, err := l.Save(context.Background(), "alice", victim, []byte("hi"), "")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("alice's save waited for bob's")
	}
}

// A save waiting behind another save of the same file gives up when its
// request ends, instead of holding the request open until the first is done.
func TestSaveWaitingForTheSameFileStopsWhenTheRequestEnds(t *testing.T) {
	g := gatedRemote{editTree(), make(chan struct{}, 2), make(chan struct{})}
	defer close(g.release)
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, g)
	go func() { _, _ = l.Save(context.Background(), owner, "notes/new.md", []byte("a"), "") }()
	<-g.entered
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := l.Save(ctx, owner, "notes/new.md", []byte("b"), "")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting save ignored its ended request")
	}
}

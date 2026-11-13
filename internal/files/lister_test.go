package files_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tink-crypto/tink-go/v2/tink"

	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/secrets"
)

// testKey seals the file pods' logins in these tests.
var testKey = func() tink.AEAD {
	k, err := secrets.NewKey()
	if err != nil {
		panic(err)
	}
	return k
}()

const (
	ns    = "javo-apps"
	owner = "alice-aaaaaaaa"
	image = "example.org/rclone@sha256:0000000000000000000000000000000000000000000000000000000000000000"
)

// --- rclone client ---

// MoveDir moves one folder of the home to another with sync/move, both
// named as local paths inside the file pod.
func TestRcloneMoveDirSendsFsPair(t *testing.T) {
	var path string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	err := files.Rclone{HTTP: srv.Client()}.MoveDir(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "deploys/.site.upload-1", "deploys/site")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/sync/move" || got["srcFs"] != "/home/user/deploys/.site.upload-1" || got["dstFs"] != "/home/user/deploys/site" ||
		got["deleteEmptySrcDirs"] != true {
		t.Fatalf("got %s %v", path, got)
	}
}

func TestRclonePurgeSendsRemote(t *testing.T) {
	var path string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	err := files.Rclone{HTTP: srv.Client()}.Purge(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "deploys/.site.old-1")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/operations/purge" || got["fs"] != "/home/user" || got["remote"] != "deploys/.site.old-1" {
		t.Fatalf("got %s %v", path, got)
	}
}

// slowBody is an endless body whose every read takes a while; reading says
// whether a read is under way.
type slowBody struct {
	reading atomic.Bool
	reads   atomic.Int32
}

func (b *slowBody) Read(p []byte) (int, error) {
	b.reading.Store(true)
	defer b.reading.Store(false)
	b.reads.Add(1)
	time.Sleep(20 * time.Millisecond)
	return copy(p, "data"), nil
}

// failAfter is a transport that reads n bytes of the request, then fails
// as a connection to rclone that breaks mid-upload would.
type failAfter int64

func (n failAfter) RoundTrip(r *http.Request) (*http.Response, error) {
	buf := make([]byte, 64<<10)
	for read := 0; int64(read) < int64(n); {
		m, err := r.Body.Read(buf)
		if err != nil {
			break
		}
		read += m
	}
	return nil, errors.New("connection reset by peer")
}

// When the upload to rclone fails part way, UploadStream returns only once
// it has stopped reading the body: the caller may read or close it next.
func TestRcloneUploadStreamStopsReadingBeforeItReturns(t *testing.T) {
	body := &slowBody{}
	err := files.Rclone{HTTP: &http.Client{Transport: failAfter(400)}}.UploadStream(context.Background(), "10.0.0.5:5572", "d", "x.bin", body)
	if err == nil {
		t.Fatal("no error from a failing rclone")
	}
	if body.reading.Load() {
		t.Fatal("still reading the body after returning")
	}
	n := body.reads.Load()
	time.Sleep(100 * time.Millisecond)
	if m := body.reads.Load(); m != n {
		t.Fatalf("read the body %d more times after returning", m-n)
	}
}

// UploadStream sends what it reads as one multipart file, however large,
// without the caller holding it all.
func TestRcloneUploadStreamSendsFile(t *testing.T) {
	data := make([]byte, 3<<20)
	for i := range data {
		data[i] = byte(i % 251)
	}
	var fs, remote, name, path string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fs, remote = r.URL.Query().Get("fs"), r.URL.Query().Get("remote")
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		p, err := mr.NextPart()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name = p.FileName()
		body, _ = io.ReadAll(p)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	err := files.Rclone{HTTP: srv.Client()}.UploadStream(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "deploys/site", "data.bin", strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if path != "/operations/uploadfile" || fs != "/home/user" || remote != "deploys/site" || name != "data.bin" || !slices.Equal(body, data) {
		t.Fatalf("got %s fs=%q remote=%q name=%q, %d bytes", path, fs, remote, name, len(body))
	}
}

func TestRcloneListParsesEntries(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/operations/list" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"list":[
			{"Path":"Data/b.csv","Name":"b.csv","Size":2,"MimeType":"text/csv","ModTime":"2026-10-30T19:22:27Z","IsDir":false},
			{"Path":"Data/sub","Name":"sub","Size":4096,"ModTime":"2026-10-29T08:00:00.5Z","IsDir":true}]}`))
	}))
	defer srv.Close()

	r := files.Rclone{HTTP: srv.Client()}
	got2, err := r.List(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "/home/user", "Data")
	if err != nil {
		t.Fatal(err)
	}
	if got2.Truncated {
		t.Error("small listing marked truncated")
	}
	entries := got2.Entries
	if got["fs"] != "/home/user" || got["remote"] != "Data" {
		t.Errorf("request body = %v", got)
	}
	want := []files.Entry{
		{Name: "b.csv", Size: 2, ModTime: time.Date(2026, 10, 30, 19, 22, 27, 0, time.UTC)},
		{Name: "sub", IsDir: true, Size: 4096, ModTime: time.Date(2026, 10, 29, 8, 0, 0, 5e8, time.UTC)},
	}
	if len(entries) != len(want) {
		t.Fatalf("entries = %+v", entries)
	}
	for i := range want {
		if entries[i].Name != want[i].Name || entries[i].IsDir != want[i].IsDir ||
			entries[i].Size != want[i].Size || !entries[i].ModTime.Equal(want[i].ModTime) {
			t.Errorf("entry %d = %+v, want %+v", i, entries[i], want[i])
		}
	}
}

func TestRcloneListMissingFolderIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"error in ListJSON: directory not found","path":"operations/list","status":404}`))
	}))
	defer srv.Close()
	_, err := files.Rclone{HTTP: srv.Client()}.List(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "/home/user", "nope")
	if !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRcloneListServerErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err := files.Rclone{HTTP: srv.Client()}.List(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "/home/user", "")
	if err == nil || errors.Is(err, files.ErrNotFound) {
		t.Fatalf("err = %v, want a non-NotFound error", err)
	}
}

// --- path cleaning ---

func TestCleanPath(t *testing.T) {
	ok := map[string]string{
		"":              "",
		"/":             "",
		".":             "",
		"Data":          "Data",
		"/Data/":        "Data",
		"Data//sub/./x": "Data/sub/x",
		"Data/sub/..":   "Data",
	}
	for in, want := range ok {
		got, err := files.CleanPath(in)
		if err != nil || got != want {
			t.Errorf("CleanPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"..", "../", "../etc", "/../etc", "Data/../../x"} {
		if _, err := files.CleanPath(in); !errors.Is(err, files.ErrOutsideHome) {
			t.Errorf("CleanPath(%q) err = %v, want ErrOutsideHome", in, err)
		}
	}
	deepest := strings.Repeat("d/", files.MaxPathDepth-1) + "f"
	if got, err := files.CleanPath(deepest); err != nil || got != deepest {
		t.Errorf("CleanPath of %d names = %q, %v", files.MaxPathDepth, got, err)
	}
	// Each folder on a path costs a stat call to the file pod, so a path of
	// thousands of folders would make every call slow.
	if _, err := files.CleanPath("d/" + deepest); !errors.Is(err, files.ErrPathTooDeep) {
		t.Errorf("CleanPath of %d names err = %v, want ErrPathTooDeep", files.MaxPathDepth+1, err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type fixedUID int64

// homelessStorage is a person without a home: Home records who asked and
// answers the new home as the store makes it, with no claim yet.
type homelessStorage struct{ asked string }

type noUID struct{}

// ownerUID is the uid the owner's apps run as, and so their file pod.
const ownerUID = 20001

type stubEnsurer struct {
	addr string
	err  error
}

// stubStorage answers every Input call with in. Its home is in.Home, or a
// prepared home on the owner's first claim when in has none.
type stubStorage struct{ in podstorage.Input }

func (s stubEnsurer) Ensure(context.Context, string) (string, error) { return s.addr, s.err }
func (s stubEnsurer) Release(context.Context, string) error          { return nil }
func (s stubEnsurer) Touch(string)                                   {}
func (s stubEnsurer) EnsureGroup(context.Context, string, string) (string, error) {
	return s.addr, s.err
}

func (s stubStorage) Input(context.Context, string, string) (podstorage.Input, error) {
	in := s.in
	h := s.home()
	in.Home = &h
	return in, nil
}

func (s stubStorage) Home(context.Context, string, string) (model.Home, error) { return s.home(), nil }

func (s stubStorage) home() model.Home {
	if s.in.Home != nil {
		return *s.in.Home
	}
	return model.Home{Spec: model.HomeSpec{Owner: owner}, Status: model.HomeStatus{ClaimName: naming.HomeClaimName(owner), Prepared: true}}
}

func (s *homelessStorage) Input(context.Context, string, string) (podstorage.Input, error) {
	return podstorage.Input{}, nil
}

func (s *homelessStorage) Home(_ context.Context, _, who string) (model.Home, error) {
	s.asked = who
	return model.Home{Spec: model.HomeSpec{Owner: who}}, nil
}

func (u fixedUID) UID(context.Context, string) (int64, int64, error) { return int64(u), 100, nil }

func (noUID) UID(context.Context, string) (int64, int64, error) { return 0, 0, ids.ErrNoUID }

var mod = time.Date(2026, 10, 30, 19, 22, 27, 123, time.UTC)

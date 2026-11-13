package bundle

import (
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// entry is one file of a test archive; a mode of zero is a plain file.
type entry struct {
	name, body string
	mode       fs.FileMode
}

func archiveOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h.SetMode(mode)
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func opened(t *testing.T, entries ...entry) *Bundle {
	t.Helper()
	b, err := open(archiveOf(t, entries...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestABinaryWithoutTheArchiveSaysHowToBuildOne(t *testing.T) {
	_, err := open(nil)
	if !errors.Is(err, ErrNone) || !strings.Contains(err.Error(), "make local") {
		t.Fatalf("open(nil) = %v, want ErrNone naming make local", err)
	}
}

// net/http serves a file only when it can seek in it, which a file of a
// zip archive cannot: the UI's files can, also for a range of one.
func TestTheUIIsServedFromTheArchive(t *testing.T) {
	b := opened(t,
		entry{name: "ui/index.html", body: "<html>javo</html>"},
		entry{name: "ui/assets/app.js", body: "console.log(1)"},
		entry{name: "templates/a.yaml", body: "x"},
	)
	srv := httptest.NewServer(http.FileServerFS(b.UI()))
	defer srv.Close()
	for path, want := range map[string]string{"/": "<html>javo</html>", "/assets/app.js": "console.log(1)"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		var body bytes.Buffer
		_, _ = body.ReadFrom(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || body.String() != want {
			t.Errorf("GET %s = %d %q, want %q", path, resp.StatusCode, body.String(), want)
		}
		// A new binary's page must not be taken for the old one by its date.
		if resp.Header.Get("Last-Modified") != "" {
			t.Errorf("GET %s carries Last-Modified %q", path, resp.Header.Get("Last-Modified"))
		}
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/assets/app.js", nil)
	req.Header.Set("Range", "bytes=8-10")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var part bytes.Buffer
	_, _ = part.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || part.String() != "log" {
		t.Errorf("a range = %d %q, want 206 %q", resp.StatusCode, part.String(), "log")
	}
	if _, err := fs.Stat(b.UI(), "templates/a.yaml"); err == nil {
		t.Error("the UI's files reach outside ui/")
	}
}

func TestTheTemplatesAreTheFilesUnderTemplates(t *testing.T) {
	b := opened(t, entry{name: "templates/a.yaml", body: "a"}, entry{name: "ui/index.html", body: "x"})
	got, err := fs.ReadFile(b.Templates(), "a.yaml")
	if err != nil || string(got) != "a" {
		t.Fatalf("a.yaml = %q, %v", got, err)
	}
}

func TestUnpackWritesThePlatformsFilesPrivateToTheUser(t *testing.T) {
	b := opened(t,
		entry{name: "files/pod-user.sh", body: "#!/bin/sh\n", mode: 0o755},
		entry{name: "files/envs/a/pixi.toml", body: "toml", mode: 0o664},
		entry{name: "ui/index.html", body: "x"},
	)
	dir := t.TempDir()
	at, err := b.Unpack(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(at) != dir {
		t.Fatalf("unpacked into %s, want a folder of %s", at, dir)
	}
	for name, want := range map[string]fs.FileMode{
		"": fs.ModeDir | 0o700, "pod-user.sh": 0o700, "envs": fs.ModeDir | 0o700, "envs/a": fs.ModeDir | 0o700, "envs/a/pixi.toml": 0o600,
	} {
		info, err := os.Lstat(filepath.Join(at, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode() != want {
			t.Errorf("%q has mode %v, want %v", name, info.Mode(), want)
		}
	}
	if _, err := os.Stat(filepath.Join(at, "index.html")); err == nil {
		t.Error("the UI was unpacked with the platform's files")
	}
}

// The same binary starts again without writing anything; another binary's
// files take the place of the old ones.
func TestUnpackWritesAgainOnlyWhenTheArchiveChanged(t *testing.T) {
	dir := t.TempDir()
	first := opened(t, entry{name: "files/a", body: "one"})
	at, err := first.Unpack(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A mark the first unpacking did not make: it stays while the folder
	// is not written again.
	mark := filepath.Join(at, "mark")
	if err := os.WriteFile(mark, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := opened(t, entry{name: "files/a", body: "one"}).Unpack(dir)
	if err != nil || again != at {
		t.Fatalf("the same archive unpacked into %s, %v; want %s", again, err, at)
	}
	if _, err := os.Stat(mark); err != nil {
		t.Errorf("the same archive rewrote its folder: %v", err)
	}
	next, err := opened(t, entry{name: "files/a", body: "two"}).Unpack(dir)
	if err != nil || next == at {
		t.Fatalf("a changed archive unpacked into %s, %v", next, err)
	}
	if got, _ := os.ReadFile(filepath.Join(next, "a")); string(got) != "two" {
		t.Errorf("a = %q, want two", got)
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 1 {
		t.Errorf("%d folders left in %s, want only the new one", len(left), dir)
	}
}

func TestUnpackRefusesWhatWouldLeaveItsFolder(t *testing.T) {
	for name, e := range map[string]entry{
		"a path that climbs out":  {name: "files/../../escaped", body: "x"},
		"an absolute path":        {name: "files//etc/escaped", body: "x"},
		"a symbolic link":         {name: "files/link", body: "/etc/passwd", mode: fs.ModeSymlink | 0o777},
		"a file through a folder": {name: "files/a/../../escaped", body: "x"},
	} {
		t.Run(name, func(t *testing.T) {
			outer := t.TempDir()
			dir := filepath.Join(outer, "files")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if at, err := opened(t, e).Unpack(dir); err == nil {
				t.Fatalf("unpacked into %s", at)
			}
			if _, err := os.Lstat(filepath.Join(outer, "escaped")); err == nil {
				t.Error("a file was written outside the folder")
			}
			if left, _ := os.ReadDir(dir); len(left) != 0 {
				t.Errorf("a refused archive left %d entries behind", len(left))
			}
		})
	}
}

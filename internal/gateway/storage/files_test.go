package storage_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestFilesListsAGroupFolderForAMember(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Entries: []files.Entry{{Name: "x.csv", Size: 1}}}
	f := apitest.GroupFilesFixture(t, l, apitest.Folder("/org/data-science", "ds", true, ""))
	w := f.Do("GET", "/api/files?place=group:data-science&path=data", "", f.Session(apitest.AliceID))
	if w.Code != http.StatusOK || l.Group != "data-science" || l.Path != "data" || l.User != apitest.AliceID {
		t.Fatalf("got %d %s; group %q path %q user %q", w.Code, w.Body, l.Group, l.Path, l.User)
	}
}

func TestFilesHidesAGroupFolderFromANonMember(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	f := apitest.GroupFilesFixture(t, l, apitest.Folder("/org/data-science", "ds", true, ""), apitest.Folder("/org/finance", "fin", true, ""))
	for _, place := range []string{"group:finance", "group:nope"} {
		if w := f.Do("GET", "/api/files?place="+place, "", f.Session(apitest.AliceID)); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s", place, w.Code, w.Body)
		}
	}
	if l.Calls != 0 {
		t.Errorf("the lister was reached %d times", l.Calls)
	}
}

func TestFilesRefusesABadPlaceAndExplainsAFolderNotReady(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	f := apitest.GroupFilesFixture(t, l, apitest.Folder("/org/data-science", "ds", false, reasons.GroupHasNoId))
	for _, place := range []string{"group:../x", "group:", "other", "group:a/b"} {
		if w := f.Do("GET", "/api/files?place="+place, "", f.Session(apitest.AliceID)); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", place, w.Code, w.Body)
		}
	}
	w := f.Do("GET", "/api/files?place=group:data-science", "", f.Session(apitest.AliceID))
	r, _ := reasons.Lookup(reasons.GroupHasNoId)
	if w.Code != http.StatusConflict || apitest.DecodeFilesBody(t, w)["title"] != r.Title || l.Calls != 0 {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
}

func TestFilesRequiresSignIn(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	rec := apitest.GetFiles(t, l, true, http.MethodGet, "/api/files?path=")
	if rec.Code != http.StatusUnauthorized || apitest.DecodeFilesBody(t, rec)["title"] != reasons.Msg(reasons.MsgSignInRequired) || l.Calls != 0 {
		t.Fatalf("got %d %s (calls %d)", rec.Code, rec.Body, l.Calls)
	}
}

func TestFilesOnlyGet(t *testing.T) {
	t.Parallel()
	rec := apitest.GetFiles(t, &apitest.StubFilesLister{}, false, http.MethodPost, "/api/files")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestFilesRejectsPathsOutsideHome(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"../", "..", "/../etc", "Data/../../x"} {
		l := &apitest.StubFilesLister{}
		rec := apitest.GetFiles(t, l, false, http.MethodGet, "/api/files?path="+p)
		if rec.Code != http.StatusBadRequest || apitest.DecodeFilesBody(t, rec)["title"] != reasons.Msg(reasons.MsgFolderOutsideHome) {
			t.Errorf("path %q: got %d %s", p, rec.Code, rec.Body)
		}
		if l.Calls != 0 {
			t.Errorf("path %q reached the lister", p)
		}
	}
}

func TestFilesListsCallersHomeSorted(t *testing.T) {
	t.Parallel()
	mod := time.Date(2026, 10, 30, 19, 22, 27, 0, time.UTC)
	l := &apitest.StubFilesLister{Entries: []files.Entry{
		{Name: "zeta.csv", Size: 5, ModTime: mod},
		{Name: "beta", IsDir: true, ModTime: mod},
		{Name: "Alpha.txt", Size: 1, ModTime: mod},
		{Name: "Gamma", IsDir: true, ModTime: mod},
		{Name: "alpha", IsDir: true, ModTime: mod},
	}}
	rec := apitest.GetFiles(t, l, false, http.MethodGet, "/api/files?path=/Data//sub/")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if l.User != "alice" || l.Path != "Data/sub" {
		t.Errorf("lister got user %q path %q", l.User, l.Path)
	}
	var got struct {
		Path    string `json:"path"`
		Entries []struct {
			Name     string    `json:"name"`
			Kind     string    `json:"kind"`
			Size     int64     `json:"size"`
			Modified time.Time `json:"modified"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Path != "Data/sub" {
		t.Errorf("path = %q", got.Path)
	}
	var order []string
	for _, e := range got.Entries {
		order = append(order, e.Kind+":"+e.Name)
	}
	if want := "folder:alpha folder:beta folder:Gamma file:Alpha.txt file:zeta.csv"; strings.Join(order, " ") != want {
		t.Errorf("order = %v, want %s", order, want)
	}
	if e := got.Entries[4]; e.Size != 5 || !e.Modified.Equal(mod) {
		t.Errorf("zeta.csv = %+v", e)
	}
}

func TestFilesEmptyFolderIsEmptyArray(t *testing.T) {
	t.Parallel()
	rec := apitest.GetFiles(t, &apitest.StubFilesLister{Entries: nil}, false, http.MethodGet, "/api/files")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"entries":[]`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestFilesHomeNotReady(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err    error
		status int
		code   reasons.Code
	}{
		{files.ErrHomeStarting, http.StatusServiceUnavailable, reasons.HomeStarting},
		{files.ErrHomeBusy, http.StatusConflict, reasons.HomeBusy},
		{files.ErrHomeConflict, http.StatusConflict, reasons.HomeConflict},
	}
	for _, tc := range cases {
		rec := apitest.GetFiles(t, &apitest.StubFilesLister{Err: tc.err}, false, http.MethodGet, "/api/files")
		if rec.Code != tc.status {
			t.Errorf("%s: status %d", tc.code, rec.Code)
		}
		r, _ := reasons.Lookup(tc.code)
		body := apitest.DecodeFilesBody(t, rec)
		if body["reason"] != string(tc.code) || body["title"] != r.Title || body["what"] != r.What || body["todo"] != r.Todo {
			t.Errorf("%s: body %v", tc.code, body)
		}
		if tc.code == reasons.HomeStarting && rec.Header().Get("Retry-After") != "2" {
			t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
		}
	}
}

func TestFilesMissingFolder(t *testing.T) {
	t.Parallel()
	rec := apitest.GetFiles(t, &apitest.StubFilesLister{Err: files.ErrNotFound}, false, http.MethodGet, "/api/files?path=nope")
	if rec.Code != http.StatusNotFound || apitest.DecodeFilesBody(t, rec)["title"] != reasons.Msg(reasons.MsgFolderNotFound) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestFilesInternalErrorHidesDetail(t *testing.T) {
	t.Parallel()
	rec := apitest.GetFiles(t, &apitest.StubFilesLister{Err: errors.New("dial tcp 10.0.0.5:5572: refused")}, false, http.MethodGet, "/api/files")
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestFilesReportsTruncatedFolder(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Entries: []files.Entry{{Name: "a"}, {Name: "b"}}, Truncated: true}
	rec := apitest.GetFiles(t, l, false, http.MethodGet, "/api/files?path=big")
	body := apitest.DecodeFilesBody(t, rec)
	if rec.Code != http.StatusOK || body["truncated"] != true || body["count"] != float64(2) {
		t.Fatalf("got %d %v", rec.Code, body)
	}
	rec = apitest.GetFiles(t, &apitest.StubFilesLister{Entries: []files.Entry{{Name: "a"}}}, false, http.MethodGet, "/api/files")
	if body := apitest.DecodeFilesBody(t, rec); body["truncated"] != false || body["count"] != float64(1) {
		t.Fatalf("complete folder: %v", body)
	}
}

// --- raw file content ---

func TestRawServesHTMLSandboxed(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Content: "<script>alert(1)</script>"}
	rec := apitest.GetFiles(t, l, false, http.MethodGet, "/api/files/raw?path=jobs/nightly/r1/output.html")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox") || strings.Contains(csp, "allow-same-origin") {
		t.Errorf("CSP = %q, want a sandbox without allow-same-origin", csp)
	}
	for k, want := range map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Cache-Control":                "private, no-store",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if rec.Body.String() != l.Content || l.Opened[0] != "alice:jobs/nightly/r1/output.html" {
		t.Errorf("body %q, opened %v", rec.Body, l.Opened)
	}
}

func TestRawContentTypesComeFromAnAllowList(t *testing.T) {
	t.Parallel()
	cases := []struct{ file, typ string }{
		{"a.png", "image/png"},
		{"a.jpg", "image/jpeg"},
		{"a.JPEG", "image/jpeg"},
		{"a.gif", "image/gif"},
		{"a.webp", "image/webp"},
		{"a.svg", "image/svg+xml"},
		{"a.pdf", "application/pdf"},
		{"run.log", "text/plain; charset=utf-8"},
		{"a.txt", "text/plain; charset=utf-8"},
		{"result.json", "text/plain; charset=utf-8"},
		{"A.HTML", "text/html; charset=utf-8"},
	}
	for _, tc := range cases {
		rec := apitest.GetFiles(t, &apitest.StubFilesLister{Content: "x"}, false, http.MethodGet, "/api/files/raw?path=jobs/"+tc.file)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != tc.typ || rec.Header().Get("Content-Disposition") != "" {
			t.Errorf("%s: %d type %q disposition %q", tc.file, rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Content-Disposition"))
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("%s: no sandbox CSP", tc.file)
		}
	}
}

func TestRawSendsNotebooksAndUnknownTypesAsAttachments(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"output.ipynb", "data.parquet", "noext"} {
		rec := apitest.GetFiles(t, &apitest.StubFilesLister{Content: "x"}, false, http.MethodGet, "/api/files/raw?path=jobs/"+file)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/octet-stream" ||
			!strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
			t.Errorf("%s: %d type %q disposition %q", file, rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Content-Disposition"))
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("%s: no sandbox CSP", file)
		}
	}
}

func TestRawRejectsPathsOutsideHomeAndEmptyPath(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"../bob/x", "/../bob/x", "a/../../bob/x"} {
		l := &apitest.StubFilesLister{Content: "secret"}
		rec := apitest.GetFiles(t, l, false, http.MethodGet, "/api/files/raw?path="+p)
		if rec.Code != http.StatusBadRequest || apitest.DecodeFilesBody(t, rec)["title"] != reasons.Msg(reasons.MsgFolderOutsideHome) || len(l.Opened) != 0 {
			t.Errorf("path %q: got %d %s (opened %v)", p, rec.Code, rec.Body, l.Opened)
		}
	}
	rec := apitest.GetFiles(t, &apitest.StubFilesLister{}, false, http.MethodGet, "/api/files/raw?path=")
	if rec.Code != http.StatusNotFound || apitest.DecodeFilesBody(t, rec)["title"] != reasons.Msg(reasons.MsgFileNotFound) {
		t.Errorf("empty path: got %d %s", rec.Code, rec.Body)
	}
}

func TestRawOnlyReadsTheCallersHome(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Content: "x"}
	apitest.GetFiles(t, l, false, http.MethodGet, "/api/files/raw?path=a.txt")
	if len(l.Opened) != 1 || l.Opened[0] != "alice:a.txt" {
		t.Errorf("opened %v, want only alice's home", l.Opened)
	}
	anon := &apitest.StubFilesLister{Content: "x"}
	rec := apitest.GetFiles(t, anon, true, http.MethodGet, "/api/files/raw?path=a.txt")
	if rec.Code != http.StatusUnauthorized || len(anon.Opened) != 0 {
		t.Errorf("anonymous: %d, opened %v", rec.Code, anon.Opened)
	}
}

func TestRawTooLargeIs413(t *testing.T) {
	t.Parallel()
	rec := apitest.GetFiles(t, &apitest.StubFilesLister{Err: files.ErrTooLarge}, false, http.MethodGet, "/api/files/raw?path=big.log")
	if rec.Code != http.StatusRequestEntityTooLarge || apitest.DecodeFilesBody(t, rec)["title"] != reasons.Msg(reasons.MsgOutputTooLarge) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestRawHomeStatesAndMissingFile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err    error
		status int
	}{
		{files.ErrNotFound, http.StatusNotFound},
		{files.ErrHomeStarting, http.StatusServiceUnavailable},
		{files.ErrHomeBusy, http.StatusConflict},
		{errors.New("dial tcp 10.0.0.5:5572: refused"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		rec := apitest.GetFiles(t, &apitest.StubFilesLister{Err: tc.err}, false, http.MethodGet, "/api/files/raw?path=a.txt")
		if rec.Code != tc.status || strings.Contains(rec.Body.String(), "10.0.0.5") {
			t.Errorf("%v: got %d %s", tc.err, rec.Code, rec.Body)
		}
	}
}

func TestRawOnlyGet(t *testing.T) {
	t.Parallel()
	rec := apitest.GetFiles(t, &apitest.StubFilesLister{}, false, http.MethodPost, "/api/files/raw?path=a.txt")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestRawLogsStreamThatBreaksPartway(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	l := &apitest.StubFilesLister{Body: &apitest.BrokenBody{}, Size: 100}
	apitest.GetFiles(t, l, false, http.MethodGet, "/api/files/raw?path=r1/output.html")
	out := buf.String()
	for _, want := range []string{"stream home file", "user=alice", "path=r1/output.html", "pod released"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q missing %q", out, want)
		}
	}
}

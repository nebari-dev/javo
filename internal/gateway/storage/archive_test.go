package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// putArchive sends PUT /api/files/archive as an agent's curl does: a gzip
// body, the API header, and the session cookie c or the token tok.
func putArchive(f *apitest.Fixture, c *http.Cookie, tok, target, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/gzip")
	r.Header.Set(authn.RequestHeader, "1")
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	return w
}

type uploadedJSON struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

func TestArchiveUploadMakesTheFolderAndAuditsIt(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	f := apitest.New(t, apitest.Options{Files: l})
	w := putArchive(f, f.Session(apitest.AliceID), "", "/api/files/archive?path=/deploys//site", "tar.gz bytes")
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	var got uploadedJSON
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != (uploadedJSON{Path: "deploys/site", Files: 1, Bytes: 12}) {
		t.Fatalf("answer %+v", got)
	}
	// The whole body reached Unpack, with the configured limit.
	if want := apitest.AliceID + ":deploys/site:104857600:10000:false:tar.gz bytes"; len(l.Unpacks) != 1 || l.Unpacks[0] != want {
		t.Fatalf("unpacked %q, want %q", l.Unpacks, want)
	}
	events := fileEvents(t, f)
	if len(events) != 1 || events[0].Action != "file.upload" || events[0].Actor != apitest.AliceID ||
		events[0].ObjectName != "deploys/site" || string(events[0].Detail) != `{"bytes":12}` {
		t.Fatalf("audit = %+v", events)
	}
}

func TestArchiveUploadReplacesOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Files: &apitest.StubFilesLister{UnpackErr: files.ErrExists}})
	w := putArchive(f, f.Session(apitest.AliceID), "", "/api/files/archive?path=deploys/site", "x")
	if w.Code != http.StatusConflict || apitest.DecodeFilesBody(t, w)["title"] != reasons.Msg(reasons.MsgFolderExists) {
		t.Fatalf("existing folder: got %d %s", w.Code, w.Body)
	}
	if events := fileEvents(t, f); len(events) != 0 {
		t.Fatalf("audited %+v", events)
	}

	l := &apitest.StubFilesLister{}
	f = apitest.New(t, apitest.Options{Files: l})
	w = putArchive(f, f.Session(apitest.AliceID), "", "/api/files/archive?path=deploys/site&replace=true", "x")
	if w.Code != http.StatusOK || len(l.Unpacks) != 1 || !strings.Contains(l.Unpacks[0], ":true:") {
		t.Fatalf("replace: got %d %s, unpacked %q", w.Code, w.Body, l.Unpacks)
	}
}

// Over api.limits.uploadBytes the body is refused: when it says so up
// front, before anything is unpacked, and when it only turns out longer.
func TestArchiveUploadRefusesABodyOverTheLimit(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	f := apitest.New(t, apitest.Options{Files: l, APILimits: config.APILimitsCfg{UploadBytes: 10}})
	alice := f.Session(apitest.AliceID)
	w := putArchive(f, alice, "", "/api/files/archive?path=deploys/site", strings.Repeat("a", 11))
	if w.Code != http.StatusRequestEntityTooLarge || apitest.DecodeFilesBody(t, w)["title"] != reasons.Msg(reasons.MsgUploadTooLarge, "10 bytes") {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if len(l.Unpacks) != 0 {
		t.Fatalf("unpacked %q", l.Unpacks)
	}
	// A streamed body has no length to check up front.
	r := httptest.NewRequest(http.MethodPut, "/api/files/archive?path=deploys/site", struct{ *strings.Reader }{strings.NewReader(strings.Repeat("a", 11))})
	r.ContentLength = -1
	r.Header.Set(authn.RequestHeader, "1")
	r.AddCookie(alice)
	w = httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("streamed: got %d %s", w.Code, w.Body)
	}
	if len(l.Unpacks) != 1 || strings.HasSuffix(l.Unpacks[0], strings.Repeat("a", 11)) {
		t.Fatalf("unpack read %q, want at most 10 bytes", l.Unpacks)
	}
	if events := fileEvents(t, f); len(events) != 0 {
		t.Fatalf("audited %+v", events)
	}
}

func TestArchiveUploadExplainsABadArchive(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err    error
		status int
		title  string
	}{
		{&files.BadEntryError{Name: "../etc/passwd"}, http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgArchiveEntryRefused, "../etc/passwd")},
		{files.ErrBadArchive, http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgArchiveInvalid)},
		{files.ErrEmptyArchive, http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgArchiveEmpty)},
		{files.ErrTooLarge, http.StatusRequestEntityTooLarge, reasons.Msg(reasons.MsgUploadTooLarge, "100 MiB")},
		{files.ErrTooManyEntries, http.StatusRequestEntityTooLarge, reasons.Msg(reasons.MsgUploadTooManyFiles, 10000)},
		{files.ErrNotFound, http.StatusNotFound, reasons.Msg(reasons.MsgFolderNotFound)},
		{files.ErrHomeStarting, http.StatusServiceUnavailable, ""},
		{errors.New("dial tcp 10.0.0.5:5572: refused"), http.StatusInternalServerError, ""},
	} {
		f := apitest.New(t, apitest.Options{Files: &apitest.StubFilesLister{UnpackErr: tc.err}})
		w := putArchive(f, f.Session(apitest.AliceID), "", "/api/files/archive?path=deploys/site", "x")
		if w.Code != tc.status || strings.Contains(w.Body.String(), "10.0.0.5") ||
			tc.title != "" && apitest.DecodeFilesBody(t, w)["title"] != tc.title {
			t.Errorf("%v: got %d %s", tc.err, w.Code, w.Body)
		}
	}
}

func TestArchiveUploadStaysInTheCallersHome(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	f := apitest.New(t, apitest.Options{Files: l})
	alice := f.Session(apitest.AliceID)
	for _, p := range []string{"../bob/site", "a/../../bob", "..", "", "/"} {
		if w := putArchive(f, alice, "", "/api/files/archive?path="+p, "x"); w.Code != http.StatusBadRequest {
			t.Errorf("%q: got %d %s", p, w.Code, w.Body)
		}
	}
	if len(l.Unpacks) != 0 {
		t.Fatalf("unpacked %q", l.Unpacks)
	}
}

// A personal token uploads when it may write; a read token may not, nor
// may an app's own token (it could put code in front of its owner's apps).
func TestArchiveUploadNeedsAWriteToken(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	ws := gatewaytest.Workspace("agent", apitest.LocalAlice(), "Agent")
	ws.Template = "agent"
	f := apitest.New(t, apitest.Options{Files: l}, apitest.AgentTemplate(), ws)
	c := f.LoginAlice()
	app, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{Name: "agent", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: ws.Namespace, Name: ws.ObjectName(), Short: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	if w := putArchive(f, nil, app, "/api/files/archive?path=deploys/site", "x"); w.Code != http.StatusForbidden ||
		apitest.DecodeFilesBody(t, w)["title"] != apitest.WorkloadRefusal(http.MethodPut) {
		t.Fatalf("app token: got %d %s", w.Code, w.Body)
	}
	read := f.NewToken(c, `{"name":"laptop","scope":"read"}`).Token
	if w := putArchive(f, nil, read, "/api/files/archive?path=deploys/site", "x"); w.Code != http.StatusForbidden ||
		apitest.DecodeFilesBody(t, w)["title"] != reasons.Msg(reasons.MsgTokenReadOnly) {
		t.Fatalf("read token: got %d %s", w.Code, w.Body)
	}
	if len(l.Unpacks) != 0 {
		t.Fatalf("unpacked %q", l.Unpacks)
	}
	rw := f.NewToken(c, `{"name":"agent","scope":"read-write"}`).Token
	if w := putArchive(f, nil, rw, "/api/files/archive?path=deploys/site", "x"); w.Code != http.StatusCreated {
		t.Fatalf("read-write token: got %d %s", w.Code, w.Body)
	}
	if events := fileEvents(t, f); len(events) != 1 || !strings.HasPrefix(events[0].Via, "token") {
		t.Fatalf("audit = %+v", events)
	}
}

// A page of another site cannot make the browser upload.
func TestArchiveUploadNeedsTheAPIHeader(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	f := apitest.New(t, apitest.Options{Files: l})
	r := httptest.NewRequest(http.MethodPut, "/api/files/archive?path=deploys/site", strings.NewReader("x"))
	r.Header.Set("Content-Type", "application/gzip")
	r.AddCookie(f.Session(apitest.AliceID))
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || len(l.Unpacks) != 0 {
		t.Fatalf("got %d, unpacked %q", w.Code, l.Unpacks)
	}
}

// The folder's parent listing would not show the new folder.
func TestArchiveUploadDropsCachedListings(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	f := apitest.New(t, apitest.Options{Files: l})
	alice := f.Session(apitest.AliceID)
	f.Do(http.MethodGet, "/api/files?path=deploys", "", alice)
	putArchive(f, alice, "", "/api/files/archive?path=deploys/site", "x")
	f.Do(http.MethodGet, "/api/files?path=deploys", "", alice)
	if l.Calls != 2 {
		t.Fatalf("listed %d times, want the folder listed again after the upload", l.Calls)
	}
}

// slowPut sends body to target on srv in two halves, wait apart, signed in
// as c; it answers the status, or 0 when the connection failed.
func slowPut(t *testing.T, srv *httptest.Server, c *http.Cookie, target, contentType string, wait time.Duration) int {
	t.Helper()
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("first half,"))
		time.Sleep(wait)
		_, _ = pw.Write([]byte("second half"))
		_ = pw.Close()
	}()
	r, err := http.NewRequest(http.MethodPut, srv.URL+target, pr)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", contentType)
	r.Header.Set(authn.RequestHeader, "1")
	r.Header.Set("If-Match", `"1-1"`)
	r.AddCookie(c)
	resp, err := srv.Client().Do(r)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// An upload has api.limits.uploadReadTimeout to send its body, not the
// bodyReadTimeout every other request keeps.
func TestArchiveUploadHasItsOwnReadTimeout(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	f := apitest.New(t, apitest.Options{Files: l, APILimits: config.APILimitsCfg{
		BodyReadTimeout:   metav1.Duration{Duration: 200 * time.Millisecond},
		UploadReadTimeout: metav1.Duration{Duration: 3 * time.Second},
	}})
	srv := httptest.NewServer(f.Srv)
	defer srv.Close()
	alice := f.Session(apitest.AliceID)
	if code := slowPut(t, srv, alice, "/api/files/archive?path=deploys/site", "application/gzip", 600*time.Millisecond); code != http.StatusCreated {
		t.Fatalf("slow upload within its own timeout: %d, unpacked %q", code, l.Unpacks)
	}
	if len(l.Unpacks) != 1 || !strings.HasSuffix(l.Unpacks[0], "first half,second half") {
		t.Fatalf("unpacked %q", l.Unpacks)
	}
	// Every other body still has bodyReadTimeout.
	if code := slowPut(t, srv, alice, "/api/files/raw?path=a.txt", "text/plain", 600*time.Millisecond); code == http.StatusOK {
		t.Fatal("a slow save got the upload's timeout")
	}

	l = &apitest.StubFilesLister{}
	f = apitest.New(t, apitest.Options{Files: l, APILimits: config.APILimitsCfg{
		BodyReadTimeout:   metav1.Duration{Duration: 5 * time.Second},
		UploadReadTimeout: metav1.Duration{Duration: 200 * time.Millisecond},
	}})
	srv2 := httptest.NewServer(f.Srv)
	defer srv2.Close()
	if code := slowPut(t, srv2, f.Session(apitest.AliceID), "/api/files/archive?path=deploys/site", "application/gzip", 800*time.Millisecond); code == http.StatusCreated {
		t.Fatal("an upload slower than uploadReadTimeout was not cut off")
	}
	if len(l.Unpacks) != 1 || strings.Contains(l.Unpacks[0], "second half") {
		t.Fatalf("unpacked %q, want the read cut off", l.Unpacks)
	}
}

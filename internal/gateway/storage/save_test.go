package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

var (
	readAt  = time.Date(2026, 10, 30, 19, 22, 27, 123, time.UTC)
	savedAt = time.Date(2026, 11, 3, 12, 0, 0, 7, time.UTC)
)

// etagOf is the ETag of a file of size bytes last written at t.
func etagOf(size int, t time.Time) string {
	return `"` + files.FileInfo{Size: int64(size), ModTime: t}.Version() + `"`
}

// fileEvents are the audit rows about files.
func fileEvents(t *testing.T, f *apitest.Fixture) []store.AuditEvent {
	t.Helper()
	all, err := f.Db.Audit().List(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEvent
	for _, e := range all {
		if e.ObjectKind == "file" {
			out = append(out, e)
		}
	}
	return out
}

// putFile sends PUT /api/files/raw as the UI does, signed in as alice
// (anonymous when c is nil), with the extra headers.
func putFile(f *apitest.Fixture, c *http.Cookie, target, body string, header map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set(authn.RequestHeader, "1")
	for k, v := range header {
		if v == "" {
			r.Header.Del(k)
		} else {
			r.Header.Set(k, v)
		}
	}
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	return w
}

func TestRawSendsTheFilesVersionAsItsETag(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Content: "print(1)\n", ModTime: readAt}
	rec := apitest.GetFiles(t, l, false, http.MethodGet, "/api/files/raw?path=notes/a.py")
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != etagOf(9, readAt) {
		t.Fatalf("got %d, ETag %q", rec.Code, rec.Header().Get("ETag"))
	}
}

func TestSaveWritesTheVersionTheEditorReadAndAuditsIt(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{SavedAt: savedAt}
	f := apitest.New(t, apitest.Options{Files: l})
	w := putFile(f, f.Session(apitest.AliceID), "/api/files/raw?path=/notes//a.py", "print(2)\n", map[string]string{"If-Match": etagOf(9, readAt)})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if want := apitest.AliceID + ":notes/a.py:" + strings.Trim(etagOf(9, readAt), `"`) + ":print(2)\n"; len(l.Saved) != 1 || l.Saved[0] != want {
		t.Fatalf("saved %q, want %q", l.Saved, want)
	}
	var got struct {
		Path     string    `json:"path"`
		Size     int64     `json:"size"`
		Modified time.Time `json:"modified"`
		ETag     string    `json:"etag"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Path != "notes/a.py" || got.Size != 9 || !got.Modified.Equal(savedAt) || got.ETag != etagOf(9, savedAt) || w.Header().Get("ETag") != got.ETag {
		t.Fatalf("answer %+v, ETag header %q", got, w.Header().Get("ETag"))
	}
	events := fileEvents(t, f)
	if len(events) != 1 || events[0].Action != "file.save" || events[0].Actor != apitest.AliceID || events[0].Via != "session" ||
		events[0].ObjectKind != "file" || events[0].ObjectName != "notes/a.py" || events[0].ObjectOwner != apitest.AliceID {
		t.Fatalf("audit = %+v", events)
	}
}

func TestSaveCreatesANewFileWithIfNoneMatch(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{SavedAt: savedAt}
	f := apitest.New(t, apitest.Options{Files: l})
	w := putFile(f, f.Session(apitest.AliceID), "/api/files/raw?path=notes/new.md", "", map[string]string{"If-None-Match": "*"})
	if w.Code != http.StatusCreated || len(l.Saved) != 1 || l.Saved[0] != apitest.AliceID+":notes/new.md::" {
		t.Fatalf("got %d %s, saved %q", w.Code, w.Body, l.Saved)
	}
	if events := fileEvents(t, f); len(events) != 1 || events[0].Action != "file.create" {
		t.Fatalf("audit = %+v", events)
	}
}

// A save that names no version would overwrite whatever is there.
func TestSaveNeedsTheVersionItReplaces(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	f := apitest.New(t, apitest.Options{Files: l})
	for _, h := range []map[string]string{nil, {"If-None-Match": `"3-1"`}, {"If-Match": "*"}} {
		w := putFile(f, f.Session(apitest.AliceID), "/api/files/raw?path=a.py", "x", h)
		if w.Code != http.StatusPreconditionRequired {
			t.Errorf("%v: got %d %s", h, w.Code, w.Body)
		}
	}
	if len(l.Saved) != 0 {
		t.Fatalf("saved %q", l.Saved)
	}
}

func TestSaveExplainsAConflict(t *testing.T) {
	t.Parallel()
	for err, msg := range map[error]reasons.MessageCode{
		files.ErrChanged: reasons.MsgFileChanged,
		files.ErrExists:  reasons.MsgFileExists,
	} {
		f := apitest.New(t, apitest.Options{Files: &apitest.StubFilesLister{SaveErr: err}})
		w := putFile(f, f.Session(apitest.AliceID), "/api/files/raw?path=a.py", "x", map[string]string{"If-Match": `"1-1"`})
		if w.Code != http.StatusConflict || apitest.DecodeFilesBody(t, w)["title"] != reasons.Msg(msg) {
			t.Errorf("%v: got %d %s", err, w.Code, w.Body)
		}
		if events := fileEvents(t, f); len(events) != 0 {
			t.Errorf("%v: audited %+v", err, events)
		}
	}
}

func TestSaveRefusesWhatCannotBeEdited(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err    error
		status int
		msg    reasons.MessageCode
	}{
		{files.ErrNotText, http.StatusUnprocessableEntity, reasons.MsgFileNotText},
		{files.ErrTooLarge, http.StatusRequestEntityTooLarge, reasons.MsgFileTooLargeToEdit},
		{files.ErrNotFound, http.StatusNotFound, reasons.MsgFileNotFound},
		{files.ErrHomeStarting, http.StatusServiceUnavailable, ""},
		{errors.New("dial tcp 10.0.0.5:5572: refused"), http.StatusInternalServerError, ""},
	} {
		f := apitest.New(t, apitest.Options{Files: &apitest.StubFilesLister{SaveErr: tc.err}})
		w := putFile(f, f.Session(apitest.AliceID), "/api/files/raw?path=a.py", "x", map[string]string{"If-Match": `"1-1"`})
		if w.Code != tc.status || strings.Contains(w.Body.String(), "10.0.0.5") ||
			tc.msg != "" && apitest.DecodeFilesBody(t, w)["title"] != reasons.Msg(tc.msg) {
			t.Errorf("%v: got %d %s", tc.err, w.Code, w.Body)
		}
	}
}

// The gateway reads no more than the cap: a longer body is refused before
// the file pod is asked.
func TestSaveRefusesABodyOverTheCapUnread(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	f := apitest.New(t, apitest.Options{Files: l})
	w := putFile(f, f.Session(apitest.AliceID), "/api/files/raw?path=a.py", strings.Repeat("a", files.MaxEditBytes+1), map[string]string{"If-Match": `"1-1"`})
	if w.Code != http.StatusRequestEntityTooLarge || len(l.Saved) != 0 {
		t.Fatalf("got %d, saved %d", w.Code, len(l.Saved))
	}
}

func TestSaveStaysInTheCallersHome(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	f := apitest.New(t, apitest.Options{Files: l})
	alice := f.Session(apitest.AliceID)
	for _, p := range []string{"../bob/a.py", "/../bob/a.py", "a/../../bob/a.py", "..", "%2e%2e/bob/a.py"} {
		w := putFile(f, alice, "/api/files/raw?path="+p, "x", map[string]string{"If-Match": `"1-1"`})
		if w.Code != http.StatusBadRequest {
			t.Errorf("%q: got %d %s", p, w.Code, w.Body)
		}
	}
	if w := putFile(f, alice, "/api/files/raw?path=", "x", map[string]string{"If-None-Match": "*"}); w.Code != http.StatusNotFound {
		t.Errorf("empty path: got %d", w.Code)
	}
	if len(l.Saved) != 0 {
		t.Fatalf("saved %q", l.Saved)
	}
}

// Another site's page cannot make the browser save: the UI's header is
// required, as on every other write. Signing in is too.
func TestSaveNeedsTheUIsHeaderAndASession(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	f := apitest.New(t, apitest.Options{Files: l})
	ok := map[string]string{"If-Match": `"1-1"`}
	if w := putFile(f, f.Session(apitest.AliceID), "/api/files/raw?path=a.py", "x", map[string]string{"If-Match": `"1-1"`, authn.RequestHeader: ""}); w.Code != http.StatusForbidden {
		t.Errorf("without the header: %d", w.Code)
	}
	if w := putFile(f, f.Session(apitest.AliceID), "/api/files/raw?path=a.py", "x", map[string]string{"If-Match": `"1-1"`, "Sec-Fetch-Site": "cross-site"}); w.Code != http.StatusForbidden {
		t.Errorf("cross-site: %d", w.Code)
	}
	if w := putFile(f, nil, "/api/files/raw?path=a.py", "x", ok); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", w.Code)
	}
	if len(l.Saved) != 0 {
		t.Fatalf("saved %q", l.Saved)
	}
}

// The folder's cached listing would show the old size and time.
func TestSaveDropsCachedListings(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Entries: []files.Entry{{Name: "a.py", Size: 1}}}
	f := apitest.New(t, apitest.Options{Files: l})
	alice := f.Session(apitest.AliceID)
	f.Do(http.MethodGet, "/api/files?path=notes", "", alice)
	f.Do(http.MethodGet, "/api/files?path=notes", "", alice)
	if l.Calls != 1 {
		t.Fatalf("listed %d times before the save, want 1 (cached)", l.Calls)
	}
	putFile(f, alice, "/api/files/raw?path=notes/a.py", "xy", map[string]string{"If-Match": `"1-1"`})
	f.Do(http.MethodGet, "/api/files?path=notes", "", alice)
	if l.Calls != 2 {
		t.Fatalf("listed %d times, want the folder listed again after the save", l.Calls)
	}
}

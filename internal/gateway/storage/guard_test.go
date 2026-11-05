package storage_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// memberRoles makes /org/members the members and javo-admins the platform
// admins.
func memberRoles() *config.Roles {
	r := config.Roles{}
	r.Member.Groups = []string{"/org/members"}
	r.PlatformAdmin.Groups = []string{"javo-admins"}
	return &r
}

// A signed-in person who is not a member gets no home: every files route
// answers 403 and reaches nothing that would make the home or its file pod,
// and /api/storage leaves usage out rather than start a pod.
func TestHomeRoutesNeedTheMemberRole(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Entries: []files.Entry{{Name: "a.txt", Size: 1}}, Content: "x"}
	u := &apitest.StubUsage{Got: files.HomeUsage{Enforced: true, Used: 1, Total: 2}}
	carolHome := apitest.HomeOf(apitest.CarolID, func(h *model.Home) { h.Status.SizeEnforced = true })
	f := apitest.New(t, apitest.Options{Files: l, Usage: u, Roles: memberRoles()}, apitest.ClusterStorage(), carolHome)
	carol := f.Session(apitest.CarolID)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/files?path=", ""},
		{"POST", "/api/files/warm", ""},
		{"GET", "/api/files/raw?path=a.txt", ""},
		{"PUT", "/api/files/raw?path=a.txt", "new"},
	} {
		w := f.Do(c.method, c.path, c.body, carol)
		if w.Code != http.StatusForbidden || apitest.DecodeFilesBody(t, w)["title"] != reasons.Msg(reasons.MsgNotAMember) {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	if w := f.Do("GET", "/api/storage?usage=true", "", carol); w.Code != http.StatusOK || apitest.Decode[apitest.StorageView](t, w).Used != nil {
		t.Errorf("storage: %d %s", w.Code, w.Body)
	}
	l.Mu.Lock()
	calls, opened, saved := l.Calls, len(l.Opened), len(l.Saved)
	l.Mu.Unlock()
	if calls != 0 || opened != 0 || saved != 0 || u.Calls != 0 {
		t.Fatalf("reached the home: lister %d, opened %d, saved %d, usage %d", calls, opened, saved, u.Calls)
	}
	if w := f.Do("GET", "/api/files?path=", "", f.Session(apitest.AliceID, "/org/members")); w.Code != http.StatusOK {
		t.Fatalf("member: %d %s", w.Code, w.Body)
	}
}

// GETs that can make a home or start its file pod are not triggered by a
// page of an app or of another site: the listing needs the web UI's
// X-Javo-Request header, and a raw file, which the web UI also loads as an
// image, a frame or a link, needs a request from the UI's own pages.
func TestHomeStartingReadsAreRefusedFromAppsAndOtherSites(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Entries: []files.Entry{{Name: "a.png", Size: 1}}, Content: "x"}
	u := &apitest.StubUsage{Got: files.HomeUsage{Enforced: true, Used: 1, Total: 2}}
	home := apitest.HomeOf(apitest.AliceID, func(h *model.Home) { h.Status.SizeEnforced = true })
	f := apitest.New(t, apitest.Options{Files: l, Usage: u}, apitest.ClusterStorage(), home)
	alice := f.Session(apitest.AliceID)
	send := func(path string, hdrs ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "javo.example.com"
		for i := 0; i+1 < len(hdrs); i += 2 {
			r.Header.Set(hdrs[i], hdrs[i+1])
		}
		r.AddCookie(alice)
		w := httptest.NewRecorder()
		f.Srv.ServeHTTP(w, r)
		return w
	}
	ui := []string{"X-Javo-Request", "1", "Sec-Fetch-Site", "same-origin"}
	for _, c := range []struct {
		name, path string
		hdrs       []string
		ok         bool
	}{
		{"listing from the web UI", "/api/files?path=", ui, true},
		{"listing without the UI's header", "/api/files?path=", []string{"Sec-Fetch-Site", "same-origin"}, false},
		{"listing from another app's host", "/api/files?path=", []string{"X-Javo-Request", "1", "Sec-Fetch-Site", "same-site"}, false},
		{"usage without the UI's header", "/api/storage?usage=true", []string{"Sec-Fetch-Site", "same-site"}, false},
		{"raw file as an image of the web UI", "/api/files/raw?path=a.png", []string{"Sec-Fetch-Site", "same-origin", "Sec-Fetch-Dest", "image"}, true},
		{"raw file from an app's host", "/api/files/raw?path=a.png", []string{"Sec-Fetch-Site", "same-site", "Sec-Fetch-Dest", "image"}, false},
		{"raw file from another site", "/api/files/raw?path=a.png", []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Dest", "image"}, false},
		{"raw file from a page under /u/", "/api/files/raw?path=a.png", []string{"Sec-Fetch-Site", "same-origin", "Referer", "https://javo.example.com/u/bob/site/"}, false},
	} {
		l.Mu.Lock()
		calls, opened := l.Calls, len(l.Opened)
		l.Mu.Unlock()
		usage := u.Calls
		w := send(c.path, c.hdrs...)
		l.Mu.Lock()
		reached := l.Calls != calls || len(l.Opened) != opened || u.Calls != usage
		l.Mu.Unlock()
		if c.ok && (w.Code != http.StatusOK || !reached) {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
		if !c.ok && (w.Code != http.StatusForbidden || reached || !strings.Contains(w.Body.String(), reasons.Msg(reasons.MsgNotAllowedFromApp))) {
			t.Errorf("%s: %d %s (reached %v)", c.name, w.Code, w.Body, reached)
		}
	}
}

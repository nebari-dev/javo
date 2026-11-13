package authlocal_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2/humatest"

	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/features/authlocal"
)

// linked is a provider with a sign-in link for alice; printed is every
// link it announced, as the path and query to open.
func linked(t *testing.T) (h *fakeHost, link *authlocal.Link, printed *[]string) {
	t.Helper()
	users := &fakeUsers{users: map[string]local.User{"alice": {ID: local.UserID("alice"), Name: "alice", Groups: []string{"admins"}}}}
	printed = &[]string{}
	link = authlocal.NewLink("alice", func(l string) { *printed = append(*printed, l) })
	p := authlocal.New(authlocal.Config{Users: users, RevalidateEvery: time.Minute, Link: link})
	_, api := humatest.New(t)
	h = &fakeHost{api: api, routes: http.NewServeMux()}
	p.Mount(h)
	return h, link, printed
}

func open(h *fakeHost, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.routes.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

func TestTheSignInLinkSignsInTheAccountOnce(t *testing.T) {
	h, link, printed := linked(t)
	link.Announce()
	link.Announce()
	if len(*printed) != 1 {
		t.Fatalf("announced %d links, want the one", len(*printed))
	}
	first := (*printed)[0]
	u, err := url.Parse(first)
	if err != nil || u.Path != authlocal.LinkPath || len(u.Query().Get("token")) < 26 {
		t.Fatalf("the link %q, %v", first, err)
	}

	w := open(h, first)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("opening the link: %d to %q", w.Code, w.Header().Get("Location"))
	}
	if got := w.Result().Cookies(); len(got) != 1 || got[0].Value != "tok" {
		t.Fatalf("cookies %v", got)
	}
	if len(h.completed) != 1 || h.completed[0].Issuer != "local" || h.completed[0].Subject != "alice" || h.completed[0].Groups[0] != "admins" {
		t.Fatalf("signed in %+v", h.completed)
	}
	if s := h.sessions[0]; s.Provider != "local" || s.Kind != session.KindUI || !s.RevalidateAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("session %+v", s)
	}
	// The link is not kept by the browser or passed on.
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("headers %v", w.Header())
	}

	// A second time it opens nothing, and the next link is announced: the
	// person asks for one by opening the page.
	if w := open(h, first); w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
		t.Fatalf("the link a second time: %d", w.Code)
	}
	if len(h.completed) != 1 {
		t.Fatal("the used link signed in again")
	}
	if len(*printed) != 2 || (*printed)[1] == first {
		t.Fatalf("after the link was used and the page asked for: %v", *printed)
	}
	if w := open(h, (*printed)[1]); w.Code != http.StatusSeeOther {
		t.Fatalf("the next link: %d", w.Code)
	}
}

// Guessing prints nothing and signs nobody in: a new link is announced
// only once the last one was used.
func TestAWrongTokenSignsNobodyInAndPrintsNothingNew(t *testing.T) {
	h, link, printed := linked(t)
	link.Announce()
	for _, target := range []string{
		authlocal.LinkPath, authlocal.LinkPath + "?token=", authlocal.LinkPath + "?token=guess",
		authlocal.LinkPath + "?token=" + strings.Repeat("A", 26),
	} {
		if w := open(h, target); w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
			t.Errorf("GET %s: %d", target, w.Code)
		}
	}
	if len(h.completed) != 0 || len(*printed) != 1 {
		t.Fatalf("signed in %d times, announced %d links", len(h.completed), len(*printed))
	}
	// The link still works after the guesses.
	if w := open(h, (*printed)[0]); w.Code != http.StatusSeeOther {
		t.Fatalf("the link after wrong guesses: %d", w.Code)
	}
}

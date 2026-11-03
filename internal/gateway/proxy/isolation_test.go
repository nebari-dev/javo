package proxy

import (
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/reasons"
)

// The headers a browser sends with a request to app B (/u/bob/nb/), and
// whether the isolation guard lets it through.
func TestFromAnotherApp(t *testing.T) {
	t.Parallel()
	const (
		host = "apps.example.com"
		self = "https://apps.example.com/u/bob/nb/lab"
		peer = "https://apps.example.com/u/alice/site/"
	)
	cases := []struct {
		name                  string
		site, mode, dest, ref string
		refused               bool
	}{
		{name: "no fetch metadata (curl, an API client)", refused: false},
		{name: "no fetch metadata, Referer of another app", ref: peer, refused: false},
		{name: "fetch from the app's own page", site: "same-origin", mode: "cors", dest: "empty", ref: self},
		{name: "script from the app's own page", site: "same-origin", mode: "no-cors", dest: "script", ref: self},
		{name: "fetch from the app's bare base address", site: "same-origin", mode: "cors", dest: "empty", ref: "https://apps.example.com/u/bob/nb"},
		{name: "fetch from another app's page", site: "same-origin", mode: "cors", dest: "empty", ref: peer, refused: true},
		{name: "image from another app's page", site: "same-origin", mode: "no-cors", dest: "image", ref: peer, refused: true},
		{name: "fetch from a sibling whose name starts the same", site: "same-origin", mode: "cors", dest: "empty", ref: "https://apps.example.com/u/bob/nb2/", refused: true},
		{name: "same-origin fetch without Referer", site: "same-origin", mode: "cors", dest: "empty", refused: true},
		{name: "same-origin fetch with an unparsable Referer", site: "same-origin", mode: "cors", dest: "empty", ref: "://bad", refused: true},
		{name: "fetch from a page of the same origin that is no app (the UI on one host)", site: "same-origin", mode: "cors", dest: "empty", ref: "https://apps.example.com/apps"},
		{name: "link followed from another app", site: "same-origin", mode: "navigate", dest: "document", ref: peer},
		{name: "new tab opened from another app without Referer", site: "same-origin", mode: "navigate", dest: "document"},
		{name: "address typed or bookmarked", site: "none", mode: "navigate", dest: "document"},
		{name: "link from another site", site: "cross-site", mode: "navigate", dest: "document", ref: "https://example.org/"},
		{name: "iframe on the app's own page", site: "same-origin", mode: "navigate", dest: "iframe", ref: self},
		{name: "iframe on another app's page", site: "same-origin", mode: "navigate", dest: "iframe", ref: peer, refused: true},
		{name: "iframe without Referer", site: "same-origin", mode: "navigate", dest: "iframe", refused: true},
		{name: "frame on another app's page", site: "same-origin", mode: "navigate", dest: "frame", ref: peer, refused: true},
		{name: "embed on another app's page", site: "same-origin", mode: "no-cors", dest: "embed", ref: peer, refused: true},
		{name: "object on another app's page", site: "same-origin", mode: "no-cors", dest: "object", ref: peer, refused: true},
		{name: "iframe on another site", site: "cross-site", mode: "navigate", dest: "iframe", ref: "https://example.org/", refused: true},
		{name: "iframe on another host whose path looks like the app", site: "cross-site", mode: "navigate", dest: "iframe", ref: "https://example.org/u/bob/nb/", refused: true},
		{name: "iframe after the ticket hop (Referer cut to the origin)", site: "same-origin", mode: "navigate", dest: "iframe", ref: "https://apps.example.com/", refused: true},
		{name: "cross-site fetch (cookies are the browser's SameSite business)", site: "cross-site", mode: "cors", dest: "empty", ref: "https://example.org/"},
		// A WebSocket handshake carries no Referer (the WebSockets standard
		// sets it to no-referrer), so it cannot be attributed to a page.
		{name: "websocket", site: "same-origin", mode: "websocket", dest: "empty"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "https://"+host+"/u/bob/nb/api/kernels", nil)
		for k, v := range map[string]string{"Sec-Fetch-Site": c.site, "Sec-Fetch-Mode": c.mode, "Sec-Fetch-Dest": c.dest, "Referer": c.ref} {
			if v != "" {
				r.Header.Set(k, v)
			}
		}
		if got := fromAnotherApp(r, "/u/bob/nb/"); got != c.refused {
			t.Errorf("%s: refused = %v, want %v", c.name, got, c.refused)
		}
	}
}

// The proxy refuses a request another app's page makes, before the app sees
// it: navigations (a frame) get the branded page, other requests plain text.
func TestProxyRefusesRequestsFromAnotherApp(t *testing.T) {
	t.Parallel()
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("backend reached") }))
	cookie := f.login(t, "alice-aaaaaaaa", "alice")
	send := func(mode, dest string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/u/"+f.aliceSl+"/nb1/", nil)
		r.AddCookie(cookie)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Sec-Fetch-Mode", mode)
		r.Header.Set("Sec-Fetch-Dest", dest)
		r.Header.Set("Referer", "https://javo.example.com/u/mallory-cccccccc/site/")
		return f.do(r)
	}
	want := reasons.Msg(reasons.MsgOpenedFromAnotherApp)
	frame := send("navigate", "iframe")
	if frame.Code != http.StatusForbidden || !strings.Contains(frame.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(frame.Body.String(), html.EscapeString(want)) {
		t.Fatalf("frame: %d %q %s", frame.Code, frame.Header().Get("Content-Type"), frame.Body)
	}
	call := send("cors", "empty")
	if call.Code != http.StatusForbidden || !strings.HasPrefix(call.Header().Get("Content-Type"), "text/plain") ||
		strings.TrimSpace(call.Body.String()) != want {
		t.Fatalf("fetch: %d %q %s", call.Code, call.Header().Get("Content-Type"), call.Body)
	}
}

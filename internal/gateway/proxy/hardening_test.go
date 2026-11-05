package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
)

// An app cannot set a cookie with no name: browsers send "=javo_session=x"
// as "javo_session=x", which the gateway would read as its own session. Nor
// can it plant a ticket nonce it knows for another app's path.
func TestANamelessSetCookieIsDropped(t *testing.T) {
	t.Parallel()
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Add("Set-Cookie", "=javo_session=attacker; Path=/api")
	resp.Header.Add("Set-Cookie", "=plain; Path=/")
	resp.Header.Add("Set-Cookie", "theme=dark; Path=/")
	resp.Header.Add("Set-Cookie", "javo_ticket=known; Path=/u/alice/sales")
	_ = sanitizeResponse(resp, "/")
	if got := resp.Header.Values("Set-Cookie"); len(got) != 1 || got[0] != "theme=dark; Path=/" {
		t.Fatalf("kept %q", got)
	}
}

// An app sets cookies for itself only: never for a parent domain, and in path
// mode never for a path outside its own, where the browser would send them
// to the web UI and to every other app. Cookies named like the gateway's are
// dropped whatever their case or prefix.
func TestAppCookiesStayInTheAppsScope(t *testing.T) {
	t.Parallel()
	const base = "/u/alice-1a2b3c4d/sales/"
	for _, tc := range []struct {
		name, base, in, want string
	}{
		{"path mode: a parent domain and the root path", base, "bomb=x; Domain=example.com; Path=/", "bomb=x; Path=/u/alice-1a2b3c4d/sales"},
		{"path mode: no path at all", base, "theme=dark; HttpOnly", "theme=dark; HttpOnly; Path=/u/alice-1a2b3c4d/sales"},
		{"path mode: a sibling app's path", base, "x=1; path=/u/alice-1a2b3c4d/sales-admin", "x=1; Path=/u/alice-1a2b3c4d/sales"},
		{"path mode: a path inside the app is kept", base, "x=1; Path=/u/alice-1a2b3c4d/sales/lab; Secure; SameSite=Lax", "x=1; Secure; SameSite=Lax; Path=/u/alice-1a2b3c4d/sales/lab"},
		{"path mode: the app's own base is kept", base, "x=1; Path=/u/alice-1a2b3c4d/sales", "x=1; Path=/u/alice-1a2b3c4d/sales"},
		{"path mode: a dot-segment path is not inside", base, "x=1; Path=/u/alice-1a2b3c4d/sales/../other", "x=1; Path=/u/alice-1a2b3c4d/sales"},
		{"path mode: a __Host- cookie cannot be valid", base, "__Host-x=1; Path=/; Secure", ""},
		{"path mode: a __Secure- cookie is scoped", base, "__Secure-x=1; Path=/; Secure", "__Secure-x=1; Secure; Path=/u/alice-1a2b3c4d/sales"},
		{"subdomain: the domain goes, the path stays", "/", "bomb=x; Domain=.apps.example.com; Path=/", "bomb=x; Path=/"},
		{"subdomain: a __Host- cookie stays valid", "/", "__Host-x=1; Path=/; Secure", "__Host-x=1; Path=/; Secure"},
		{"gateway name in another case", "/", "JAVO_SESSION=x; Path=/", ""},
		{"gateway prefix under __Secure-", "/", "__Secure-javo_app=x; Path=/; Secure", ""},
		{"a new gateway-like name", "/", "javo_anything=x", ""},
	} {
		resp := &http.Response{Header: http.Header{"Set-Cookie": {tc.in}}}
		_ = sanitizeResponse(resp, tc.base)
		got := strings.Join(resp.Header.Values("Set-Cookie"), "|")
		if got != tc.want {
			t.Errorf("%s: %q became %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// A visitor's API token never reaches an app, whatever form the header has.
func TestEveryAuthorizationWithAnAPITokenIsStripped(t *testing.T) {
	t.Parallel()
	for _, hdrs := range [][]string{
		{"Bearer\tjavo_abc"},
		{"Basic eDp5", "Bearer javo_abc"},
		{"bearer  javo_abc"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		for _, h := range hdrs {
			r.Header.Add("Authorization", h)
		}
		stripAPIToken(r)
		for _, v := range r.Header.Values("Authorization") {
			if strings.Contains(v, "javo_") {
				t.Fatalf("%q kept %q", hdrs, r.Header.Values("Authorization"))
			}
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Basic eDp5")
	stripAPIToken(r)
	if r.Header.Get("Authorization") != "Basic eDp5" {
		t.Fatal("the app's own Authorization was removed")
	}
}

// Headers a client sends to pass itself off as the front proxy do not reach
// the app.
func TestClientForwardingHeadersDoNotReachTheApp(t *testing.T) {
	t.Parallel()
	var got seen
	f := setup(t, recordingBackend(&got))
	hdrs := []string{"X-Real-Ip", "X-Scheme", "X-Forwarded-Scheme", "X-Original-Url", "X-Rewrite-Url", "X-Forwarded-Ssl", "X-Original-Host", "X-Client-Ip", "True-Client-Ip", "Forwarded"}
	var kv []string
	for _, h := range hdrs {
		kv = append(kv, h, "6.6.6.6")
	}
	if w := f.get(t, f.proxy, "/u/"+f.aliceSl+"/nb1/x", kv...); w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	for _, h := range hdrs {
		if v := got.header.Get(h); v != "" {
			t.Errorf("the app saw %s=%q", h, v)
		}
	}
}

// An app's response can be framed by its own origin only, unless the app
// says otherwise itself.
func TestAppResponsesGetFrameProtectionUnlessTheySetTheirOwn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		set      http.Header
		wantOurs bool
	}{
		{"nothing", http.Header{}, true},
		{"a CSP without frame-ancestors", http.Header{"Content-Security-Policy": {"default-src 'self'"}}, true},
		{"frame-ancestors", http.Header{"Content-Security-Policy": {"frame-ancestors https://intranet.example"}}, false},
		{"X-Frame-Options", http.Header{"X-Frame-Options": {"DENY"}}, false},
	} {
		resp := &http.Response{Header: tc.set.Clone()}
		_ = sanitizeResponse(resp, "/")
		ours := false
		for _, v := range resp.Header.Values("Content-Security-Policy") {
			ours = ours || v == "frame-ancestors 'self'"
		}
		if ours != tc.wantOurs {
			t.Errorf("%s: CSP %q", tc.name, resp.Header.Values("Content-Security-Policy"))
		}
	}
}

// 103 Early Hints from an app do not reach the browser: ReverseProxy sends
// 1xx headers past ModifyResponse, so they would skip the cookie filter.
func TestInformationalResponsesFromTheAppAreDropped(t *testing.T) {
	t.Parallel()
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", "</evil.js>; rel=preload")
		w.Header().Set("Set-Cookie", "javo_session=attacker; Path=/")
		w.WriteHeader(http.StatusEarlyHints)
		w.Header().Del("Link")
		w.Header().Del("Set-Cookie")
		_, _ = io.WriteString(w, "ok")
	}))
	s := httptest.NewServer(f.proxy)
	t.Cleanup(s.Close)
	var hints []int
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		Got1xxResponse: func(code int, _ textproto.MIMEHeader) error { hints = append(hints, code); return nil },
	})
	r, _ := http.NewRequestWithContext(ctx, "GET", s.URL+"/u/"+f.aliceSl+"/nb1/", nil)
	r.AddCookie(f.login(t, "alice-aaaaaaaa", "alice"))
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || len(hints) != 0 {
		t.Fatalf("status %d, informational answers %v", resp.StatusCode, hints)
	}
}

// Browsers without Fetch Metadata still send Origin: a websocket handshake or
// a form post whose Origin is another host is refused, whatever
// Sec-Fetch-Site says (or does not).
func TestAForeignOriginCannotOpenAWebsocketOrPost(t *testing.T) {
	t.Parallel()
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	path := "/u/" + f.aliceSl + "/nb1/"
	try := func(method, origin string, upgrade bool) int {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(f.login(t, "alice-aaaaaaaa", "alice"))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if upgrade {
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
		}
		return f.do(r).Code
	}
	if c := try("GET", "https://evil.example", true); c != http.StatusForbidden {
		t.Fatalf("websocket from another origin: %d", c)
	}
	if c := try("POST", "https://evil.example", false); c != http.StatusForbidden {
		t.Fatalf("post from another origin: %d", c)
	}
	if c := try("POST", "null", false); c != http.StatusForbidden {
		t.Fatalf("post from an opaque origin: %d", c)
	}
	if c := try("POST", "http://example.com", false); c == http.StatusForbidden {
		t.Fatalf("post from the app's own origin: %d", c)
	}
	if c := try("GET", "https://evil.example", false); c != http.StatusOK {
		t.Fatalf("a plain GET from another origin (a link): %d", c)
	}
}

// A request for an app its owner stopped is answered at once: nothing listens,
// so waiting for a connection would only let anonymous traffic hold the
// gateway's goroutines for DialWait each.
func TestAStoppedAppIsAnsweredWithoutWaitingToDial(t *testing.T) {
	t.Parallel()
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	app, err := f.db.Apps().GetByOwnerName(context.Background(), f.aliceSl, "nb1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Apps().UpdateSpec(context.Background(), gatewaytest.Actor, app.ID, app.Version, func(a *model.App) error {
		a.Paused = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.backend.Close() // nothing listens
	p := New(Config{Store: f.db, Sessions: f.store, Revalidator: confirming(f.store), Exposure: gatewaytest.OneHostExposure, Activity: f.activity,
		DialWait: 5 * time.Second,
		Resolve: func(context.Context, *model.App, *model.Template) (Backend, error) {
			return Backend{URL: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}}, nil
		}})
	start := time.Now()
	w := f.get(t, p, "/u/"+f.aliceSl+"/nb1/")
	if took := time.Since(start); took > time.Second || w.Code != http.StatusBadGateway {
		t.Fatalf("stopped app: %d after %s", w.Code, took)
	}
}

// The proxy's own pages (not found, refused, unreachable) run no script and
// cannot be framed by another site.
func TestTheProxysOwnPagesCarryAPolicy(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	notFound(w)
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "frame-ancestors 'self'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP %q lacks %q", csp, want)
		}
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("no nosniff")
	}
}

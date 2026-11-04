package proxy

import (
	"bufio"
	"context"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/activity"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/features/exposurepath"
	"github.com/nebari-dev/javo/internal/features/exposuresubdomain"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/gateway/nebi"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

type fixture struct {
	proxy   http.Handler
	store   *session.DBStore
	backend *httptest.Server
	aliceSl string
	db      *store.Store
	// activity is the tracker the proxy records use in.
	activity *activity.Tracker
}

func setup(t *testing.T, backend http.Handler) *fixture {
	t.Helper()
	db := storetest.New(t)
	gatewaytest.Seed(t, db, []any{gatewaytest.App("alice-aaaaaaaa", "nb1")})
	store := &session.DBStore{Sessions: db.Sessions()}
	be := httptest.NewServer(backend)
	t.Cleanup(be.Close)
	u, _ := url.Parse(be.URL)
	tr := activity.NewTracker(nil)
	p := New(Config{
		Store: db, Sessions: store, Revalidator: confirming(store), Exposure: gatewaytest.OneHostExposure, DialWait: 200 * time.Millisecond, Activity: tr,
		Resolve: func(context.Context, *model.App, *model.Template) (Backend, error) { return Backend{URL: u}, nil },
	})
	return &fixture{db: db, proxy: p, store: store, backend: be, aliceSl: "alice-aaaaaaaa", activity: tr}
}

func (f *fixture) login(t *testing.T, id, name string) *http.Cookie {
	t.Helper()
	tok, err := f.store.Create(context.Background(), session.Session{Principal: authz.Principal{ID: id, Username: name}, Provider: "local", Kind: session.KindUI})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: model.CookieName(model.SessionCookieName, true), Value: tok}
}

func (f *fixture) do(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.proxy.ServeHTTP(w, r)
	return w
}

func TestOwnerGetsBackendResponse(t *testing.T) {
	t.Parallel()
	var gotPath, gotQuery, gotCookie, gotXFH string
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotXFH = r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Forwarded-Host")
		gotCookie = r.Header.Get("Cookie")
		io.WriteString(w, "hello app")
	}))
	path := "/u/" + f.aliceSl + "/nb1/lab/tree?x=1"
	r := httptest.NewRequest("GET", path, nil)
	r.Host = "javo.example.com"
	r.AddCookie(f.login(t, "alice-aaaaaaaa", "alice"))
	r.AddCookie(&http.Cookie{Name: "app_pref", Value: "dark"})
	w := f.do(r)
	if w.Code != 200 || w.Body.String() != "hello app" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	if gotPath != "/u/"+f.aliceSl+"/nb1/lab/tree" || gotQuery != "x=1" {
		t.Fatalf("backend saw %q ? %q", gotPath, gotQuery)
	}
	if strings.Contains(gotCookie, model.SessionCookieName) || !strings.Contains(gotCookie, "app_pref=dark") {
		t.Fatalf("cookie header to app: %q", gotCookie)
	}
	if gotXFH != "javo.example.com" {
		t.Fatalf("X-Forwarded-Host %q", gotXFH)
	}
}

func TestNotOwnerAndMissingLookIdentical(t *testing.T) {
	t.Parallel()
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("backend reached") }))
	bob := f.login(t, "bob-bbbbbbbb", "bob")
	get := func(p string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", p, nil)
		r.AddCookie(bob)
		return f.do(r)
	}
	notMine := get("/u/" + f.aliceSl + "/nb1/")
	missing := get("/u/" + f.aliceSl + "/nope/")
	wrongSlug := get("/u/" + "bob-bbbbbbbb" + "/nb1/")
	for _, w := range []*httptest.ResponseRecorder{notMine, missing, wrongSlug} {
		if w.Code != 404 || w.Body.String() != notMine.Body.String() {
			t.Fatalf("got %d %q", w.Code, w.Body.String())
		}
	}
}

func TestSameShortNameForTwoUsersReachesEachOwnersApp(t *testing.T) {
	t.Parallel()
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	gatewaytest.Seed(t, f.db, []any{gatewaytest.App("bob-bbbbbbbb", "nb1")})
	var resolved []string
	f.proxy = New(Config{Store: f.db, Sessions: f.store, Revalidator: confirming(f.store), Exposure: gatewaytest.OneHostExposure, Activity: f.activity,
		Resolve: func(_ context.Context, app *model.App, _ *model.Template) (Backend, error) {
			resolved = append(resolved, app.Owner)
			u, err := url.Parse(f.backend.URL)
			return Backend{URL: u}, err
		}})
	for _, id := range []string{"alice-aaaaaaaa", "bob-bbbbbbbb"} {
		who := strings.SplitN(id, "-", 2)[0]
		r := httptest.NewRequest("GET", "/u/"+id+"/nb1/", nil)
		r.AddCookie(f.login(t, id, who))
		if w := f.do(r); w.Code != 200 {
			t.Fatalf("%s: got %d %q", who, w.Code, w.Body)
		}
	}
	if len(resolved) != 2 || resolved[0] != "alice-aaaaaaaa" || resolved[1] != "bob-bbbbbbbb" {
		t.Fatalf("resolved %v", resolved)
	}
}

func TestNotFoundPageUsesCatalogText(t *testing.T) {
	t.Parallel()
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("backend reached") }))
	r := httptest.NewRequest("GET", "/u/"+f.aliceSl+"/nope/", nil)
	r.AddCookie(f.login(t, "alice-aaaaaaaa", "alice"))
	w := f.do(r)
	if w.Code != 404 || !strings.Contains(w.Body.String(), html.EscapeString(reasons.Msg(reasons.MsgAppNotFound))) {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}
}

func TestSlugMustMatchOwnerEvenForOwner(t *testing.T) {
	t.Parallel()
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("backend reached") }))
	r := httptest.NewRequest("GET", "/u/someone-else/nb1/", nil)
	r.AddCookie(f.login(t, "alice-aaaaaaaa", "alice"))
	if w := f.do(r); w.Code != 404 {
		t.Fatalf("got %d", w.Code)
	}
}

func TestNoSessionRedirectsToLogin(t *testing.T) {
	t.Parallel()
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("backend reached") }))
	p := "/u/" + f.aliceSl + "/nb1/lab?a=b&c=d"
	for _, r := range []*http.Request{
		httptest.NewRequest("GET", p, nil),
		func() *http.Request {
			r := httptest.NewRequest("GET", p, nil)
			r.AddCookie(&http.Cookie{Name: model.CookieName(model.SessionCookieName, true), Value: "bogus"})
			return r
		}(),
	} {
		w := f.do(r)
		want := "/login?next=" + url.QueryEscape(p)
		if w.Code != 302 || w.Header().Get("Location") != want {
			t.Fatalf("got %d %q want %q", w.Code, w.Header().Get("Location"), want)
		}
	}
}

func TestBackendDownBrandedBadGateway(t *testing.T) {
	t.Parallel()
	f := setup(t, http.NotFoundHandler())
	f.backend.Close()
	r := httptest.NewRequest("GET", "/u/"+f.aliceSl+"/nb1/", nil)
	r.AddCookie(f.login(t, "alice-aaaaaaaa", "alice"))
	w := f.do(r)
	body := w.Body.String()
	r0, _ := reasons.Lookup(reasons.AppUnreachable)
	if w.Code != 502 || !strings.Contains(body, r0.Title) || !strings.Contains(body, html.EscapeString(r0.What)) || !strings.Contains(body, html.EscapeString(r0.Todo)) || !strings.Contains(body, "Fira Sans") || !strings.Contains(body, `href="/apps/nb1"`) {
		t.Fatalf("got %d %q", w.Code, body)
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("content type %q", w.Header().Get("Content-Type"))
	}
}

// An app can be Ready (the kubelet's probe passed) a moment before the
// network policy lets the gateway in. The proxy keeps dialing for a short
// while instead of answering 502 at once.
func TestProxyWaitsForAppThatIsNotReachableYet(t *testing.T) {
	t.Parallel()
	f := setup(t, http.NotFoundHandler())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	go func() {
		time.Sleep(700 * time.Millisecond)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			t.Error(err)
			return
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "late app") })}
		t.Cleanup(func() { srv.Close() })
		_ = srv.Serve(l)
	}()
	p := New(Config{Store: f.db, Sessions: f.store, Revalidator: confirming(f.store), Exposure: gatewaytest.OneHostExposure, Activity: f.activity, DialWait: 5 * time.Second,
		Resolve: func(context.Context, *model.App, *model.Template) (Backend, error) {
			return Backend{URL: &url.URL{Scheme: "http", Host: addr}}, nil
		}})
	r := httptest.NewRequest("GET", "/u/"+f.aliceSl+"/nb1/", nil)
	r.AddCookie(f.login(t, "alice-aaaaaaaa", "alice"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "late app" {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}
}

func (f *fixture) appID(t *testing.T, owner, name string) string {
	t.Helper()
	app, err := f.db.Apps().GetByOwnerName(context.Background(), owner, name)
	if err != nil {
		t.Fatal(err)
	}
	return app.ID
}

func TestRequestRecordsActivity(t *testing.T) {
	t.Parallel()
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest("GET", "/u/"+f.aliceSl+"/nb1/", nil)
	r.AddCookie(f.login(t, "alice-aaaaaaaa", "alice"))
	if w := f.do(r); w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if _, ok := gatewaytest.Flushed(t, f.activity)[f.appID(t, f.aliceSl, "nb1")]; !ok {
		t.Fatal("a request recorded no activity")
	}
}

// A refused request is not use: someone without access cannot keep an app alive.
func TestRefusedRequestRecordsNoActivity(t *testing.T) {
	t.Parallel()
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest("GET", "/u/"+f.aliceSl+"/nb1/", nil)
	r.AddCookie(f.login(t, "bob-bbbbbbbb", "bob"))
	if w := f.do(r); w.Code != 404 {
		t.Fatalf("status %d", w.Code)
	}
	if got := gatewaytest.Flushed(t, f.activity); len(got) != 0 {
		t.Fatalf("recorded %v", got)
	}
}

// An open websocket keeps the app active at every flush, with no frames
// sent, until it closes.
func TestOpenWebsocketKeepsTheAppActive(t *testing.T) {
	t.Parallel()
	f := setup(t, http.HandlerFunc(gatewaytest.EchoBackend))
	srv := httptest.NewServer(f.proxy)
	defer srv.Close()
	cookie := f.login(t, "alice-aaaaaaaa", "alice")
	id := f.appID(t, f.aliceSl, "nb1")

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "GET /u/"+f.aliceSl+"/nb1/ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nCookie: "+cookie.String()+"\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	first := gatewaytest.Flushed(t, f.activity)[id]
	time.Sleep(20 * time.Millisecond)
	second := gatewaytest.Flushed(t, f.activity)[id]
	if first.IsZero() || !second.After(first) {
		t.Fatalf("open websocket flushed %v then %v, want both, the second later", first, second)
	}
	io.WriteString(conn, "ping")
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo: %q %v", buf, err)
	}
	conn.Close()
	// The proxy closes its side once the client is gone.
	deadline := time.Now().Add(5 * time.Second)
	for {
		gatewaytest.Flushed(t, f.activity)
		if _, still := gatewaytest.Flushed(t, f.activity)[id]; !still {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a closed websocket still counts as use")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBackendCannotTouchGatewaySessionOrOrigin(t *testing.T) {
	t.Parallel()
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Add("Set-Cookie", "javo_session=evil; Path=/")
		h.Add("Set-Cookie", "javo_session=evil2; Path=/api")
		h.Add("Set-Cookie", "app_pref=ok; Path=/")
		h.Set("Service-Worker-Allowed", "/")
		h.Set("Clear-Site-Data", `"cookies"`)
	}))
	r := httptest.NewRequest("GET", "/u/"+f.aliceSl+"/nb1/", nil)
	r.AddCookie(f.login(t, "alice-aaaaaaaa", "alice"))
	w := f.do(r)
	var names []string
	for _, c := range w.Result().Cookies() {
		names = append(names, c.Name)
	}
	if len(names) != 1 || names[0] != "app_pref" {
		t.Fatalf("cookies reaching browser: %v", names)
	}
	if w.Header().Get("Service-Worker-Allowed") != "" || w.Header().Get("Clear-Site-Data") != "" {
		t.Fatalf("dangerous headers kept: %v", w.Header())
	}
}

// Over TLS the gateway's cookies carry the __Host- prefix; an app on the UI's
// host must not be able to set those either.
func TestBackendCannotSetTheGatewaysHostCookies(t *testing.T) {
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Add("Set-Cookie", "__Host-javo_session=evil; Path=/; Secure")
		h.Add("Set-Cookie", "__Host-javo_app=evil; Path=/; Secure")
		h.Add("Set-Cookie", "__Host-app_pref=ok; Path=/; Secure")
	}))
	r := httptest.NewRequest("GET", "/u/"+f.aliceSl+"/nb1/", nil)
	r.AddCookie(f.login(t, "alice-aaaaaaaa", "alice"))
	w := f.do(r)
	var names []string
	for _, c := range w.Result().Cookies() {
		names = append(names, c.Name)
	}
	if len(names) != 1 || names[0] != "__Host-app_pref" {
		t.Fatalf("cookies reaching browser: %v", names)
	}
}

func TestHostPreservedAndCookiesByteForByte(t *testing.T) {
	t.Parallel()
	var host, xfh, cookie string
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, xfh, cookie = r.Host, r.Header.Get("X-Forwarded-Host"), r.Header.Get("Cookie")
	}))
	r := httptest.NewRequest("GET", "/u/"+f.aliceSl+"/nb1/", nil)
	r.Host = "javo.example.com"
	r.Header.Set("Cookie", `a="x y"; __Host-javo_session=`+f.login(t, "alice-aaaaaaaa", "alice").Value+";  b=2;c=%41")
	f.do(r)
	if host != "javo.example.com" || xfh != "javo.example.com" {
		t.Fatalf("host %q xfh %q", host, xfh)
	}
	if cookie != `a="x y"; b=2; c=%41` && cookie != `a="x y";  b=2;c=%41` {
		t.Fatalf("cookie %q", cookie)
	}
}

func TestDefaultResolveUsesTemplatePort(t *testing.T) {
	t.Parallel()
	app := &model.App{Namespace: gatewaytest.Namespace, Name: "nb1", Owner: "alice-aaaaaaaa", Template: "t"}
	resolve := func(port int32) (Backend, error) {
		tpl := &model.Template{Name: "t", Spec: model.TemplateSpec{Port: port}}
		return defaultResolve(gatewaytest.OneHostExposure)(context.Background(), app, tpl)
	}
	b, err := resolve(8888)
	if err != nil || b.URL.String() != "http://alice-aaaaaaaa-nb1.javo-apps.svc:8888" || b.StripPrefix {
		t.Fatalf("got %+v %v", b, err)
	}
	if _, err := resolve(0); err == nil {
		t.Fatal("port 0 must be an error")
	}
	if _, err := defaultResolve(gatewaytest.OneHostExposure)(context.Background(), app, nil); err == nil {
		t.Fatal("a missing template must be an error")
	}
}

func TestResolveErrorRendersUnreachablePage(t *testing.T) {
	t.Parallel()
	f := setup(t, http.NotFoundHandler())
	p := New(Config{Store: f.db, Sessions: f.store, Revalidator: confirming(f.store), Exposure: gatewaytest.OneHostExposure, Activity: f.activity,
		Resolve: func(context.Context, *model.App, *model.Template) (Backend, error) { return Backend{}, io.EOF }})
	r := httptest.NewRequest("GET", "/u/"+f.aliceSl+"/nb1/", nil)
	r.AddCookie(f.login(t, "alice-aaaaaaaa", "alice"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	r0, _ := reasons.Lookup(reasons.AppUnreachable)
	if w.Code != 502 || !strings.Contains(w.Body.String(), r0.Title) {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}

// templateProxy serves the fixture's workspace nb1 through the default
// resolver for template spec, with the backend swapped for the test server
// (the resolver's service address does not exist outside a cluster).
func templateProxy(t *testing.T, f *fixture, spec model.TemplateSpec) http.Handler {
	t.Helper()
	tpl := &model.Template{Name: "t", Spec: spec}
	u, _ := url.Parse(f.backend.URL)
	return New(Config{Store: f.db, Sessions: f.store, Revalidator: confirming(f.store), Exposure: gatewaytest.OneHostExposure, Activity: f.activity,
		Resolve: func(ctx context.Context, app *model.App, _ *model.Template) (Backend, error) {
			b, err := defaultResolve(gatewaytest.OneHostExposure)(ctx, app, tpl)
			b.URL = u
			return b, err
		}})
}

type seen struct {
	path, rawPath, query string
	header               http.Header
}

func recordingBackend(into *seen) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*into = seen{path: r.URL.Path, rawPath: r.URL.RawPath, query: r.URL.RawQuery, header: r.Header.Clone()}
		io.WriteString(w, "ok")
	})
}

func (f *fixture) get(t *testing.T, p http.Handler, path string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.AddCookie(f.login(t, "alice-aaaaaaaa", "alice"))
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}

func TestStripModeRemovesPrefixAndSendsForwardedPrefix(t *testing.T) {
	t.Parallel()
	var got seen
	f := setup(t, recordingBackend(&got))
	p := templateProxy(t, f, model.TemplateSpec{Port: 8000, BasePath: "Strip"})
	prefix := "/u/" + f.aliceSl + "/nb1"

	if w := f.get(t, p, prefix+"/static/x.js?v=2"); w.Code != 200 {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if got.path != "/static/x.js" || got.query != "v=2" {
		t.Fatalf("backend saw %q ? %q, want /static/x.js ? v=2", got.path, got.query)
	}
	if v := got.header.Values("X-Forwarded-Prefix"); len(v) != 1 || v[0] != prefix {
		t.Fatalf("X-Forwarded-Prefix %q, want [%s]", v, prefix)
	}

	f.get(t, p, prefix) // the bare app root, no trailing slash
	if got.path != "/" {
		t.Fatalf("bare prefix reached the app as %q, want /", got.path)
	}

	f.get(t, p, prefix+"/files/a%2Fb")
	if got.path != "/files/a/b" || got.rawPath != "/files/a%2Fb" {
		t.Fatalf("escaped path became %q / %q", got.path, got.rawPath)
	}
}

func TestStripModeReplacesClientForwardedPrefix(t *testing.T) {
	t.Parallel()
	var got seen
	f := setup(t, recordingBackend(&got))
	p := templateProxy(t, f, model.TemplateSpec{Port: 8000, BasePath: "Strip"})
	f.get(t, p, "/u/"+f.aliceSl+"/nb1/", "X-Forwarded-Prefix", "/evil")
	if v := got.header.Values("X-Forwarded-Prefix"); len(v) != 1 || v[0] != "/u/"+f.aliceSl+"/nb1" {
		t.Fatalf("X-Forwarded-Prefix %q", v)
	}
}

func TestPrefixModeKeepsPathAndDropsClientForwardedPrefix(t *testing.T) {
	t.Parallel()
	var got seen
	f := setup(t, recordingBackend(&got))
	p := templateProxy(t, f, model.TemplateSpec{Port: 8000, BasePath: "Prefix"})
	f.get(t, p, "/u/"+f.aliceSl+"/nb1/lab", "X-Forwarded-Prefix", "/evil")
	if got.path != "/u/"+f.aliceSl+"/nb1/lab" {
		t.Fatalf("backend saw %q", got.path)
	}
	if v := got.header.Values("X-Forwarded-Prefix"); len(v) != 0 {
		t.Fatalf("client X-Forwarded-Prefix reached the app: %q", v)
	}
}

func TestProxyHeadersAreRenderedWithBaseURL(t *testing.T) {
	t.Parallel()
	var got seen
	f := setup(t, recordingBackend(&got))
	p := templateProxy(t, f, model.TemplateSpec{Port: 8000, BasePath: "Strip",
		ProxyHeaders: map[string]string{"X-Script-Name": "{{.BaseURL}}", "X-Mode": "embedded"}})
	f.get(t, p, "/u/"+f.aliceSl+"/nb1/", "X-Script-Name", "/evil")
	if v := got.header.Values("X-Script-Name"); len(v) != 1 || v[0] != "/u/"+f.aliceSl+"/nb1/" {
		t.Fatalf("X-Script-Name %q", v)
	}
	if got.header.Get("X-Mode") != "embedded" {
		t.Fatalf("X-Mode %q", got.header.Get("X-Mode"))
	}
}

func TestBadProxyHeaderTemplateIsUnreachablePage(t *testing.T) {
	t.Parallel()
	f := setup(t, http.NotFoundHandler())
	p := templateProxy(t, f, model.TemplateSpec{Port: 8000, ProxyHeaders: map[string]string{"X-A": "{{.Nope}}"}})
	if w := f.get(t, p, "/u/"+f.aliceSl+"/nb1/"); w.Code != 502 {
		t.Fatalf("got %d, want 502", w.Code)
	}
}

func TestGatewayPagesLinkTheSiteIcon(t *testing.T) {
	t.Parallel()
	f := setup(t, http.NotFoundHandler())
	w := f.get(t, f.proxy, "/u/"+f.aliceSl+"/nope/")
	if w.Code != 404 {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	// The same icon links as ui/index.html.
	for _, link := range []string{
		`<link rel="icon" type="image/svg+xml" href="/favicon.svg">`,
		`<link rel="icon" href="/favicon.ico" sizes="32x32">`,
		`<link rel="icon" href="/favicon.png" type="image/png" sizes="180x180">`,
		`<link rel="apple-touch-icon" href="/favicon.png">`,
	} {
		if !strings.Contains(w.Body.String(), link) {
			t.Errorf("page lacks %s", link)
		}
	}
}

// Strip removes the base path the exposure gives the app: /u/<owner>/<app>
// in path mode (on a separate apps host too), nothing in subdomain mode,
// where the app already sits at the root of its own host.
func TestStripPrefixFollowsTheExposure(t *testing.T) {
	t.Parallel()
	tpl := &model.Template{Name: "t", Spec: model.TemplateSpec{
		Port: 8000, BasePath: "Strip", ProxyHeaders: map[string]string{"X-Root": "{{.BaseURL}}"}}}
	app := &model.App{Namespace: gatewaytest.Namespace, Name: "nb1", Owner: "alice-aaaaaaaa", Template: "t"}
	for _, tc := range []struct {
		name         string
		exp          exposure.Backend
		prefix, root string
	}{
		{"path on the UI host", gatewaytest.OneHostExposure, "/u/alice-aaaaaaaa/nb1", "/u/alice-aaaaaaaa/nb1/"},
		{"path on an apps host", exposurepath.New(config.URLs{Mode: "path", UIHost: "javo.example.com", AppsHost: "apps.example.com"}), "/u/alice-aaaaaaaa/nb1", "/u/alice-aaaaaaaa/nb1/"},
		{"subdomain", exposuresubdomain.New(config.URLs{Mode: "subdomain", UIHost: "javo.example.com", AppsDomain: "apps.example.com"}), "", "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := defaultResolve(tc.exp)(context.Background(), app, tpl)
			if err != nil || !b.StripPrefix || b.Prefix != tc.prefix || b.Headers.Get("X-Root") != tc.root {
				t.Fatalf("got %+v, %v; want prefix %q, X-Root %q", b, err, tc.prefix, tc.root)
			}
		})
	}
}

// An app that names its own port (a container image) is reached on it.
func TestDefaultResolveUsesTheAppsOwnPort(t *testing.T) {
	t.Parallel()
	tpl := &model.Template{Name: "t", Spec: model.TemplateSpec{Port: 8080, UserImage: true, BasePath: "Strip"}}
	app := &model.App{Namespace: gatewaytest.Namespace, Name: "web", Owner: "alice-aaaaaaaa", Template: "t", Image: "example.org/web:1", Port: 9000}
	b, err := defaultResolve(gatewaytest.OneHostExposure)(context.Background(), app, tpl)
	if err != nil || b.URL.String() != "http://alice-aaaaaaaa-web.javo-apps.svc:9000" || !b.StripPrefix {
		t.Fatalf("got %+v %v", b, err)
	}
}

// Behind a TLS proxy the gateway's own hop is plain http, but apps build
// absolute links and redirects from these headers: they get the public
// scheme, and no port a client or proxy in front claimed.
func TestAppsSeeThePublicSchemeAndNoForwardedPort(t *testing.T) {
	t.Parallel()
	var got seen
	f := setup(t, recordingBackend(&got))
	p := templateProxy(t, f, model.TemplateSpec{Port: 8000, BasePath: "Strip"})
	f.get(t, p, "/u/"+f.aliceSl+"/nb1/", "X-Forwarded-Proto", "http", "X-Forwarded-Port", "443")
	if v := got.header.Values("X-Forwarded-Proto"); len(v) != 1 || v[0] != "https" {
		t.Fatalf("X-Forwarded-Proto %q, want [https]", v)
	}
	if v := got.header.Values("X-Forwarded-Port"); len(v) != 0 {
		t.Fatalf("X-Forwarded-Port reached the app: %q", v)
	}
}

// Opening the app through the proxy is the owner's use that keeps the
// token: the token of the session's ID token ends up stored for the app.
func TestTheOwnerOpeningTheAppStoresTheirNebiToken(t *testing.T) {
	t.Parallel()
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "lab") }))
	opener := gatewaytest.Keyset(t, f.db)
	tpl := gatewaytest.Template("t", "jupyterlab")
	tpl.Spec.NebiToken = true
	gatewaytest.Seed(t, f.db, []any{tpl})
	src := &gatewaytest.NebiSource{Now: time.Now}
	u, _ := url.Parse(f.backend.URL)
	p := New(Config{Store: f.db, Sessions: f.store, Revalidator: confirming(f.store), Exposure: gatewaytest.OneHostExposure, Activity: f.activity,
		NebiTokens: &nebi.Tokens{Store: f.db, Source: src},
		Resolve:    func(context.Context, *model.App, *model.Template) (Backend, error) { return Backend{URL: u}, nil }})
	tok, err := f.store.Create(context.Background(), session.Session{Principal: authz.Principal{ID: f.aliceSl, Username: "alice"},
		Provider: "oidc", Kind: session.KindUI, IDToken: "id-alice"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/u/"+f.aliceSl+"/nb1/lab", nil)
	r.AddCookie(&http.Cookie{Name: model.CookieName(model.SessionCookieName, true), Value: tok})
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	id := f.appID(t, f.aliceSl, "nb1")
	deadline := time.Now().Add(5 * time.Second)
	for {
		sealed, _, err := f.db.NebiTokens().Get(context.Background(), id)
		if err == nil {
			got, err := opener.Open(sealed, secrets.NebiTokenAD(id, f.aliceSl))
			if err != nil || string(got) != "nebi-1" {
				t.Fatalf("stored %q, %v", got, err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no Nebi token stored after the owner opened the app")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

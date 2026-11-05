package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/activity"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/features/exposurepath"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

const (
	rootAlice = "alice-aaaaaaaa"
	rootBob   = "bob-bbbbbbbb"
	oneHost   = "javo.example.com"
	appsHost  = "apps.example.com"
)

// rootFixture serves alice's apps web and web2 (their template strips the
// prefix), lab (its template keeps it) and bob's private app secret, through
// the default resolver with the backend swapped for a test server that says
// which app and path it was asked for.
type rootFixture struct {
	t     *testing.T
	p     http.Handler
	host  string
	exp   exposure.Backend
	store *session.DBStore
	hits  chan string
}

func newRoot(t *testing.T, exp exposure.Backend, host string) *rootFixture {
	t.Helper()
	db := storetest.New(t)
	strip := gatewaytest.Template("strip", "streamlit")
	strip.Spec.BasePath, strip.Spec.Port = "Strip", 8000
	keep := gatewaytest.Template("keep", "streamlit")
	keep.Spec.BasePath, keep.Spec.Port = "Prefix", 8000
	app := func(owner, name, tpl string) *model.App {
		a := gatewaytest.App(owner, name)
		a.Template = tpl
		return a
	}
	gatewaytest.Seed(t, db, []any{strip, keep, app(rootAlice, "web", "strip"), app(rootAlice, "web2", "strip"),
		app(rootAlice, "lab", "keep"), app(rootBob, "secret", "strip")})
	f := &rootFixture{t: t, host: host, exp: exp, store: &session.DBStore{Sessions: db.Sessions()}, hits: make(chan string, 16)}
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit := r.Header.Get("X-App") + " " + r.URL.Path
		if c := r.Header.Get("Cookie"); strings.Contains(strings.ToLower(c), "javo_") {
			hit += " leaked " + c
		}
		f.hits <- hit
		// An app that keeps the prefix is asked for it too: name the last
		// segment only.
		switch "/" + strings.TrimPrefix(path.Base(r.URL.Path), "/") {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, `<!doctype html><script src="/assets/app.js"></script>`)
		case "/go":
			http.Redirect(w, r, "/login", http.StatusFound)
		case "/abs":
			http.Redirect(w, r, "https://"+r.Host+"/login?x=1", http.StatusFound)
		case "/already":
			http.Redirect(w, r, r.Header.Get("X-Forwarded-Prefix")+"/x", http.StatusFound)
		case "/elsewhere":
			http.Redirect(w, r, "https://other.example.org/login", http.StatusFound)
		case "/relative":
			w.Header().Set("Location", "login")
			w.WriteHeader(http.StatusFound)
		case "/cookie":
			w.Header().Add("Set-Cookie", "sid=1; Path=/")
			w.Header().Add("Set-Cookie", "pref=2")
		default:
			io.WriteString(w, "ok")
		}
	}))
	t.Cleanup(be.Close)
	u, _ := url.Parse(be.URL)
	seal, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	f.p = New(Config{
		Store: db, Sessions: f.store, Revalidator: confirming(f.store), Exposure: exp, Seal: seal,
		DialWait: 200 * time.Millisecond, Activity: activity.NewTracker(nil),
		Resolve: func(ctx context.Context, a *model.App, tpl *model.Template) (Backend, error) {
			b, err := defaultResolve(exp)(ctx, a, tpl)
			b.URL = u
			if b.Headers == nil {
				b.Headers = http.Header{}
			}
			b.Headers.Set("X-App", a.Owner+"/"+a.Name)
			return b, err
		},
	})
	return f
}

// oneHostRoot is the fixture with apps under /u/ on the UI host.
func oneHostRoot(t *testing.T) *rootFixture {
	return newRoot(t, gatewaytest.OneHostExposure, oneHost)
}

// appsHostRoot is the fixture with apps under /u/ on an apps host.
func appsHostRoot(t *testing.T) *rootFixture {
	return newRoot(t, exposurepath.New(config.URLs{Mode: "path", UIHost: oneHost, AppsHost: appsHost}), appsHost)
}

// ui signs id in on the UI host and returns the UI session cookie.
func (f *rootFixture) ui(id string) *http.Cookie {
	f.t.Helper()
	tok, err := f.store.Create(context.Background(), session.Session{Principal: authz.Principal{ID: id, Username: id[:strings.Index(id, "-")]},
		Provider: "local", Kind: session.KindUI, RevalidateAt: time.Now().Add(time.Hour)})
	if err != nil {
		f.t.Fatal(err)
	}
	return &http.Cookie{Name: model.CookieName(model.SessionCookieName, true), Value: tok}
}

// appSession is the app cookie a redeemed ticket gives id for owner's app
// name on the apps host.
func (f *rootFixture) appSession(id, owner, name string) *http.Cookie {
	f.t.Helper()
	ui := f.ui(id)
	tok, err := f.store.Create(context.Background(), session.Session{Principal: authz.Principal{ID: id}, Provider: "local",
		Kind: session.KindApp, Parent: session.ID(ui.Value), Workspace: gatewaytest.Namespace + "/" + gatewaytest.App(owner, name).ObjectName()})
	if err != nil {
		f.t.Fatal(err)
	}
	tg, _ := f.exp.Match(httptest.NewRequest("GET", "https://"+f.host+"/u/"+owner+"/"+name+"/", nil))
	return &http.Cookie{Name: f.p.(*proxy).appCookie(tg).Name, Value: tok}
}

// page is the address of a page of owner's app name.
func (f *rootFixture) page(owner, name string) string {
	return "https://" + f.host + "/u/" + owner + "/" + name + "/"
}

// send makes a browser request for path on the fixture's host. hdr are
// header name and value pairs.
func (f *rootFixture) send(path string, cookies []*http.Cookie, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "https://"+f.host+path, nil)
	r.Host = f.host
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	f.p.ServeHTTP(w, r)
	return w
}

// navigate opens owner's app name as a top-level page and returns the
// cookies the answer sets, with the ones sent.
func (f *rootFixture) navigate(owner, name string, cookies ...*http.Cookie) []*http.Cookie {
	f.t.Helper()
	w := f.send("/u/"+owner+"/"+name+"/", cookies, "Sec-Fetch-Site", "none", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document")
	if w.Code != 200 {
		f.t.Fatalf("open %s/%s: %d %s", owner, name, w.Code, w.Body)
	}
	if hit := <-f.hits; hit != owner+"/"+name+" /" {
		f.t.Fatalf("open %s/%s reached %q", owner, name, hit)
	}
	return append(cookies, w.Result().Cookies()...)
}

// asset asks for path as a script of a page whose address is referer
// ("" sends none).
func (f *rootFixture) asset(path, referer string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	hdr := []string{"Sec-Fetch-Site", "same-origin", "Sec-Fetch-Mode", "no-cors", "Sec-Fetch-Dest", "script"}
	if referer != "" {
		hdr = append(hdr, "Referer", referer)
	}
	return f.send(path, cookies, hdr...)
}

// websocket opens path as a websocket handshake would ask for it: no Referer.
func (f *rootFixture) websocket(path string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	return f.send(path, cookies, "Sec-Fetch-Site", "same-origin", "Sec-Fetch-Mode", "websocket", "Sec-Fetch-Dest", "websocket",
		"Origin", "https://"+f.host)
}

// reached is the app and path the backend saw for the last request, "" when
// it saw none.
func (f *rootFixture) reached() string {
	select {
	case h := <-f.hits:
		return h
	default:
		return ""
	}
}

// A page of a stripped app that loads /assets/app.js gets the file from the
// app: the Referer names the app, and the app sees the path as it is.
func TestAnAbsoluteLinkOfAnAppPageReachesTheApp(t *testing.T) {
	t.Parallel()
	for _, f := range []*rootFixture{oneHostRoot(t), appsHostRoot(t)} {
		var cookies []*http.Cookie
		if f.host == oneHost {
			cookies = f.navigate(rootAlice, "web", f.ui(rootAlice))
		} else {
			cookies = f.navigate(rootAlice, "web", f.appSession(rootAlice, rootAlice, "web"))
			// The app cookie lives on the app's path: a browser does not send
			// it to /assets/.
			cookies = dropNamed(cookies, cookies[0].Name)
		}
		w := f.asset("/assets/app.js", f.page(rootAlice, "web")+"lab?x=1", cookies)
		if w.Code != 200 || w.Body.String() != "ok" {
			t.Fatalf("%s: %d %s", f.host, w.Code, w.Body)
		}
		if got := f.reached(); got != rootAlice+"/web /assets/app.js" {
			t.Fatalf("%s: backend saw %q", f.host, got)
		}
	}
}

// dropNamed is cookies without the ones called name.
func dropNamed(cookies []*http.Cookie, name string) []*http.Cookie {
	var out []*http.Cookie
	for _, c := range cookies {
		if c.Name != name {
			out = append(out, c)
		}
	}
	return out
}

// A request with no Referer (a websocket) goes to the app whose page the
// browser opened last, named by a sealed cookie the gateway set then. A
// cookie that was not sealed by the gateway names nothing.
func TestARootRequestWithoutARefererGoesToTheLastOpenedApp(t *testing.T) {
	t.Parallel()
	f := oneHostRoot(t)
	ui := f.ui(rootAlice)
	cookies := f.navigate(rootAlice, "web", ui)
	if w := f.websocket("/ws", cookies); w.Code != 200 {
		t.Fatalf("websocket: %d %s", w.Code, w.Body)
	}
	if got := f.reached(); got != rootAlice+"/web /ws" {
		t.Fatalf("backend saw %q", got)
	}
	if w := f.websocket("/ws", []*http.Cookie{ui}); w.Code != http.StatusNotFound || f.reached() != "" {
		t.Fatalf("no cookie: %d, reached the app", w.Code)
	}
	var forged []*http.Cookie
	for _, c := range cookies {
		if c != ui {
			c := *c
			c.Value = "web"
			forged = append(forged, &c)
		}
	}
	if len(forged) == 0 {
		t.Fatal("opening the app set no cookie")
	}
	if w := f.websocket("/ws", append(forged, ui)); w.Code != http.StatusNotFound || f.reached() != "" {
		t.Fatalf("forged cookie: %d, reached the app", w.Code)
	}
}

// The Referer decides over the cookie: a request from a page of web goes to
// web even after web2 was opened, and a request whose Referer is a page that
// is not an app's gets no app.
func TestTheRefererWinsOverTheCookie(t *testing.T) {
	t.Parallel()
	f := oneHostRoot(t)
	cookies := f.navigate(rootAlice, "web2", f.navigate(rootAlice, "web", f.ui(rootAlice))...)
	if w := f.asset("/assets/app.js", f.page(rootAlice, "web"), cookies); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := f.reached(); got != rootAlice+"/web /assets/app.js" {
		t.Fatalf("backend saw %q", got)
	}
	if w := f.asset("/assets/app.js", "https://"+oneHost+"/apps", cookies); w.Code != http.StatusNotFound || f.reached() != "" {
		t.Fatalf("a page of the web UI: %d, reached an app", w.Code)
	}
}

// A root request never reaches an app its sender could not open directly:
// not by naming that app's page as the Referer, nor with a cookie its owner's
// browser got.
func TestARootRequestNeverOpensAnAppTheVisitorCannotOpen(t *testing.T) {
	t.Parallel()
	f := oneHostRoot(t)
	alice := f.ui(rootAlice)
	if w := f.asset("/assets/app.js", f.page(rootBob, "secret"), []*http.Cookie{alice}); w.Code != http.StatusNotFound || f.reached() != "" {
		t.Fatalf("bob's Referer: %d, reached the app", w.Code)
	}
	bobs := dropNamed(f.navigate(rootBob, "secret", f.ui(rootBob)), alice.Name)
	if w := f.websocket("/ws", append(bobs, alice)); w.Code != http.StatusNotFound || f.reached() != "" {
		t.Fatalf("bob's cookie: %d, reached the app", w.Code)
	}
}

// A request from another site never uses the cookie, and a Referer of
// another host never names an app.
func TestACrossSiteRootRequestGetsNoApp(t *testing.T) {
	t.Parallel()
	f := oneHostRoot(t)
	cookies := f.navigate(rootAlice, "web", f.ui(rootAlice))
	if w := f.send("/ws", cookies, "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"); w.Code != http.StatusNotFound || f.reached() != "" {
		t.Fatalf("cross-site with the cookie: %d, reached the app", w.Code)
	}
	if w := f.send("/x", cookies, "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "no-cors", "Referer", "https://evil.example.org/u/"+rootAlice+"/web/"); w.Code != http.StatusNotFound || f.reached() != "" {
		t.Fatalf("another host's Referer: %d, reached the app", w.Code)
	}
}

// An app whose template does not strip the prefix writes its links with it,
// so a root request from its page is not the app's.
func TestAnAppThatKeepsThePrefixGetsNoRootRequests(t *testing.T) {
	t.Parallel()
	f := oneHostRoot(t)
	if w := f.asset("/assets/app.js", f.page(rootAlice, "lab"), []*http.Cookie{f.ui(rootAlice)}); w.Code != http.StatusNotFound || f.reached() != "" {
		t.Fatalf("%d, reached the app", w.Code)
	}
}

// The ticket address of an app is the gateway's: a root request for it is
// never redeemed as the app's.
func TestTheRootTicketPathIsNotAnApps(t *testing.T) {
	t.Parallel()
	f := appsHostRoot(t)
	cookies := dropNamed(f.navigate(rootAlice, "web", f.appSession(rootAlice, rootAlice, "web")), model.CookieName(model.AppCookieName, false))
	if w := f.asset("/"+ticketPath+"?t=x", f.page(rootAlice, "web"), cookies); w.Code != http.StatusNotFound || f.reached() != "" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// A redirect of a stripped app to one of its own absolute paths gets the
// prefix back; one already under the prefix, to another host or relative is
// left alone, and an app that keeps the prefix is left alone.
func TestARedirectOfAStrippedAppGetsThePrefix(t *testing.T) {
	t.Parallel()
	f := oneHostRoot(t)
	ui := f.ui(rootAlice)
	prefix := "/u/" + rootAlice + "/web"
	for path, want := range map[string]string{
		"/go":        prefix + "/login",
		"/abs":       "https://" + oneHost + prefix + "/login?x=1",
		"/already":   prefix + "/x",
		"/elsewhere": "https://other.example.org/login",
		"/relative":  "login",
	} {
		w := f.send(prefix+path, []*http.Cookie{ui})
		f.reached()
		if got := w.Header().Get("Location"); w.Code != http.StatusFound || got != want {
			t.Errorf("%s: %d Location %q, want %q", path, w.Code, got, want)
		}
	}
	// A root request is the app's too.
	w := f.asset("/go", f.page(rootAlice, "web"), []*http.Cookie{ui})
	f.reached()
	if got := w.Header().Get("Location"); got != prefix+"/login" {
		t.Errorf("root /go: Location %q", got)
	}
	w = f.send("/u/"+rootAlice+"/lab/go", []*http.Cookie{ui})
	f.reached()
	if got := w.Header().Get("Location"); got != "/login" {
		t.Errorf("an app that keeps the prefix: Location %q, want /login", got)
	}
}

// A cookie a stripped app sets for / from a root request still lives on the
// app's own path.
func TestACookieSetOnARootRequestStaysInTheApp(t *testing.T) {
	t.Parallel()
	f := oneHostRoot(t)
	w := f.asset("/cookie", f.page(rootAlice, "web"), []*http.Cookie{f.ui(rootAlice)})
	f.reached()
	want := "Path=/u/" + rootAlice + "/web"
	got := w.Header().Values("Set-Cookie")
	if len(got) != 2 || !strings.HasSuffix(got[0], want) || !strings.HasSuffix(got[1], want) {
		t.Fatalf("Set-Cookie %q, want each with %s", got, want)
	}
}

// Subdomain mode needs no fallback: a request for an app's host is the app's
// own already, so nothing here changes it.
func TestNoRootRequestsWhenAnAppHasItsOwnHost(t *testing.T) {
	t.Parallel()
	sub := New(Config{Exposure: subdomainLike{}, Activity: activity.NewTracker(nil)}).(*proxy)
	r := httptest.NewRequest("GET", "https://x.apps.example.com/assets/app.js", nil)
	r.Header.Set("Referer", "https://x.apps.example.com/u/"+rootAlice+"/web/")
	if _, ok := sub.rootTarget(r); ok {
		t.Fatal("subdomain mode took a root request")
	}
}

// subdomainLike is an exposure whose apps sit at the root of their own host.
type subdomainLike struct{ exposure.Backend }

func (subdomainLike) BasePath(string, string) string { return "/" }

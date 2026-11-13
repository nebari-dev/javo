package gateway_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/capacity/nodes"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/diagnose"
	"github.com/nebari-dev/javo/internal/gateway"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/clusterview/kubeview"
	"github.com/nebari-dev/javo/internal/gateway/proxy"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/secrets"
)

// stripServer replaces f's server with one that also serves a built UI from
// uiDir ("" for none) and whose apps have their prefix stripped (template
// basePath Strip), backed by a server that answers / with a page naming
// absolute paths and records every other path it is asked for.
func stripServer(t *testing.T, f *appsFixture, uiDir string) (hits *[]string) {
	t.Helper()
	var seen []string
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, `<!doctype html><script type="module" src="/assets/index.js"></script>`)
			return
		}
		io.WriteString(w, "app "+r.URL.Path)
	}))
	t.Cleanup(be.Close)
	target, _ := url.Parse(be.URL)
	seal, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	f.srv = gateway.NewServer(gateway.Config{Cluster: kubeview.Of(f.c), Store: f.db, Placement: apitest.SharedPlacement(), Sessions: f.store, Roles: appsRoles(),
		URLs: f.urls, Exposure: f.exp, Providers: nil, Revalidator: f.rv, Now: func() time.Time { return f.now }, UIDir: uiDir,
		Engines: apitest.TestEngines(), NetworkPolicy: &netprobe.Store{Facts: f.db.Facts()},
		Tokens: &token.DBStore{Tokens: f.db.Tokens()}, Machines: nodes.Source{Reader: f.c}, Diagnose: diagnose.NewEngine()})
	f.srv.Apps(proxy.New(proxy.Config{Store: f.db, Sessions: f.store, Exposure: f.exp, Roles: appsRoles(), Activity: f.activity,
		Revalidator: f.rv, URLs: f.urls, DialWait: 200 * time.Millisecond, Seal: seal,
		Resolve: func(_ context.Context, app *model.App, _ *model.Template) (proxy.Backend, error) {
			return proxy.Backend{URL: target, Prefix: strings.TrimSuffix(f.exp.BasePath(app.Owner, app.Name), "/"), StripPrefix: true}, nil
		}}))
	return &seen
}

// fetch asks srv for rawURL as a script of the page referer would ask, with
// the cookies jar holds for it.
func fetch(srv http.Handler, jar http.CookieJar, method, rawURL, referer, mode, dest string) *httptest.ResponseRecorder {
	u, _ := url.Parse(rawURL)
	r := httptest.NewRequest(method, rawURL, nil)
	r.Host = u.Host
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Sec-Fetch-Mode", mode)
	r.Header.Set("Sec-Fetch-Dest", dest)
	if referer != "" {
		r.Header.Set("Referer", referer)
	}
	for _, c := range jar.Cookies(u) {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	jar.SetCookies(u, w.Result().Cookies())
	return w
}

// A prebuilt single-page app whose index.html loads /assets/index.js works on
// an apps host: the person opens it from the web UI (ticket hop and all), and
// the browser's request for the script, which carries only the cookies an
// RFC 6265 jar sends to /assets/, reaches the app. So does its websocket,
// which carries no Referer.
func TestAnAppPageWithAbsolutePathsWorksOnAnAppsHost(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(nil))
	hits := stripServer(t, f, "")
	jar, _ := cookiejar.New(nil)
	ui, _ := url.Parse("https://" + uiHost + "/")
	jar.SetCookies(ui, []*http.Cookie{uiCookie(f.ui(alice()))})
	if w := f.browse(jar, salesURL); w.Code != 200 || !strings.Contains(w.Body.String(), `src="/assets/index.js"`) {
		t.Fatalf("open: %d %s", w.Code, w.Body)
	}
	w := fetch(f.srv, jar, "GET", "https://"+appsHost+"/assets/index.js", salesURL, "cors", "script")
	if w.Code != 200 || w.Body.String() != "app /assets/index.js" {
		t.Fatalf("script: %d %s", w.Code, w.Body)
	}
	w = fetch(f.srv, jar, "GET", "https://"+appsHost+"/ws", "", "websocket", "websocket")
	if w.Code != 200 || w.Body.String() != "app /ws" {
		t.Fatalf("websocket: %d %s", w.Code, w.Body)
	}
	if got := strings.Join(*hits, " "); got != "/ /assets/index.js /ws" {
		t.Fatalf("the app saw %q", got)
	}
}

// On one host a root request with an app's Referer reaches the app only for
// a path the web UI and the API do not serve, and never as a navigation (the
// web UI's own pages are routed in the browser, so the server cannot tell
// them apart).
func TestOneHostRootRequestsNeverShadowTheGatewaysRoutes(t *testing.T) {
	t.Parallel()
	f := newApps(t, config.URLs{Mode: "path"}, model.AccessRestricted, salesApp(nil))
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>javo</html>"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "assets"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "assets", "ui.js"), []byte("javo ui"), 0o644)
	hits := stripServer(t, f, dir)
	jar, _ := cookiejar.New(nil)
	host, _ := url.Parse("https://" + uiHost + "/")
	jar.SetCookies(host, []*http.Cookie{uiCookie(f.ui(alice()))})
	page := "https://" + uiHost + "/u/" + apitest.AliceID + "/sales-app/"
	if w := fetch(f.srv, jar, "GET", page, "", "navigate", "document"); w.Code != 200 {
		t.Fatalf("open: %d %s", w.Code, w.Body)
	}
	*hits = nil
	for _, c := range []struct{ method, path, mode, dest string }{
		{"GET", "/", "navigate", "document"},
		{"GET", "/about", "navigate", "document"},
		{"GET", "/apps", "navigate", "document"},
		{"GET", "/index.html", "no-cors", "script"},
		{"GET", "/assets/ui.js", "no-cors", "script"},
		{"GET", "/healthz", "cors", "empty"},
		{"POST", "/healthz", "cors", "empty"},
		{"GET", "/readyz", "cors", "empty"},
		{"GET", "/api/whoami", "cors", "empty"},
		{"GET", "/api", "cors", "empty"},
		{"GET", "/api/docs", "cors", "empty"},
		{"GET", "/u", "cors", "empty"},
	} {
		fetch(f.srv, jar, c.method, "https://"+uiHost+c.path, page, c.mode, c.dest)
		if len(*hits) != 0 {
			t.Fatalf("%s %s (%s) reached the app as %v", c.method, c.path, c.mode, *hits)
		}
	}
	w := fetch(f.srv, jar, "GET", "https://"+uiHost+"/assets/index.js", page, "no-cors", "script")
	if w.Code != 200 || w.Body.String() != "app /assets/index.js" {
		t.Fatalf("the app's own script: %d %s", w.Code, w.Body)
	}
	if h := w.Header().Get("X-Frame-Options"); h != "" {
		t.Fatalf("the app's answer got the gateway's headers: X-Frame-Options %q", h)
	}
	w = fetch(f.srv, jar, "GET", "https://"+uiHost+"/ws", "", "websocket", "websocket")
	if w.Code != 200 || w.Body.String() != "app /ws" {
		t.Fatalf("websocket: %d %s", w.Code, w.Body)
	}
}

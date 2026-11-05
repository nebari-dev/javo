package gateway_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nebari-dev/javo/internal/activity"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/core/registry"
	"github.com/nebari-dev/javo/internal/diagnose"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/features/exposurepath"
	"github.com/nebari-dev/javo/internal/features/exposuresubdomain"
	"github.com/nebari-dev/javo/internal/gateway"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/gateway/proxy"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func TestTicketRefusesTargetsThatAreNotOurAppUrls(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(nil))
	bobTok := f.ui(bob())
	for name, to := range map[string]string{
		"other host":           "https://evil.example.com/u/" + apitest.AliceID + "/sales-app/",
		"userinfo trick":       "//apps.example.com@evil.example.com/",
		"userinfo with scheme": "https://apps.example.com@evil.example.com/u/" + apitest.AliceID + "/sales-app/",
		"wrong scheme":         "http://" + appsHost + "/u/" + apitest.AliceID + "/sales-app/",
		"not an app path":      "https://" + appsHost + "/api/me",
		"app bob may not open": salesURL,
		"no such app":          "https://" + appsHost + "/u/" + apitest.AliceID + "/nope/",
		"empty":                "",
	} {
		w := f.ticketFor(bobTok, to)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "No app with that name") {
			t.Errorf("%s: %d %q", name, w.Code, w.Body)
		}
	}
	if n := f.tickets(); n != 0 {
		t.Fatalf("%d tickets minted for refused targets", n)
	}
}

func TestTicketNeedsASession(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(nil))
	w := f.ticketFor("", salesURL)
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/login?next=") {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Location"))
	}
}

// Over TLS only the __Host- session cookie mints tickets: a plain-named one
// may have been set for the parent domain by an app on a sibling host.
func TestTicketIgnoresAPlainNamedSessionCookie(t *testing.T) {
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(nil))
	plain := &http.Cookie{Name: model.SessionCookieName, Value: f.ui(alice())}
	w := f.serve(f.srv, "GET", "https://"+uiHost+"/api/auth/app-ticket?to="+url.QueryEscape(salesURL), plain)
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/login?next=") {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestTicketFlowGivesAnAppCookieAndTheApp(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(nil))
	aliceTok := f.ui(alice())
	to := salesURL + "lab?x=1"
	w := f.ticketFor(aliceTok, to)
	loc := w.Header().Get("Location")
	if w.Code != http.StatusFound || !strings.HasPrefix(loc, salesURL+"_javo/ticket?n="+testNonce+"&t=") {
		t.Fatalf("mint: %d %q", w.Code, loc)
	}
	if u, _ := url.Parse(loc); u.Query().Get("to") != "/u/"+apitest.AliceID+"/sales-app/lab?x=1" {
		t.Fatalf("to %q", u.Query().Get("to"))
	}

	w = f.redeem(loc)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/u/"+apitest.AliceID+"/sales-app/lab?x=1" {
		t.Fatalf("redeem: %d %q", w.Code, w.Header().Get("Location"))
	}
	var c *http.Cookie
	for _, x := range w.Result().Cookies() {
		if x.Name == model.AppCookieName {
			c = x
		}
	}
	if c == nil || c.Name != model.AppCookieName || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode ||
		c.Path != "/u/"+apitest.AliceID+"/sales-app" || c.Domain != "" {
		t.Fatalf("cookie %+v", c)
	}

	w = f.serve(f.srv, "GET", salesURL+"lab", c, &http.Cookie{Name: "app_pref", Value: "dark"})
	if w.Code != 200 || w.Body.String() != "hello app" {
		t.Fatalf("app: %d %q", w.Code, w.Body)
	}
	if got := f.seen[len(f.seen)-1].Header.Get("Cookie"); got != "app_pref=dark" {
		t.Fatalf("cookie header to app: %q", got)
	}
	// A ui cookie is not an app cookie: it opens nothing on the apps host.
	w = f.serve(f.srv, "GET", salesURL+"lab", uiCookie(aliceTok))
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "https://"+uiHost+"/api/auth/app-ticket?n=") {
		t.Fatalf("ui cookie on the apps host: %d %q", w.Code, w.Header().Get("Location"))
	}
}

// A ticket works only in the browser whose visit to the app asked for it:
// Mallory cannot mint a ticket for her own app and send Bob the link, so
// that Bob's browser holds an app cookie of hers (login CSRF into one app).
func TestATicketIsRedeemedOnlyByTheBrowserThatAskedForIt(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(nil))
	loc := f.ticketFor(f.ui(alice()), salesURL).Header().Get("Location")
	// Bob's browser follows the link with none of the cookies alice's had.
	if w := f.serve(f.srv, "GET", loc); w.Code != http.StatusGone || len(w.Result().Cookies()) != 0 {
		t.Fatalf("ticket redeemed by another browser: %d %v", w.Code, w.Result().Cookies())
	}
}

// Each ticket is a database row, so one session cannot mint them without
// bound.
func TestTicketsPerSessionAreRateLimited(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(nil))
	tok := f.ui(alice())
	for i := range proxy.TicketBurst {
		if w := f.ticketFor(tok, salesURL); w.Code != http.StatusFound {
			t.Fatalf("ticket %d: %d", i, w.Code)
		}
	}
	if w := f.ticketFor(tok, salesURL); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("ticket past the burst: %d %v", w.Code, w.Header())
	}
	if n := f.tickets(); n > proxy.TicketBurst {
		t.Fatalf("%d tickets minted", n)
	}
}

func TestTicketWorksOnceAndOnlyForSixtySeconds(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(nil))
	aliceTok := f.ui(alice())
	mint := func() string { return f.ticketFor(aliceTok, salesURL).Header().Get("Location") }
	expired := func(w *httptest.ResponseRecorder) bool {
		return w.Code == http.StatusGone && strings.Contains(w.Body.String(), reasons.Msg(reasons.MsgTicketExpired)) && len(w.Result().Cookies()) == 0
	}

	loc := mint()
	if w := f.redeem(loc); w.Code != http.StatusFound {
		t.Fatalf("first redeem: %d", w.Code)
	}
	if w := f.redeem(loc); !expired(w) {
		t.Fatalf("second redeem: %d %q", w.Code, w.Body)
	}

	loc = mint()
	f.now = f.now.Add(61 * time.Second)
	if w := f.redeem(loc); !expired(w) {
		t.Fatalf("redeem after 61s: %d %q", w.Code, w.Body)
	}
}

func TestTicketForOneAppIsRefusedAtAnother(t *testing.T) {
	t.Parallel()
	other := gatewaytest.App(apitest.AliceID, "other-app")
	other.Template = "streamlit"
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(nil), other)
	loc := f.ticketFor(f.ui(alice()), salesURL).Header().Get("Location")
	atOther := strings.Replace(loc, "/sales-app/", "/other-app/", 1)
	if w := f.redeem(atOther); w.Code != http.StatusGone || len(w.Result().Cookies()) != 0 {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
}

func TestRemovingTheGroupFromTheShareEndsAccessOnTheNextRequest(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(sharedWithDS()))
	bobTok := f.ui(bob())
	cookie := f.open(bobTok, salesURL)
	if w := f.serve(f.srv, "GET", salesURL, cookie); w.Code != 200 {
		t.Fatalf("before: %d", w.Code)
	}

	f.setSharing(&model.Sharing{Access: model.AccessRestricted, Groups: []string{"/org/other"}})
	if w := f.serve(f.srv, "GET", salesURL, cookie); w.Code != 404 {
		t.Fatalf("after the group was removed: %d", w.Code)
	}
}

func TestEndingTheUISessionSendsTheNextRequestToTheTicketFlow(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(sharedWithDS()))
	bobTok := f.ui(bob())
	cookie := f.open(bobTok, salesURL)
	if err := f.store.Delete(context.Background(), bobTok); err != nil {
		t.Fatal(err)
	}
	w := f.serve(f.srv, "GET", salesURL+"lab?a=b", cookie)
	want := "https://" + uiHost + "/api/auth/app-ticket?n="
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), want) ||
		!strings.Contains(w.Header().Get("Location"), "&to="+url.QueryEscape(salesURL+"lab?a=b")) {
		t.Fatalf("%d %q want %q", w.Code, w.Header().Get("Location"), want)
	}
	if len(f.seen) != 0 {
		t.Fatal("app reached after the session ended")
	}
}

func TestLosingTheGroupAtTheProviderReachesRunningApps(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(sharedWithDS()))
	cookie := f.open(f.ui(bob()), salesURL)
	if w := f.serve(f.srv, "GET", salesURL, cookie); w.Code != 200 || f.idp.calls != 0 {
		t.Fatalf("before it is due: %d, provider asked %d times", w.Code, f.idp.calls)
	}

	// Bob only uses the app tab: nothing but the app proxy ever sees his session.
	f.now = f.now.Add(2 * time.Hour) // past the parent's RevalidateAt
	f.idp.dropGroups = true
	if w := f.serve(f.srv, "GET", salesURL, cookie); w.Code != 404 || f.idp.calls != 1 {
		t.Fatalf("after the provider dropped the group: %d, provider asked %d times", w.Code, f.idp.calls)
	}
	// The result is stored on the parent: the next request does not ask again.
	f.serve(f.srv, "GET", salesURL, cookie)
	if f.idp.calls != 1 {
		t.Fatalf("provider asked %d times", f.idp.calls)
	}
}

func TestAPersonTheProviderRevokedLosesTheirAppSession(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(sharedWithDS()))
	bobTok := f.ui(bob())
	cookie := f.open(bobTok, salesURL)
	f.now = f.now.Add(2 * time.Hour)
	f.idp.revoked = true
	w := f.serve(f.srv, "GET", salesURL, cookie)
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "https://"+uiHost+"/api/auth/app-ticket?n=") {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Location"))
	}
	if len(f.seen) != 0 {
		t.Fatal("app reached after the provider revoked the person")
	}
	if _, err := f.store.Get(context.Background(), bobTok); err == nil {
		t.Fatal("the ui session survived a revocation")
	}
}

func TestTicketIsNotMintedForAPersonWhoLostTheGroupAtTheProvider(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(sharedWithDS()))
	bobTok := f.ui(bob())
	f.now = f.now.Add(2 * time.Hour)
	f.idp.dropGroups = true
	if w := f.ticketFor(bobTok, salesURL); w.Code != http.StatusNotFound || f.tickets() != 0 {
		t.Fatalf("%d, %d tickets", w.Code, f.tickets())
	}
	f.idp.revoked = true
	f.now = f.now.Add(2 * time.Hour)
	if w := f.ticketFor(bobTok, salesURL); w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/login?next=") {
		t.Fatalf("revoked: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestPublicAppIsProxiedToAnonymousWithoutCookies(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessPublic, salesApp(&model.Sharing{Access: model.AccessPublic}))
	w := f.serve(f.srv, "GET", salesURL, &http.Cookie{Name: model.AppCookieName, Value: "stale"}, &http.Cookie{Name: "app_pref", Value: "x"})
	if w.Code != 200 || w.Body.String() != "hello app" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	if got := f.seen[0].Header.Values("Cookie"); len(got) != 0 {
		t.Fatalf("cookies reached the app: %q", got)
	}

	// The template is edited down to Private: the same request is a 404, not a sign-in redirect.
	tpl, err := f.db.Templates().Get(context.Background(), "streamlit")
	if err != nil {
		t.Fatal(err)
	}
	tpl.Spec.MaxSharing = model.AccessPrivate
	if _, err := f.db.Templates().Put(context.Background(), gatewaytest.Actor, tpl, tpl.Version); err != nil {
		t.Fatal(err)
	}
	if w := f.serve(f.srv, "GET", salesURL); w.Code != 404 {
		t.Fatalf("after maxSharing Private: %d", w.Code)
	}
}

func TestPrivateAndMissingAppsLookTheSameToAnonymous(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(nil))
	private := f.serve(f.srv, "GET", salesURL)
	missing := f.serve(f.srv, "GET", "https://"+appsHost+"/u/"+apitest.AliceID+"/nope/")
	if private.Code != http.StatusFound || missing.Code != http.StatusFound ||
		!strings.HasPrefix(private.Header().Get("Location"), "https://"+uiHost+"/api/auth/app-ticket?n=") ||
		!strings.HasPrefix(missing.Header().Get("Location"), "https://"+uiHost+"/api/auth/app-ticket?n=") {
		t.Fatalf("%d %q / %d %q", private.Code, private.Header().Get("Location"), missing.Code, missing.Header().Get("Location"))
	}
}

func TestViewerWebsocketUpgradePassesAndRecordsActivity(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessSignedIn, salesApp(&model.Sharing{Access: model.AccessSignedIn}))
	cookie := f.open(f.ui(bob()), salesURL)
	srv := httptest.NewServer(f.srv)
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "GET /u/"+apitest.AliceID+"/sales-app/ws HTTP/1.1\r\nHost: "+appsHost+"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nCookie: "+cookie.String()+"\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	app, err := f.db.Apps().GetByObjectName(context.Background(), sales)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := gatewaytest.Flushed(t, f.activity)[app.ID]; !ok {
		t.Fatal("no activity recorded for the viewer's upgrade")
	}
	io.WriteString(conn, "ping")
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo %q %v", buf, err)
	}
}

func TestUpgradeIsRefusedOnceTheShareIsGone(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(sharedWithDS()))
	cookie := f.open(f.ui(bob()), salesURL)
	f.setSharing(nil)
	r := httptest.NewRequest("GET", salesURL+"ws", nil)
	r.Host = appsHost
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Connection", "Upgrade")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("got %d", w.Code)
	}
}

func TestSubdomainFlow(t *testing.T) {
	t.Parallel()
	f := newApps(t, subdomainURLs, model.AccessRestricted, salesApp(sharedWithDS()))
	base := "https://" + sales + "." + appsHost + "/"
	cookie := f.open(f.ui(bob()), base+"lab")
	if cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("cookie scope %q %q", cookie.Domain, cookie.Path)
	}
	w := f.serve(f.srv, "GET", base+"lab", cookie)
	if w.Code != 200 || f.seen[len(f.seen)-1].URL.Path != "/lab" {
		t.Fatalf("%d, app saw %v", w.Code, f.seen[len(f.seen)-1].URL)
	}
	// The cookie is for this app's host only: another app's host gets the sign-in flow.
	other := "https://other-app." + appsHost + "/"
	if w := f.serve(f.srv, "GET", other, cookie); w.Code != http.StatusFound {
		t.Fatalf("other host: %d", w.Code)
	}
}

// Each app's own host keeps its cookie under the __Host- prefix, so another
// app's host cannot set one for the shared apps domain in its place.
func TestSubdomainAppCookieIsHostOnly(t *testing.T) {
	f := newApps(t, subdomainURLs, model.AccessRestricted, salesApp(sharedWithDS()))
	base := "https://" + sales + "." + appsHost + "/"
	w := f.redeem(f.ticketFor(f.ui(bob()), base).Header().Get("Location"))
	var cs []*http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.MaxAge >= 0 { // the spent nonce is cleared beside it
			cs = append(cs, c)
		}
	}
	if len(cs) != 1 || cs[0].Name != "__Host-javo_app" || !cs[0].Secure || cs[0].Path != "/" || cs[0].Domain != "" {
		t.Fatalf("redeem: %d, cookies %v", w.Code, cs)
	}
	if w := f.serve(f.srv, "GET", base, cs[0]); w.Code != 200 {
		t.Fatalf("host cookie: %d", w.Code)
	}
	if w := f.serve(f.srv, "GET", base, &http.Cookie{Name: model.AppCookieName, Value: cs[0].Value}); w.Code != http.StatusFound {
		t.Fatalf("plain-named cookie: %d", w.Code)
	}
}

// In subdomain mode an app's websocket reaches it at the path the browser
// used, with the browser's Host and Origin. ttyd (the coding agent templates,
// --check-origin) refuses a websocket whose Origin does not match its Host.
func TestSubdomainWebsocketKeepsHostAndOrigin(t *testing.T) {
	t.Parallel()
	f := newApps(t, subdomainURLs, model.AccessRestricted, salesApp(sharedWithDS()))
	host := sales + "." + appsHost
	cookie := f.open(f.ui(bob()), "https://"+host+"/")
	srv := httptest.NewServer(f.srv)
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: "+host+"\r\nOrigin: https://"+host+"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: tty\r\nCookie: "+cookie.String()+"\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	got := f.seen[len(f.seen)-1]
	if got.URL.Path != "/ws" || got.Host != host || got.Header.Get("Origin") != "https://"+host || got.Header.Get("Sec-WebSocket-Protocol") != "tty" {
		t.Fatalf("app saw path %q, Host %q, Origin %q, protocol %q", got.URL.Path, got.Host, got.Header.Get("Origin"), got.Header.Get("Sec-WebSocket-Protocol"))
	}
}

func TestTheUIHostIsNotAnAppsHost(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(nil))
	if w := f.serve(f.srv, "GET", "https://"+uiHost+"/healthz"); w.Code != 200 {
		t.Fatalf("ui host healthz: %d", w.Code)
	}
	if w := f.serve(f.srv, "GET", "https://"+appsHost+"/api/auth/options"); w.Code == 200 {
		t.Fatal("the UI answered on the apps host")
	}
	if w := f.serve(f.srv, "GET", "https://"+uiHost+salesURL[len("https://"+appsHost):]); w.Code != 404 {
		t.Fatalf("an app path on the ui host: %d", w.Code)
	}
}

// Pods inside the cluster reach the API at the gateway's own Service: that
// host is the UI's for /api/, never an app's.
func TestTheInternalHostServesTheAPI(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(nil))
	if w := f.serve(f.srv, "GET", "http://javo-gateway.javo-system.svc/api/auth/options"); w.Code != 200 {
		t.Fatalf("internal host /api: %d %s", w.Code, w.Body)
	}
	if w := f.serve(f.srv, "GET", "http://"+uiHost+salesURL[len("https://"+appsHost):]); w.Code != 404 {
		t.Fatalf("an app path on the ui host: %d", w.Code)
	}
}

// A kubelet probe sends the pod's address as Host, not either configured host.
func TestHealthzAnswersWhateverHostTheProbeSends(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(nil))
	for _, host := range []string{"10.42.0.7:8080", "10.42.0.7", appsHost} {
		for _, path := range []string{"/healthz", "/readyz"} {
			if w := f.serve(f.srv, "GET", "http://"+host+path); w.Code != 200 {
				t.Errorf("%s with Host %q: %d", path, host, w.Code)
			}
		}
	}
	// Only the health check is open to other hosts: nothing else of the UI is.
	if w := f.serve(f.srv, "GET", "http://10.42.0.7:8080/api/auth/options"); w.Code == 200 {
		t.Fatal("the UI answered on an unknown host")
	}
}

func TestAnAppAddressWithoutItsTrailingSlashOpens(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(nil))
	jar, _ := cookiejar.New(nil)
	ui, _ := url.Parse("https://" + uiHost + "/")
	jar.SetCookies(ui, []*http.Cookie{uiCookie(f.ui(alice()))})

	bare := strings.TrimSuffix(salesURL, "/")
	if w := f.browse(jar, bare); w.Code != 200 || w.Body.String() != "hello app" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	if n := f.tickets(); n > 1 {
		t.Fatalf("%d tickets for one visit", n)
	}
	// The cookie must not reach a sibling app whose name only starts the same.
	if w := f.serve(f.srv, "GET", bare+"2", &http.Cookie{Name: model.AppCookieName, Value: "x"}); w.Code == 200 {
		t.Fatal("a sibling path opened")
	}
}

// In subdomain mode every app has an origin of its own, but all of them, and
// the UI host, are one site: a sibling app's page could send requests that
// carry the visitor's cookie for this app. Only top-level GET navigations
// from the same site reach the app.
func TestSubdomainModeRefusesRequestsFromSiblingApps(t *testing.T) {
	t.Parallel()
	f := newApps(t, subdomainURLs, model.AccessRestricted, salesApp(nil))
	appURL := "https://" + sales + "." + appsHost + "/"
	cookie := f.open(f.ui(alice()), appURL)
	cases := []struct {
		name, method, site, mode, dest string
		refused                        bool
	}{
		{"fetch from a sibling app", "GET", "same-site", "cors", "empty", true},
		{"image from a sibling app", "GET", "same-site", "no-cors", "image", true},
		{"iframe on a sibling app", "GET", "same-site", "navigate", "iframe", true},
		{"form posted from a sibling app", "POST", "same-site", "navigate", "document", true},
		{"websocket from a sibling app", "GET", "same-site", "websocket", "empty", true},
		{"link from a sibling app or the web UI", "GET", "same-site", "navigate", "document", false},
		{"fetch from the app's own page", "GET", "same-origin", "cors", "empty", false},
		{"post from the app's own page", "POST", "same-origin", "cors", "empty", false},
		{"no fetch metadata (curl, an API client)", "GET", "", "", "", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, appURL+"api/kernels", nil)
		r.Host = sales + "." + appsHost
		r.AddCookie(cookie)
		for k, v := range map[string]string{"Sec-Fetch-Site": c.site, "Sec-Fetch-Mode": c.mode, "Sec-Fetch-Dest": c.dest} {
			if v != "" {
				r.Header.Set(k, v)
			}
		}
		w := httptest.NewRecorder()
		f.srv.ServeHTTP(w, r)
		refused := w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), html.EscapeString(reasons.Msg(reasons.MsgOpenedFromAnotherApp)))
		if refused != c.refused || !refused && w.Code != 200 {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
}

func TestPlatformAdminOpeningAnotherPersonsAppIsRecorded(t *testing.T) {
	t.Parallel()
	f := newApps(t, subdomainURLs, model.AccessRestricted, salesApp(nil))
	salesURL := "https://" + sales + "." + appsHost + "/"
	admin := authz.Principal{ID: "carol-9a8b7c6d", Username: "carol", Groups: []string{"javo-admins"}}
	adminApp := f.open(f.ui(admin), salesURL)
	if w := f.serve(f.srv, "GET", salesURL, adminApp); w.Code != 200 {
		t.Fatalf("got %d", w.Code)
	}
	got := f.adminOpens()
	if len(got) != 1 || got[0].Actor != admin.ID || got[0].ObjectName != "sales-app" || got[0].ObjectOwner != apitest.AliceID ||
		got[0].ObjectNamespace != gatewaytest.Namespace || got[0].ObjectID == "" || !got[0].Allowed {
		t.Fatalf("audit rows %+v", got)
	}
	// A page load is many requests: the same admin opening the same app again
	// does not add a row each time.
	f.serve(f.srv, "GET", salesURL+"lab", adminApp)
	if n := len(f.adminOpens()); n != 1 {
		t.Fatalf("%d rows after a second request", n)
	}
	// The owner's own visit records nothing.
	f.serve(f.srv, "GET", salesURL, f.open(f.ui(alice()), salesURL))
	if n := len(f.adminOpens()); n != 1 {
		t.Fatalf("%d rows after the owner's visit", n)
	}
}

// previews returns a preview of the app parentID of pull request 7 from a
// fork and one of pull request 8 from the repository itself.
func previews(parentID string) []any {
	var out []any
	for _, pr := range []struct {
		name string
		n    int32
		fork bool
	}{{"sales-app-pr-7", 7, true}, {"sales-app-pr-8", 8, false}} {
		a := gatewaytest.App(apitest.AliceID, pr.name)
		a.Template, a.PullNumber, a.ParentID = "streamlit", pr.n, parentID
		a.Source = &model.Source{URL: "https://github.com/example-org/site", Ref: "abc123", Fork: pr.fork}
		out = append(out, a)
	}
	return out
}

// On one host an app's page runs in the UI's origin and can call the API as
// whoever views it, so the proxy does not show a platform admin someone
// else's app, nor anyone a preview of a pull request, whose code the owner
// has not necessarily read.
func TestOneHostRefusesAdminOpensAndPreviews(t *testing.T) {
	t.Parallel()
	f := newOneHost(t, salesApp(nil))
	gatewaytest.Seed(t, f.db, previews(f.getWorkspaceIn(apitest.AliceID, "sales-app").ID))
	admin := authz.Principal{ID: "carol-9a8b7c6d", Username: "carol", Groups: []string{"javo-admins"}}
	refused := func(who authz.Principal, path string, code reasons.MessageCode) {
		t.Helper()
		w := f.serve(f.proxy, "GET", path, uiCookie(f.ui(who)))
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), html.EscapeString(reasons.Msg(code))) {
			t.Fatalf("%s %s: %d %s", who.Username, path, w.Code, w.Body)
		}
	}
	refused(admin, "/u/"+apitest.AliceID+"/sales-app/", reasons.MsgAdminOpenNeedsAppsHost)
	if n := len(f.adminOpens()); n != 0 {
		t.Fatalf("%d admin opens recorded", n)
	}
	refused(alice(), "/u/"+apitest.AliceID+"/sales-app-pr-7/", reasons.MsgPreviewNeedsAppsHost)
	refused(alice(), "/u/"+apitest.AliceID+"/sales-app-pr-8/", reasons.MsgPreviewNeedsAppsHost)
	if w := f.serve(f.proxy, "GET", "/u/"+apitest.AliceID+"/sales-app/", uiCookie(f.ui(alice()))); w.Code != 200 {
		t.Fatalf("owner: %d", w.Code)
	}
}

// With an apps host in path mode every app shares one origin, so an app's
// page can script another app the visitor opens: the proxy applies the same
// refusals as on one host.
func TestPathModeWithAppsHostRefusesAdminOpensAndPreviews(t *testing.T) {
	t.Parallel()
	f := newApps(t, pathURLs, model.AccessRestricted, salesApp(nil))
	gatewaytest.Seed(t, f.db, previews(f.getWorkspaceIn(apitest.AliceID, "sales-app").ID))
	admin := authz.Principal{ID: "carol-9a8b7c6d", Username: "carol", Groups: []string{"javo-admins"}}
	refused := func(who authz.Principal, appURL string, code reasons.MessageCode) {
		t.Helper()
		w := f.serve(f.srv, "GET", appURL, f.open(f.ui(who), appURL))
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), html.EscapeString(reasons.Msg(code))) {
			t.Fatalf("%s %s: %d %s", who.Username, appURL, w.Code, w.Body)
		}
	}
	refused(admin, salesURL, reasons.MsgAdminOpenNeedsAppsHost)
	if n := len(f.adminOpens()); n != 0 {
		t.Fatalf("%d admin opens recorded", n)
	}
	refused(alice(), "https://"+appsHost+"/u/"+apitest.AliceID+"/sales-app-pr-7/", reasons.MsgPreviewNeedsAppsHost)
	refused(alice(), "https://"+appsHost+"/u/"+apitest.AliceID+"/sales-app-pr-8/", reasons.MsgPreviewNeedsAppsHost)
	if w := f.serve(f.srv, "GET", salesURL, f.open(f.ui(alice()), salesURL)); w.Code != 200 {
		t.Fatalf("owner: %d", w.Code)
	}
}

// Any admin role, not only platform admin, is power an app's page could use
// as its viewer: a template admin does not open someone else's app shared
// with them while apps share an origin, and still opens their own.
func TestOneHostRefusesTemplateAdminOpeningAnotherPersonsApp(t *testing.T) {
	t.Parallel()
	f := newOneHost(t, salesApp(sharedWithDS()))
	tplAdmin := func(p authz.Principal) authz.Principal {
		p.Groups = append(p.Groups, "javo-template-admins")
		return p
	}
	path := "/u/" + apitest.AliceID + "/sales-app/"
	w := f.serve(f.proxy, "GET", path, uiCookie(f.ui(tplAdmin(bob()))))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), html.EscapeString(reasons.Msg(reasons.MsgAdminOpenNeedsAppsHost))) {
		t.Fatalf("template admin: %d %s", w.Code, w.Body)
	}
	if w := f.serve(f.proxy, "GET", path, uiCookie(f.ui(tplAdmin(alice())))); w.Code != 200 {
		t.Fatalf("template admin owner: %d", w.Code)
	}
}

func TestOneHostIgnoresAPlainNamedSessionCookie(t *testing.T) {
	f := newOneHost(t, salesApp(nil))
	plain := &http.Cookie{Name: model.SessionCookieName, Value: f.ui(alice())}
	if w := f.serve(f.proxy, "GET", "/u/"+apitest.AliceID+"/sales-app/", plain); w.Code != http.StatusFound {
		t.Fatalf("plain-named cookie: %d", w.Code)
	}
}

func TestOneHostAppNeverSeesTheHostSessionCookie(t *testing.T) {
	f := newOneHost(t, salesApp(nil))
	w := f.serve(f.proxy, "GET", "/u/"+apitest.AliceID+"/sales-app/", uiCookie(f.ui(alice())), &http.Cookie{Name: "app_pref", Value: "dark"},
		&http.Cookie{Name: "__Host-javo_oidc_ABCDEFGH", Value: "flow"}, &http.Cookie{Name: "javo_oidc_ABCDEFGH", Value: "flow"})
	if w.Code != 200 {
		t.Fatalf("got %d", w.Code)
	}
	if got := f.seen[len(f.seen)-1].Header.Get("Cookie"); got != "app_pref=dark" {
		t.Fatalf("cookie header to app: %q", got)
	}
}

// On one host a shared app's page could act as its viewer, so shares open
// only when the admin set sharing.allowOneHost: a share made while the
// install had an apps host stops opening once apps move onto the UI host.
func TestOneHostSharesOpenOnlyWithAllowOneHost(t *testing.T) {
	t.Parallel()
	f := newOneHost(t, salesApp(sharedWithDS()))
	path := "/u/" + apitest.AliceID + "/sales-app/"
	w := f.serve(f.proxy, "GET", path, uiCookie(f.ui(bob())))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), html.EscapeString(reasons.Msg(reasons.MsgSharedNeedsAppsHost))) {
		t.Fatalf("shared viewer without allowOneHost: %d %s", w.Code, w.Body)
	}
	if w := f.serve(f.proxy, "GET", path, uiCookie(f.ui(alice()))); w.Code != 200 {
		t.Fatalf("owner: %d", w.Code)
	}
}

// In subdomain mode a platform admin opens someone else's plain app, but not
// one that runs code as its owner: no ticket is minted, and an app cookie
// from before the change no longer opens it.
func TestPlatformAdminDoesNotOpenAnotherPersonsCodeRunningApp(t *testing.T) {
	t.Parallel()
	f := newApps(t, subdomainURLs, model.AccessPrivate, salesApp(nil))
	salesURL := "https://" + sales + "." + appsHost + "/"
	admin := authz.Principal{ID: "carol-9a8b7c6d", Username: "carol", Groups: []string{"javo-admins"}}
	if w := f.ticketFor(f.ui(admin), salesURL); w.Code != http.StatusNotFound {
		t.Fatalf("ticket: %d %s", w.Code, w.Header())
	}
	if w := f.serve(f.srv, "GET", salesURL, f.open(f.ui(alice()), salesURL)); w.Code != 200 {
		t.Fatalf("owner: %d", w.Code)
	}
	if n := len(f.adminOpens()); n != 0 {
		t.Fatalf("%d admin opens recorded", n)
	}
}

// An admin who opened an app before its template was capped at Private is
// told why it no longer opens, on the next request.
func TestPlatformAdminLosesAnAppWhoseTemplateBecomesPrivate(t *testing.T) {
	t.Parallel()
	f := newApps(t, subdomainURLs, model.AccessRestricted, salesApp(nil))
	salesURL := "https://" + sales + "." + appsHost + "/"
	admin := authz.Principal{ID: "carol-9a8b7c6d", Username: "carol", Groups: []string{"javo-admins"}}
	adminApp := f.open(f.ui(admin), salesURL)
	if w := f.serve(f.srv, "GET", salesURL, adminApp); w.Code != 200 {
		t.Fatalf("before: %d", w.Code)
	}
	tpl, err := f.db.Templates().Get(context.Background(), "streamlit")
	if err != nil {
		t.Fatal(err)
	}
	tpl.Spec.MaxSharing = model.AccessPrivate
	if _, err := f.db.Templates().Put(context.Background(), gatewaytest.Actor, tpl, tpl.Version); err != nil {
		t.Fatal(err)
	}
	w := f.serve(f.srv, "GET", salesURL, adminApp)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), html.EscapeString(reasons.Msg(reasons.MsgOwnerOnlyOpens))) {
		t.Fatalf("after: %d %s", w.Code, w.Body)
	}
}

func TestOneHostViewerWithTheUISessionOpensASharedApp(t *testing.T) {
	t.Parallel()
	f := newOneHostSharing(t, salesApp(sharedWithDS()), true)
	if w := f.serve(f.proxy, "GET", "/u/"+apitest.AliceID+"/sales-app/", uiCookie(f.ui(bob()))); w.Code != 200 {
		t.Fatalf("shared viewer: %d", w.Code)
	}
	carol := authz.Principal{ID: "carol-9a8b7c6d", Username: "carol"}
	if w := f.serve(f.proxy, "GET", "/u/"+apitest.AliceID+"/sales-app/", uiCookie(f.ui(carol))); w.Code != 404 {
		t.Fatalf("not shared: %d", w.Code)
	}
}

const (
	uiHost   = "javo.example.com"
	appsHost = "apps.example.com"
	dsGroup  = "/org/ds"
)

var (
	pathURLs      = config.URLs{Mode: "path", UIHost: uiHost, AppsHost: appsHost, Scheme: "https"}
	subdomainURLs = config.URLs{Mode: "subdomain", UIHost: uiHost, AppsDomain: appsHost, Scheme: "https"}
	sales         = naming.ObjectName(apitest.AliceID, "sales-app")
)

// appsFixture is a gateway with apps on a separate host: a Server for the UI
// host and the proxy for the apps host, over one clock.
type appsFixture struct {
	t *testing.T
	// c is the fake cluster (tokens, the network policy result); db holds apps,
	// templates and audit rows.
	c       client.Client
	db      *store.Store
	store   *session.DBStore
	now     time.Time
	srv     *gateway.Server
	exp     exposure.Backend
	urls    config.URLs
	idp     *idpDouble
	backend *httptest.Server
	seen    []*http.Request
	// activity is where the proxy records use.
	activity *activity.Tracker
	// rv revalidates sessions against idp for the API and the proxy.
	rv *authn.Revalidator
}

func newApps(t *testing.T, urls config.URLs, maxSharing string, apps ...*model.App) *appsFixture {
	t.Helper()
	f := &appsFixture{t: t, now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), urls: urls}
	f.exp = exposurepath.New(urls)
	if urls.Mode == "subdomain" {
		f.exp = exposuresubdomain.New(urls)
	}
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	f.c = fake.NewClientBuilder().WithScheme(s).Build()
	f.db = storetest.New(t)
	tpl := &model.Template{Name: "streamlit",
		Spec: model.TemplateSpec{DisplayName: "Streamlit", Kind: "streamlit", Image: "example.org/streamlit:1", Port: 8501, MaxSharing: maxSharing}}
	gatewaytest.Seed(t, f.db, []any{tpl})
	for _, a := range apps {
		gatewaytest.Seed(t, f.db, []any{a})
	}
	f.store = &session.DBStore{Sessions: f.db.Sessions(), Now: func() time.Time { return f.now }}
	f.backend = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.seen = append(f.seen, r)
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			gatewaytest.EchoBackend(w, r)
			return
		}
		_, _ = io.WriteString(w, "hello app")
	}))
	t.Cleanup(f.backend.Close)
	target, _ := url.Parse(f.backend.URL)
	roles := appsRoles()
	f.idp = &idpDouble{now: func() time.Time { return f.now }}
	providers := registry.New[authprovider.Provider]()
	providers.Register(f.idp)
	rv := authn.NewRevalidator(providers, f.store, f.db.People(store.IDRange{}), 0, func() time.Time { return f.now })
	f.rv = rv
	f.srv = gateway.NewServer(gateway.Config{Client: f.c, Store: f.db, Placement: apitest.SharedPlacement(), Sessions: f.store, Roles: roles, URLs: urls, Exposure: f.exp, InternalHost: "javo-gateway.javo-system.svc",
		Providers: providers, Revalidator: rv, Now: func() time.Time { return f.now },
		Engines: apitest.TestEngines(), NetworkPolicy: &netprobe.Store{Facts: f.db.Facts()},
		Tokens: &token.DBStore{Tokens: f.db.Tokens()}, Machines: capacity.Nodes{Reader: f.c}, Diagnose: diagnose.NewEngine()})
	f.activity = activity.NewTracker(nil)
	f.srv.Apps(proxy.New(proxy.Config{Store: f.db, Sessions: f.store, Exposure: f.exp, Roles: roles, Activity: f.activity,
		Revalidator: rv, URLs: urls, DialWait: 200 * time.Millisecond,
		Resolve: func(context.Context, *model.App, *model.Template) (proxy.Backend, error) {
			return proxy.Backend{URL: target}, nil
		}}))
	return f
}

// idpDouble is the identity provider the sessions name ("local"). Its
// Revalidate keeps the person signed in for another hour, unless told to drop
// their groups or to say they are gone.
type idpDouble struct {
	now        func() time.Time
	dropGroups bool
	revoked    bool
	calls      int
}

func (d *idpDouble) ID() string { return "local" }

func (d *idpDouble) Option() authprovider.LoginOption { return authprovider.LoginOption{ID: "local"} }

func (d *idpDouble) Mount(authprovider.Mounter) {}

func (d *idpDouble) Revalidate(_ context.Context, s session.Session) (identity.Claims, session.Session, error) {
	d.calls++
	if d.revoked {
		return identity.Claims{}, s, authprovider.ErrRevoked
	}
	if d.dropGroups {
		s.Principal.Groups = nil
	}
	s.RevalidateAt = d.now().Add(time.Hour)
	return identity.Claims{}, s, nil
}

// salesApp is alice's app with the given sharing.
func salesApp(sharing *model.Sharing) *model.App {
	w := gatewaytest.App(apitest.AliceID, "sales-app")
	w.Template = "streamlit"
	if sharing != nil {
		w.Sharing = *sharing
	}
	return w
}

func (f *appsFixture) setSharing(sh *model.Sharing) {
	f.t.Helper()
	app := f.getWorkspaceIn(apitest.AliceID, "sales-app")
	_, err := f.db.Apps().UpdateSpec(context.Background(), gatewaytest.Actor, app.ID, app.Version, func(a *model.App) error {
		a.Sharing = model.Sharing{}
		if sh != nil {
			a.Sharing = *sh
		}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *appsFixture) getWorkspaceIn(owner, short string) model.App {
	f.t.Helper()
	app, err := f.db.Apps().GetByOwnerName(context.Background(), owner, short)
	if err != nil {
		f.t.Fatal(err)
	}
	return app
}

func (f *appsFixture) ui(p authz.Principal) string {
	f.t.Helper()
	tok, err := f.store.Create(context.Background(), session.Session{Principal: p, Provider: "local", Kind: session.KindUI,
		RevalidateAt: f.now.Add(time.Hour)})
	if err != nil {
		f.t.Fatal(err)
	}
	return tok
}

func bob() authz.Principal {
	return authz.Principal{ID: apitest.BobID, Username: "bob", Groups: []string{dsGroup}}
}

func alice() authz.Principal {
	return authz.Principal{ID: apitest.AliceID, Username: "alice"}
}

func (f *appsFixture) serve(h http.Handler, method, rawURL string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, rawURL, nil)
	u, _ := url.Parse(rawURL)
	if u.Host != "" {
		r.Host = u.Host
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// uiCookie is the session cookie of a gateway served over TLS.
func uiCookie(tok string) *http.Cookie {
	return &http.Cookie{Name: model.CookieName(model.SessionCookieName, true), Value: tok}
}

// testNonce stands for the nonce an app host sets before it sends the
// browser for a ticket.
var testNonce = base64.RawURLEncoding.EncodeToString(make([]byte, 32))

// ticketFor asks the UI host for a ticket to open appURL as the holder of
// uiTok, as the browser an app host gave testNonce does.
func (f *appsFixture) ticketFor(uiTok, appURL string) *httptest.ResponseRecorder {
	return f.serve(f.srv, "GET", "https://"+uiHost+"/api/auth/app-ticket?n="+testNonce+"&to="+url.QueryEscape(appURL), uiCookie(uiTok))
}

// redeem follows a ticket link in the browser that holds testNonce.
func (f *appsFixture) redeem(loc string) *httptest.ResponseRecorder {
	return f.serve(f.srv, "GET", loc, &http.Cookie{Name: "javo_ticket", Value: testNonce}, &http.Cookie{Name: "__Host-javo_ticket", Value: testNonce})
}

func (f *appsFixture) tickets() int {
	var l corev1.SecretList
	if err := f.c.List(context.Background(), &l, client.InNamespace("javo-system"), client.MatchingLabels{model.LabelSession: session.KindTicket}); err != nil {
		f.t.Fatal(err)
	}
	return len(l.Items)
}

// open mints a ticket for appURL, redeems it on the apps host and returns the
// app cookie it sets.
func (f *appsFixture) open(uiTok, appURL string) *http.Cookie {
	f.t.Helper()
	w := f.ticketFor(uiTok, appURL)
	if w.Code != http.StatusFound {
		f.t.Fatalf("ticket: %d %s", w.Code, w.Body)
	}
	w = f.redeem(w.Header().Get("Location"))
	for _, c := range w.Result().Cookies() {
		if c.Name == model.AppCookieName || c.Name == model.CookieName(model.AppCookieName, true) {
			return c
		}
	}
	f.t.Fatalf("redeem set no app cookie: %d %v", w.Code, w.Header())
	return nil
}

func sharedWithDS() *model.Sharing {
	return &model.Sharing{Access: model.AccessRestricted, Groups: []string{dsGroup}}
}

var salesURL = "https://" + appsHost + "/u/" + apitest.AliceID + "/sales-app/"

// browse follows redirects the way a browser does, with a cookie jar that
// applies RFC 6265 domain and path matching, for a link opened from the web
// UI: a top-level navigation from the same site, since the UI host and the
// apps share example.com. It returns the final response.
func (f *appsFixture) browse(jar http.CookieJar, rawURL string) *httptest.ResponseRecorder {
	f.t.Helper()
	for range 8 {
		u, _ := url.Parse(rawURL)
		r := httptest.NewRequest("GET", rawURL, nil)
		r.Host = u.Host
		r.Header.Set("Sec-Fetch-Site", "same-site")
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		r.Header.Set("Sec-Fetch-Dest", "document")
		for _, c := range jar.Cookies(u) {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		f.srv.ServeHTTP(w, r)
		jar.SetCookies(u, w.Result().Cookies())
		loc := w.Header().Get("Location")
		if w.Code != http.StatusFound {
			return w
		}
		Next, err := u.Parse(loc)
		if err != nil {
			f.t.Fatal(err)
		}
		rawURL = Next.String()
	}
	f.t.Fatalf("redirect loop: %d tickets minted", f.tickets())
	return nil
}

// adminOpens are the audit rows for platform admins opening apps.
func (f *appsFixture) adminOpens() []store.AuditEvent {
	f.t.Helper()
	all, err := f.db.Audit().List(context.Background(), 100)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []store.AuditEvent
	for _, e := range all {
		if e.Action == "app.admin_open" {
			out = append(out, e)
		}
	}
	return out
}

// oneHostFixture is the M1 shape: apps under /u/ on the UI host.
type oneHostFixture struct {
	*appsFixture
	proxy http.Handler
}

// appsRoles makes javo-admins platform admins, javo-template-admins
// template admins and everyone a member.
func appsRoles() config.Roles {
	var roles config.Roles
	roles.PlatformAdmin.Groups = []string{"javo-admins"}
	roles.TemplateAdmin.Groups = []string{"javo-template-admins"}
	return roles
}

func newOneHost(t *testing.T, app *model.App) *oneHostFixture {
	return newOneHostSharing(t, app, false)
}

// newOneHostSharing is newOneHost with sharing.allowOneHost set to allowOneHost.
func newOneHostSharing(t *testing.T, app *model.App, allowOneHost bool) *oneHostFixture {
	f := newApps(t, config.URLs{Mode: "path"}, model.AccessRestricted, app)
	target, _ := url.Parse(f.backend.URL)
	roles := appsRoles()
	return &oneHostFixture{appsFixture: f, proxy: proxy.New(proxy.Config{Store: f.db, Sessions: f.store,
		Exposure: exposurepath.New(config.URLs{}), Roles: roles, Activity: f.activity, Revalidator: f.rv,
		Sharing: config.SharingCfg{AllowOneHost: allowOneHost},
		Resolve: func(context.Context, *model.App, *model.Template) (proxy.Backend, error) {
			return proxy.Backend{URL: target}, nil
		}})}
}

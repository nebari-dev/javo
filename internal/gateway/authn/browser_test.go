package authn_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/features/authoidc"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestBrowserCallbackSignInEndsThePreviousSession(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Extra: []authprovider.Provider{&apitest.RedirectProvider{}}})
	old := f.LoginAlice()

	r := httptest.NewRequest(http.MethodGet, "/auth/redirect/callback", nil)
	r.AddCookie(old)
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("callback %d: %s", w.Code, w.Body)
	}
	if _, err := f.Store.Get(context.Background(), old.Value); err == nil {
		t.Fatal("the session this browser had before signing in as someone else is still alive")
	}
}

// The avatar URL the provider sends at sign-in is shown on /api/me.
func TestBrowserSignInKeepsTheProvidersPicture(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Extra: []authprovider.Provider{&apitest.RedirectProvider{}}})
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/redirect/callback", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("callback %d: %s", w.Code, w.Body)
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == model.SessionCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	me := f.Do("GET", "/api/me", "", cookie)
	if got := apitest.Decode[map[string]any](t, me)["picture"]; got != "https://example.com/bob.png" {
		t.Fatalf("picture %v in %s", got, me.Body)
	}
}

func TestLogoutPointsAtTheProvidersEndSessionURL(t *testing.T) {
	t.Parallel()
	const end = "https://idp.example.com/logout?client_id=javo"
	f := apitest.New(t, apitest.Options{Extra: []authprovider.Provider{&apitest.RedirectProvider{LogoutTo: end}}})
	tok, err := f.Store.Create(context.Background(), session.Session{
		Provider: "redirect", Kind: session.KindUI, RevalidateAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	w := f.Do("POST", "/api/auth/logout", "", &http.Cookie{Name: model.SessionCookieName, Value: tok})
	if w.Code != http.StatusNoContent || w.Header().Get(authn.LogoutURLHeader) != end {
		t.Fatalf("%d, %s = %q", w.Code, authn.LogoutURLHeader, w.Header().Get(authn.LogoutURLHeader))
	}
	if _, err := f.Store.Get(context.Background(), tok); err == nil {
		t.Fatal("session survived logout")
	}

	// Local accounts have nothing at a provider to sign out of: no headers.
	w = f.Do("POST", "/api/auth/logout", "", f.LoginAlice())
	if w.Header().Get(authn.LogoutURLHeader) != "" || w.Header().Get(authn.LogoutReasonHeader) != "" {
		t.Fatalf("local logout sent %q, %q", w.Header().Get(authn.LogoutURLHeader), w.Header().Get(authn.LogoutReasonHeader))
	}
}

func TestLogoutWithoutAnEndSessionEndpointSaysSignedOutLocallyOnly(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Extra: []authprovider.Provider{&apitest.RedirectProvider{}}})
	tok, err := f.Store.Create(context.Background(), session.Session{
		Provider: "redirect", Kind: session.KindUI, RevalidateAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	w := f.Do("POST", "/api/auth/logout", "", &http.Cookie{Name: model.SessionCookieName, Value: tok})
	if w.Code != http.StatusNoContent || w.Header().Get(authn.LogoutURLHeader) != "" ||
		w.Header().Get(authn.LogoutReasonHeader) != string(reasons.SignedOutLocallyOnly) {
		t.Fatalf("%d, url %q, reason %q", w.Code, w.Header().Get(authn.LogoutURLHeader), w.Header().Get(authn.LogoutReasonHeader))
	}
}

func TestParallelRevalidationsDoNotReuseTheRefreshToken(t *testing.T) {
	t.Parallel()
	rp := &apitest.RotatingProvider{Current: "refresh-1"}
	f := apitest.New(t, apitest.Options{Extra: []authprovider.Provider{rp}})
	tok, err := f.Store.Create(context.Background(), session.Session{
		Provider: "rotating", Kind: session.KindUI, Refresh: "refresh-1",
		RevalidateAt: time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Cookie{Name: model.SessionCookieName, Value: tok}
	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = f.Do("GET", "/api/me", "", c).Code
		}()
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("request %d answered %d: a valid session was ended", i, code)
		}
	}
	if rp.Calls != 1 {
		t.Fatalf("provider asked %d times, want 1", rp.Calls)
	}
	if _, err := f.Store.Get(context.Background(), tok); err != nil {
		t.Fatalf("session gone: %v", err)
	}
}

// With the identity provider down at startup the gateway still serves, lists
// the OIDC option as disabled with its reason, and signs local accounts in.
func TestGatewayServesLocalLoginWhileTheIdentityProviderIsDown(t *testing.T) {
	t.Parallel()
	oidc := authoidc.New(config.OIDC{
		Issuer: "http://idp.test:8080/auth/realms/javo", ClientID: "javo",
		Dial: map[string]string{"idp.test:8080": "127.0.0.1:1"}, // nothing listens there
	}, config.URLs{}, &http.Client{Timeout: 200 * time.Millisecond})
	t.Cleanup(oidc.Close)
	f := apitest.New(t, apitest.Options{Extra: []authprovider.Provider{oidc}})

	if w := f.Do("GET", "/healthz", "", nil); w.Code != http.StatusOK {
		t.Fatalf("healthz %d", w.Code)
	}
	var found *authprovider.LoginOption
	for _, o := range apitest.Decode[[]authprovider.LoginOption](t, f.Do("GET", "/api/auth/options", "", nil)) {
		if o.ID == "oidc" {
			found = &o
		}
	}
	if found == nil || !found.Disabled || found.Reason != "IdpUnreachable" {
		t.Fatalf("oidc option %+v", found)
	}
	if f.LoginAlice() == nil {
		t.Fatal("local login")
	}
}

// Requests queued behind one revalidation of a session whose provider hangs
// reuse its outcome instead of each waiting out the timeout again.
func TestParallelRevalidationsShareAnOutage(t *testing.T) {
	t.Parallel()
	hp := &apitest.HangingProvider{Hang: 300 * time.Millisecond}
	f := apitest.New(t, apitest.Options{Extra: []authprovider.Provider{hp}})
	tok, err := f.Store.Create(context.Background(), session.Session{
		Provider: "hanging", Kind: session.KindUI, Refresh: "refresh-1",
		RevalidateAt: time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Cookie{Name: model.SessionCookieName, Value: tok}
	const n = 8
	codes := make([]int, n)
	until := make([]string, n)
	start := time.Now()
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := f.Do("GET", "/api/me", "", c)
			codes[i], until[i] = w.Code, w.Header().Get(authn.IdpUnreachableHeader)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	for i := range n {
		if codes[i] != http.StatusOK || until[i] == "" {
			t.Fatalf("request %d answered %d with %s %q", i, codes[i], authn.IdpUnreachableHeader, until[i])
		}
	}
	if hp.Calls != 1 {
		t.Fatalf("provider asked %d times, want 1", hp.Calls)
	}
	if elapsed > 3*hp.Hang {
		t.Fatalf("requests took %v; each waited for the provider in turn", elapsed)
	}
}

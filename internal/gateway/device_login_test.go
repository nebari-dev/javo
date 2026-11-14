package gateway_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
)

// deviceFixture is a gateway at https://javo.example.com on a clock the test
// moves, with alice able to sign in.
func deviceFixture(t *testing.T, o apitest.Options) (*apitest.Fixture, *apitest.TestClock) {
	t.Helper()
	clk := &apitest.TestClock{T: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)}
	o.Now, o.PublicURL = clk.Now, "https://javo.example.com"
	return apitest.New(t, o), clk
}

type deviceStart struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURL         string `json:"verificationURL"`
	VerificationURLComplete string `json:"verificationURLComplete"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

// startDevice starts a device login as an agent would: no session.
func startDevice(t *testing.T, f *apitest.Fixture) deviceStart {
	t.Helper()
	w := f.Do("POST", "/api/auth/device", "", nil)
	wantCode(t, "start", w, http.StatusOK)
	return apitest.Decode[deviceStart](t, w)
}

// pollDevice asks for the token with the device code.
func pollDevice(f *apitest.Fixture, code string) *httptest.ResponseRecorder {
	f.T.Helper()
	return f.Do("POST", "/api/auth/device/token", `{"deviceCode":"`+code+`"}`, nil)
}

// wantDeviceError checks a poll answered 400 with the RFC 8628 error name.
func wantDeviceError(t *testing.T, w *httptest.ResponseRecorder, name string) {
	t.Helper()
	wantCode(t, name, w, http.StatusBadRequest)
	if got := apitest.Decode[struct {
		Error string `json:"error"`
	}](t, w).Error; got != name {
		t.Fatalf("error %q, want %q: %s", got, name, w.Body)
	}
}

func TestDeviceStartReturnsCodes(t *testing.T) {
	f, _ := deviceFixture(t, apitest.Options{})
	s := startDevice(t, f)
	if len(s.UserCode) != 9 || s.UserCode[4] != '-' {
		t.Fatalf("user code %q, want XXXX-XXXX", s.UserCode)
	}
	for i, c := range s.UserCode {
		if i != 4 && !strings.ContainsRune("BCDFGHJKLMNPQRSTVWXZ", c) {
			t.Fatalf("user code %q has %q", s.UserCode, c)
		}
	}
	if s.DeviceCode == "" {
		t.Fatal("no device code")
	}
	if s.VerificationURL != "https://javo.example.com/device" {
		t.Fatalf("verificationURL %q", s.VerificationURL)
	}
	if want := "https://javo.example.com/device?code=" + s.UserCode; s.VerificationURLComplete != want {
		t.Fatalf("verificationURLComplete %q, want %q", s.VerificationURLComplete, want)
	}
	if s.ExpiresIn != 600 || s.Interval != 5 {
		t.Fatalf("expiresIn %d interval %d", s.ExpiresIn, s.Interval)
	}
}

func TestDevicePollPending(t *testing.T) {
	f, clk := deviceFixture(t, apitest.Options{})
	s := startDevice(t, f)
	wantDeviceError(t, pollDevice(f, s.DeviceCode), "authorization_pending")
	clk.Add(4 * time.Second)
	wantDeviceError(t, pollDevice(f, s.DeviceCode), "slow_down")
	clk.Add(5 * time.Second)
	wantDeviceError(t, pollDevice(f, s.DeviceCode), "authorization_pending")
}

// approveDevice starts a device login and alice approves it.
func approveDevice(t *testing.T, f *apitest.Fixture, alice *http.Cookie) deviceStart {
	t.Helper()
	s := startDevice(t, f)
	wantCode(t, "approve", f.Do("POST", "/api/auth/device/"+s.UserCode+"/approve", "", alice), http.StatusNoContent)
	return s
}

type deviceToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func TestDeviceApproveGivesToken(t *testing.T) {
	f, clk := deviceFixture(t, apitest.Options{})
	alice := f.LoginAlice()
	s := approveDevice(t, f, alice)
	w := pollDevice(f, s.DeviceCode)
	wantCode(t, "poll", w, http.StatusOK)
	tok := apitest.Decode[deviceToken](t, w)
	want := clk.Now().Add(8 * time.Hour)
	if !tok.ExpiresAt.Equal(want) {
		t.Fatalf("expiresAt %v, want %v", tok.ExpiresAt, want)
	}
	me := f.Bearer("GET", "/api/me", "", tok.Token, nil)
	wantCode(t, "me", me, http.StatusOK)
	if got := apitest.Decode[struct {
		Username string `json:"username"`
	}](t, me).Username; got != "alice" {
		t.Fatalf("token acts as %q: %s", got, me.Body)
	}
	list := apitest.Decode[[]struct {
		Name      string     `json:"name"`
		Scope     string     `json:"scope"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}](t, f.Do("GET", "/api/tokens", "", alice))
	if len(list) != 1 || list[0].Name != "Agent login" || list[0].Scope != "read-write" || list[0].ExpiresAt == nil || !list[0].ExpiresAt.Equal(want) {
		t.Fatalf("alice's tokens: %+v", list)
	}
}

func TestDevicePollAfterSuccessIsSpent(t *testing.T) {
	f, _ := deviceFixture(t, apitest.Options{})
	s := approveDevice(t, f, f.LoginAlice())
	wantCode(t, "first poll", pollDevice(f, s.DeviceCode), http.StatusOK)
	wantDeviceError(t, pollDevice(f, s.DeviceCode), "expired_token")
}

func TestDeviceDeny(t *testing.T) {
	f, _ := deviceFixture(t, apitest.Options{})
	alice := f.LoginAlice()
	s := startDevice(t, f)
	wantCode(t, "deny", f.Do("POST", "/api/auth/device/"+s.UserCode+"/deny", "", alice), http.StatusNoContent)
	wantDeviceError(t, pollDevice(f, s.DeviceCode), "access_denied")
}

func TestDeviceExpires(t *testing.T) {
	f, clk := deviceFixture(t, apitest.Options{})
	alice := f.LoginAlice()
	s := startDevice(t, f)
	clk.Add(601 * time.Second)
	wantDeviceError(t, pollDevice(f, s.DeviceCode), "expired_token")
	wantCode(t, "view", f.Do("GET", "/api/auth/device/"+s.UserCode, "", alice), http.StatusGone)
	wantCode(t, "approve", f.Do("POST", "/api/auth/device/"+s.UserCode+"/approve", "", alice), http.StatusGone)
}

func TestDeviceDecidedOnce(t *testing.T) {
	f, _ := deviceFixture(t, apitest.Options{})
	alice := f.LoginAlice()
	s := startDevice(t, f)
	wantCode(t, "deny", f.Do("POST", "/api/auth/device/"+s.UserCode+"/deny", "", alice), http.StatusNoContent)
	wantCode(t, "approve", f.Do("POST", "/api/auth/device/"+s.UserCode+"/approve", "", alice), http.StatusConflict)
	wantDeviceError(t, pollDevice(f, s.DeviceCode), "access_denied")
}

// A token, even one from a device login, cannot approve one: it could make
// itself a new token forever.
func TestDeviceApproveRefusesToken(t *testing.T) {
	f, _ := deviceFixture(t, apitest.Options{})
	alice := f.LoginAlice()
	tok := f.NewToken(alice, `{"name":"agent","scope":"read-write","expiresInDays":1}`).Token
	s := startDevice(t, f)
	wantCode(t, "approve", f.Bearer("POST", "/api/auth/device/"+s.UserCode+"/approve", "", tok, nil), http.StatusForbidden)
	wantCode(t, "view", f.Bearer("GET", "/api/auth/device/"+s.UserCode, "", tok, nil), http.StatusForbidden)
	wantDeviceError(t, pollDevice(f, s.DeviceCode), "authorization_pending")
}

func TestDeviceStartRateLimited(t *testing.T) {
	f, _ := deviceFixture(t, apitest.Options{})
	for i := 0; i < 10; i++ {
		startDevice(t, f)
	}
	w := f.Do("POST", "/api/auth/device", "", nil)
	wantCode(t, "11th start", w, http.StatusTooManyRequests)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
}

func TestDeviceTokenLifetimeCapped(t *testing.T) {
	f, clk := deviceFixture(t, apitest.Options{MaxLifetime: time.Hour})
	alice := f.LoginAlice()
	s := startDevice(t, f)
	view := apitest.Decode[struct {
		UserCode   string `json:"userCode"`
		TokenHours int    `json:"tokenHours"`
		State      string `json:"state"`
	}](t, f.Do("GET", "/api/auth/device/"+strings.ToLower(s.UserCode), "", alice))
	if view.TokenHours != 1 || view.UserCode != s.UserCode || view.State != "pending" {
		t.Fatalf("view %+v", view)
	}
	wantCode(t, "approve", f.Do("POST", "/api/auth/device/"+s.UserCode+"/approve", "", alice), http.StatusNoContent)
	w := pollDevice(f, s.DeviceCode)
	wantCode(t, "poll", w, http.StatusOK)
	if got, want := apitest.Decode[deviceToken](t, w).ExpiresAt, clk.Now().Add(time.Hour); !got.Equal(want) {
		t.Fatalf("expiresAt %v, want %v", got, want)
	}
}

func TestDeviceUnknownCode(t *testing.T) {
	f, _ := deviceFixture(t, apitest.Options{})
	alice := f.LoginAlice()
	wantCode(t, "unknown", f.Do("GET", "/api/auth/device/BCDF-GHJK", "", alice), http.StatusNotFound)
	wantCode(t, "not a code", f.Do("GET", "/api/auth/device/AEIO-U123", "", alice), http.StatusNotFound)
	wantDeviceError(t, pollDevice(f, "not-a-device-code"), "expired_token")
}

// TestDeviceStartWithoutPublicURL checks a gateway without mcp.publicURL
// links the approval page at the address the request came to.
func TestDeviceStartWithoutPublicURL(t *testing.T) {
	f := apitest.New(t, apitest.Options{})
	r := httptest.NewRequest(http.MethodPost, "/api/auth/device", nil)
	r.Host = "javo.example.com"
	r.Header.Set("X-Javo-Request", "1")
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	wantCode(t, "start", w, http.StatusOK)
	s := apitest.Decode[deviceStart](t, w)
	if s.VerificationURL != "http://javo.example.com/device" {
		t.Fatalf("verificationURL %q", s.VerificationURL)
	}
	if want := "http://javo.example.com/device?code=" + s.UserCode; s.VerificationURLComplete != want {
		t.Fatalf("verificationURLComplete %q, want %q", s.VerificationURLComplete, want)
	}
}

// deviceFrom sends a request as Fixture.Do does, from the address ip.
func deviceFrom(f *apitest.Fixture, ip, method, path, body string, c *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("X-Javo-Request", "1")
	r.RemoteAddr = ip + ":40000"
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	return w
}

type deviceView struct {
	UserCode           string `json:"userCode"`
	AskedFrom          string `json:"askedFrom"`
	ClientName         string `json:"clientName"`
	FromAnotherAddress bool   `json:"fromAnotherAddress"`
}

// The approval page is told where the request came from and what the device
// calls itself, as the gateway recorded them at the start, and whether that
// address is another than the approver's own: the sign that someone else
// started it and sent the link.
func TestDeviceViewSaysWhoAsked(t *testing.T) {
	f, _ := deviceFixture(t, apitest.Options{})
	alice := f.LoginAlice()
	w := deviceFrom(f, "203.0.113.7", "POST", "/api/auth/device", `{"clientName":"javo agent on Linux"}`, nil)
	wantCode(t, "start", w, http.StatusOK)
	s := apitest.Decode[deviceStart](t, w)

	other := apitest.Decode[deviceView](t, deviceFrom(f, "198.51.100.4", "GET", "/api/auth/device/"+s.UserCode, "", alice))
	if other.AskedFrom != "203.0.113.7" || other.ClientName != "javo agent on Linux" || !other.FromAnotherAddress {
		t.Fatalf("seen from another address: %+v", other)
	}
	same := apitest.Decode[deviceView](t, deviceFrom(f, "203.0.113.7", "GET", "/api/auth/device/"+s.UserCode, "", alice))
	if same.AskedFrom != "203.0.113.7" || same.FromAnotherAddress {
		t.Fatalf("seen from the same address: %+v", same)
	}
}

// The name a device gives is shown to the person who approves, so it is
// plain visible text of at most 64 characters: nothing that hides, reorders
// or breaks what the page shows. A start with no body still works.
func TestDeviceClientNameIsPlainText(t *testing.T) {
	f, _ := deviceFixture(t, apitest.Options{})
	for what, name := range map[string]string{
		"a line break":             `javo\nApproved by your admin`,
		"a right-to-left override": `javo ‮gnp.exe`,
		"a zero-width space":       `ja​vo`,
		"an escape character":      `javo\u001b[2J`,
		"65 characters":            strings.Repeat("a", 65),
	} {
		w := f.Do("POST", "/api/auth/device", `{"clientName":"`+name+`"}`, nil)
		wantCode(t, what, w, http.StatusUnprocessableEntity)
	}
	wantCode(t, "64 characters", f.Do("POST", "/api/auth/device", `{"clientName":"`+strings.Repeat("é", 64)+`"}`, nil), http.StatusOK)
	wantCode(t, "no body", f.Do("POST", "/api/auth/device", "", nil), http.StatusOK)
}

// Behind an ingress with no trustedProxies every start comes from the
// ingress's address. That address keys no bucket of ten, which one caller
// would spend for everyone; the starts of everyone together are still
// bounded, at sixty and then one a second. Nor is it shown as where the
// request came from: it is the proxy's, not the asker's.
func TestDeviceStartsThroughAnUntrustedProxyShareOnlyTheWideLimit(t *testing.T) {
	f, _ := deviceFixture(t, apitest.Options{})
	alice := f.LoginAlice()
	var last deviceStart
	for i := range 60 {
		w := deviceFrom(f, "10.42.0.9", "POST", "/api/auth/device", "", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("start %d through the shared address: %d %s", i+1, w.Code, w.Body)
		}
		last = apitest.Decode[deviceStart](t, w)
	}
	// The bucket fills by one a second while the test runs: one of the next
	// few is refused.
	if w := startsUntilRefused(f, "10.42.0.9", 5); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("starts 61 to 65: %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	view := apitest.Decode[deviceView](t, deviceFrom(f, "10.42.0.10", "GET", "/api/auth/device/"+last.UserCode, "", alice))
	if view.AskedFrom != "" || view.FromAnotherAddress {
		t.Fatalf("view through the shared address: %+v", view)
	}
}

// Many addresses together get no more starts than the wide limit: ten each
// from six addresses spend it, and a seventh address gets none.
func TestDeviceStartsFromManyAddressesAreBoundedTogether(t *testing.T) {
	f, _ := deviceFixture(t, apitest.Options{})
	for a := range 6 {
		for i := range 10 {
			ip := "203.0.113." + strconv.Itoa(a+1)
			if w := deviceFrom(f, ip, "POST", "/api/auth/device", "", nil); w.Code != http.StatusOK {
				t.Fatalf("start %d from %s: %d %s", i+1, ip, w.Code, w.Body)
			}
		}
	}
	if w := startsUntilRefused(f, "203.0.113.7", 5); w.Code != http.StatusTooManyRequests {
		t.Fatalf("a seventh address: %d", w.Code)
	}
}

// startsUntilRefused starts up to n device logins from ip and returns the
// first answer that is not 200, or the last.
func startsUntilRefused(f *apitest.Fixture, ip string, n int) (w *httptest.ResponseRecorder) {
	for range n {
		if w = deviceFrom(f, ip, "POST", "/api/auth/device", "", nil); w.Code != http.StatusOK {
			break
		}
	}
	return w
}

// With auth.deviceLogin.enabled false there is no device login: none is
// started, read, approved or polled, and the guide that tells an agent to
// start one is not served.
func TestDeviceLoginTurnedOff(t *testing.T) {
	f, _ := deviceFixture(t, apitest.Options{NoDeviceLogin: true})
	alice := f.LoginAlice()
	for _, c := range []struct {
		method, path string
		cookie       *http.Cookie
	}{
		{"POST", "/api/auth/device", nil},
		{"POST", "/api/auth/device/token", nil},
		{"GET", "/api/auth/device/BCDF-GHJK", alice},
		{"POST", "/api/auth/device/BCDF-GHJK/approve", alice},
		{"GET", "/llms.txt", nil},
	} {
		body := ""
		if c.path == "/api/auth/device/token" {
			body = `{"deviceCode":"x"}`
		}
		wantCode(t, c.method+" "+c.path, f.Do(c.method, c.path, body, c.cookie), http.StatusNotFound)
	}
}

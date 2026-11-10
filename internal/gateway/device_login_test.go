package gateway_test

import (
	"net/http"
	"net/http/httptest"
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

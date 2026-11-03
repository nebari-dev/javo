package envnebi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/ext/envsource"
)

// fakeNebi answers like Nebi's API (internal/api/router.go in the Nebi
// repository): POST /api/v1/auth/device-token trades an OIDC ID token for a
// Nebi JWT, and every other route needs that JWT as a bearer token.
func fakeNebi(t *testing.T) *httptest.Server {
	t.Helper()
	const idToken, jwt = "id-token-alice", "nebi-jwt-alice"
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/device-token", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDToken string `json:"id_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.IDToken != idToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid or expired id_token"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": jwt, "username": "alice"})
	})
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+jwt {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /api/v1/workspaces", authed(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":"ws-1","name":"sales","status":"ready","owner":{"username":"alice"}},
			{"id":"ws-2","name":"half-made","status":"creating","owner":{"username":"alice"}},
			{"id":"ws-3","name":"shared-ml","status":"ready","owner":{"username":"bob"}}]`))
	}))
	mux.HandleFunc("GET /api/v1/workspaces/ws-1", authed(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"ws-1","name":"sales","status":"ready","owner":{"username":"alice"}}`))
	}))
	mux.HandleFunc("GET /api/v1/workspaces/ws-1/versions", authed(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"version_number":3},{"version_number":2},{"version_number":1}]`))
	}))
	mux.HandleFunc("GET /api/v1/workspaces/ws-1/versions/3/pixi-toml", authed(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[workspace]\nname = \"sales\"\n"))
	}))
	mux.HandleFunc("GET /api/v1/workspaces/ws-1/versions/3/pixi-lock", authed(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("version: 6\n"))
	}))
	mux.HandleFunc("GET /api/v1/workspaces/ws-9", authed(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestListShowsReadyEnvironmentsThePersonSees(t *testing.T) {
	src := New(Config{URL: fakeNebi(t).URL})
	got, err := src.List(context.Background(), "id-token-alice")
	if err != nil {
		t.Fatal(err)
	}
	want := []envsource.Environment{{ID: "ws-1", Name: "sales", Owner: "alice"}, {ID: "ws-3", Name: "shared-ml", Owner: "bob"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("List = %+v, want %+v", got, want)
	}
}

func TestFetchPinsTheNewestVersion(t *testing.T) {
	src := New(Config{URL: fakeNebi(t).URL})
	got, err := src.Fetch(context.Background(), "id-token-alice", "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "ws-1" || got.Name != "sales" || got.Version != "3" {
		t.Fatalf("Fetch = %+v, want ws-1 sales version 3", got.Environment)
	}
	if got.Manifest != "[workspace]\nname = \"sales\"\n" || got.Lock != "version: 6\n" {
		t.Fatalf("files = %q, %q", got.Manifest, got.Lock)
	}
}

func TestARefusedSignInIsErrRefused(t *testing.T) {
	src := New(Config{URL: fakeNebi(t).URL})
	if _, err := src.List(context.Background(), "someone-elses-token"); !errors.Is(err, envsource.ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
}

func TestAnEnvironmentThePersonCannotSeeIsErrNotFound(t *testing.T) {
	src := New(Config{URL: fakeNebi(t).URL})
	if _, err := src.Fetch(context.Background(), "id-token-alice", "ws-9"); !errors.Is(err, envsource.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestAnIDThatIsNotAPathSegmentIsRefusedBeforeAnyRequest(t *testing.T) {
	src := New(Config{URL: fakeNebi(t).URL})
	if _, err := src.Fetch(context.Background(), "id-token-alice", "../admin/users"); !errors.Is(err, envsource.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// A Nebi token for the person, with when it expires (the JWT's exp), for an
// app to call Nebi as its owner; a refused sign-in is ErrRefused.
func TestTokenIsThePersonsNebiTokenWithItsExpiry(t *testing.T) {
	exp := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	jwt := "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":"u-1","exp":%d}`, exp.Unix()))) + ".sig"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDToken string `json:"id_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/api/v1/auth/device-token" || body.IDToken != "id-token-alice" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": jwt, "username": "alice"})
	}))
	defer srv.Close()
	s := New(Config{URL: srv.URL})
	tok, got, err := s.Token(context.Background(), "id-token-alice")
	if err != nil || tok != jwt || !got.Equal(exp) {
		t.Fatalf("Token = %q, %v, %v; want the JWT expiring %v", tok, got, err, exp)
	}
	if _, _, err := s.Token(context.Background(), "id-token-mallory"); !errors.Is(err, envsource.ErrRefused) {
		t.Fatalf("a refused sign-in: %v", err)
	}
}

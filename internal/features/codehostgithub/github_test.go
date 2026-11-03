package codehostgithub

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/ext/codehost"
)

const repoURL = "https://github.com/example-org/site"

type pull struct {
	Number    int32     `json:"number"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updated_at"`
	Head      head      `json:"head"`
	Base      head      `json:"base"`
}

type head struct {
	SHA  string `json:"sha"`
	Repo *repo  `json:"repo"`
}

type repo struct {
	FullName string `json:"full_name"`
}

func samePull(n int32, sha string, at time.Time) pull {
	r := &repo{FullName: "example-org/site"}
	return pull{Number: n, Title: fmt.Sprintf("change %d", n), UpdatedAt: at, Head: head{SHA: sha, Repo: r}, Base: head{Repo: r}}
}

func host(t *testing.T, h http.Handler, tokenFile string) *Host {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(Config{APIURL: srv.URL, TokenFile: tokenFile})
}

func TestOpenPullsReadsEveryPageAndMarksForks(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/example-org/site/pulls", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "open" {
			t.Errorf("state = %q, want open", r.URL.Query().Get("state"))
		}
		if r.URL.Query().Get("page") == "2" {
			fork := samePull(3, "ccc", at)
			fork.Head.Repo = &repo{FullName: "bob/site"}
			_ = json.NewEncoder(w).Encode([]pull{fork})
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/example-org/site/pulls?page=2>; rel="next"`, srv.URL))
		_ = json.NewEncoder(w).Encode([]pull{samePull(1, "aaa", at), samePull(2, "bbb", at)})
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	got, err := New(Config{APIURL: srv.URL}).OpenPulls(context.Background(), repoURL, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []codehost.PullRequest{
		{Number: 1, Title: "change 1", HeadSHA: "aaa", UpdatedAt: at},
		{Number: 2, Title: "change 2", HeadSHA: "bbb", UpdatedAt: at},
		{Number: 3, Title: "change 3", HeadSHA: "ccc", Fork: true, UpdatedAt: at},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pull %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestOpenPullsStopsAtTheCap(t *testing.T) {
	var srv *httptest.Server
	pages := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		var ps []pull
		for i := 0; i < 60; i++ {
			ps = append(ps, samePull(int32(pages*100+i), "sha", time.Now()))
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/example-org/site/pulls?page=%d>; rel="next"`, srv.URL, pages+1))
		_ = json.NewEncoder(w).Encode(ps)
	}))
	defer srv.Close()
	got, err := New(Config{APIURL: srv.URL}).OpenPulls(context.Background(), repoURL, "")
	if err != nil || len(got) != codehost.MaxPulls || pages != 2 {
		t.Fatalf("got %d pulls in %d requests (%v), want %d in 2", len(got), pages, err, codehost.MaxPulls)
	}
}

func TestOpenPullsRateLimitCarriesTheResetTime(t *testing.T) {
	reset := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	h := host(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "60")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}), "")
	_, err := h.OpenPulls(context.Background(), repoURL, "")
	var rl *codehost.RateLimitError
	if !errors.As(err, &rl) || !rl.Reset.Equal(reset) {
		t.Fatalf("err = %v, want a RateLimitError resetting at %s", err, reset)
	}
}

func TestOpenPullsSecondaryRateLimitIsARateLimitToo(t *testing.T) {
	h := host(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "90")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"secondary","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#about-secondary-rate-limits"}`))
	}), "")
	before := time.Now()
	_, err := h.OpenPulls(context.Background(), repoURL, "")
	var rl *codehost.RateLimitError
	if !errors.As(err, &rl) || rl.Reset.Before(before.Add(80*time.Second)) || rl.Reset.After(before.Add(100*time.Second)) {
		t.Fatalf("err = %v (reset %v), want a RateLimitError about 90s ahead", err, rl)
	}
}

// Failures the app owner can fix (a wrong address, a private repository
// without a token, a refused token) are typed so they reach the owner as their
// own reason; anything else stays a plain failure.
func TestOpenPullsFailuresAreTyped(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"missing or hidden repository", http.StatusNotFound, `{"message":"Not Found"}`, codehost.ErrNotFound},
		{"bad token", http.StatusUnauthorized, `{"message":"Bad credentials"}`, codehost.ErrAuth},
		{"token without access", http.StatusForbidden, `{"message":"Resource not accessible by personal access token"}`, codehost.ErrAuth},
		{"server error", http.StatusBadGateway, `{"message":"Server Error"}`, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := host(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, c.body, c.status)
			}), "")
			_, err := h.OpenPulls(context.Background(), repoURL, "owner-token")
			var rl *codehost.RateLimitError
			if err == nil || errors.As(err, &rl) {
				t.Fatalf("err = %v, want a failure that is not a rate limit", err)
			}
			for _, typed := range []error{codehost.ErrNotFound, codehost.ErrAuth} {
				if errors.Is(err, typed) != (typed == c.want) {
					t.Fatalf("err = %v; is %v = %v, want %v", err, typed, errors.Is(err, typed), typed == c.want)
				}
			}
		})
	}
}

func TestOpenPullsCredentialOrder(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("platform-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var auth string
	record := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/example-org/site" {
			_, _ = w.Write([]byte(`{"private":false}`))
			return
		}
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("[]"))
	})
	for _, c := range []struct{ name, token, file, want string }{
		{"owner token wins", "owner-token", file, "Bearer owner-token"},
		{"platform file next, newline trimmed", "", file, "Bearer platform-token"},
		{"neither means anonymous", "", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			auth = "unset"
			if _, err := host(t, record, c.file).OpenPulls(context.Background(), repoURL, c.token); err != nil {
				t.Fatal(err)
			}
			if auth != c.want {
				t.Fatalf("Authorization = %q, want %q", auth, c.want)
			}
		})
	}
}

func TestPlatformTokenIsNeverUsedForAPrivateRepository(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("platform-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, owner string
		private     bool
		wantErr     bool
	}{
		{"public repo uses the platform token", "", false, false},
		{"private repo is refused", "", true, true},
		{"owner token reaches a private repo", "owner-token", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			pulled := false
			h := host(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/example-org/site" {
					_ = json.NewEncoder(w).Encode(map[string]any{"private": c.private})
					return
				}
				pulled = true
				_, _ = w.Write([]byte("[]"))
			}), file)
			_, err := h.OpenPulls(context.Background(), repoURL, c.owner)
			if (err != nil) != c.wantErr || pulled == c.wantErr {
				t.Fatalf("err = %v, pulls listed = %v", err, pulled)
			}
			// The owner can fix it by pasting a token, so it is theirs to fix.
			if c.wantErr && !errors.Is(err, codehost.ErrNotFound) {
				t.Fatalf("err = %v, want codehost.ErrNotFound", err)
			}
		})
	}
}

func TestOpenPullsErrorNeverCarriesTheToken(t *testing.T) {
	h := host(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	}), "")
	_, err := h.OpenPulls(context.Background(), repoURL, "ghp_testtoken123")
	if err == nil || strings.Contains(err.Error(), "ghp_testtoken123") {
		t.Fatalf("err = %v", err)
	}
}

func TestOwns(t *testing.T) {
	h := New(Config{})
	for url, want := range map[string]bool{
		repoURL:                                true,
		repoURL + ".git":                       true,
		"https://gitlab.com/example-org/site":  false,
		"https://github.com/example-org":       false,
		"http://github.com/example-org/site":   false,
		"https://example.com/example-org/site": false,
	} {
		if got := h.Owns(url); got != want {
			t.Errorf("Owns(%s) = %v, want %v", url, got, want)
		}
	}
}

func delivery(t *testing.T, body, secret string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-GitHub-Event", "pull_request")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(body))
		r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return r
}

func TestVerifyWebhook(t *testing.T) {
	h := New(Config{})
	body := `{"action":"opened"}`
	event, err := h.VerifyWebhook(delivery(t, body, "s3cret"), []byte("s3cret"))
	if err != nil || event != "pull_request" {
		t.Fatalf("valid signature: %q, %v", event, err)
	}
	if _, err := h.VerifyWebhook(delivery(t, body, "other"), []byte("s3cret")); err == nil {
		t.Error("a wrong signature was accepted")
	}
	if _, err := h.VerifyWebhook(delivery(t, body, ""), []byte("s3cret")); err == nil {
		t.Error("a missing signature header was accepted")
	}
}

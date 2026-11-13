package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/store"
)

// fixtureApp is the program of the fixture template: this test binary
// under that name serves one page on the port it is given.
const fixtureApp = "fixture-app"

// TestMain lets this test binary stand in for the javo binary and for the
// fixture app. The process executor runs the platform's own steps as the
// running binary, which in a test is this one: asked for anything but
// tests, it is javo.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == fixtureApp {
		serveFixture(os.Args[1])
	}
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-test.") {
		cmd := New(nil, nil)
		cmd.SetArgs(os.Args[1:])
		if err := cmd.Execute(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// serveFixture answers every request on loopback at port, until killed.
func serveFixture(port string) {
	err := http.ListenAndServe("127.0.0.1:"+port, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "fixture page")
	}))
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// server is a `javo server` running in this process, and a browser of
// one person signed in to it.
type server struct {
	t    *testing.T
	base string
	http *http.Client
	// done receives what the command returned.
	done chan error
	stop context.CancelFunc
}

const (
	testPerson   = "alice"
	testPassword = "correct horse battery staple"
)

// freeAddr is a loopback address nothing listens on now.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// seedAccount makes the local account people sign in with, as `javo user
// add` does, in the SQLite file at path before the server opens it.
func seedAccount(t *testing.T, path string, names ...string) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Config{Engine: store.EngineSQLite, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	hash, err := local.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := (&local.DBUsers{Accounts: db.LocalAccounts()}).Add(ctx, name, hash, nil, false); err != nil {
			t.Fatal(err)
		}
	}
}

// javoServer is the command `javo server` with args, as cmd/javo runs it.
func javoServer(ctx context.Context, args ...string) error {
	cmd := New(nil, nil)
	cmd.SetArgs(append([]string{"server"}, args...))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.ExecuteContext(ctx)
}

// startServer runs `javo server` with args on a free loopback port until
// the test ends, and waits until it is ready.
func startServer(t *testing.T, args ...string) *server {
	t.Helper()
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	s := &server{t: t, base: "http://" + addr, done: make(chan error, 1), stop: cancel}
	jar, _ := cookiejar.New(nil)
	s.http = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	go func() { s.done <- javoServer(ctx, append([]string{"--listen", addr, "--ui-dir", ""}, args...)...) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-s.done:
		case <-time.After(30 * time.Second):
			t.Error("javo server did not stop")
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-s.done:
			s.done <- err
			t.Fatalf("javo server ended: %v", err)
		default:
		}
		if resp, err := s.http.Get(s.base + "/readyz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("javo server never became ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// call sends one API request as the signed-in person and decodes the
// answer into out (unless nil); it returns the status.
func (s *server) call(method, path string, body, out any) int {
	s.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.base+path, r)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(authn.RequestHeader, "1")
	resp, err := s.http.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			s.t.Fatalf("%s %s: %v in %s", method, path, err, raw)
		}
	}
	if resp.StatusCode >= 400 {
		s.t.Logf("%s %s: %d %s", method, path, resp.StatusCode, raw)
	}
	return resp.StatusCode
}

// signIn signs name in through the login route, which makes the person.
func (s *server) signIn(name string) int {
	s.t.Helper()
	return s.call(http.MethodPost, "/api/auth/local/login", map[string]string{"username": name, "password": testPassword}, nil)
}

// appView is the part of an app's view the tests read.
type appView struct {
	Name  string `json:"name"`
	Phase string `json:"phase"`
	// URL is the app's address: a path on the gateway's host here.
	URL   string `json:"url"`
	Steps []struct {
		Type, Status, Reason string
		Evidence             []string
	} `json:"steps"`
}

// createApp makes an app of template through the API and returns its name.
func (s *server) createApp(displayName, template string) string {
	s.t.Helper()
	var made appView
	if code := s.call(http.MethodPost, "/api/workspaces", map[string]string{"displayName": displayName, "template": template}, &made); code != http.StatusCreated {
		s.t.Fatalf("create app: %d", code)
	}
	return made.Name
}

// waitApp reads the app until ok says so, and fails with its last view
// after a minute.
func (s *server) waitApp(name, what string, ok func(appView) bool) appView {
	s.t.Helper()
	var last appView
	var raw json.RawMessage
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		last = appView{}
		if code := s.call(http.MethodGet, "/api/workspaces/"+name, nil, &raw); code == http.StatusOK {
			if err := json.Unmarshal(raw, &last); err != nil {
				s.t.Fatal(err)
			}
			if ok(last) {
				return last
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.t.Fatalf("the app is not %s: %s", what, raw)
	return last
}

// write makes the file at path hold text, with its folders.
func write(t *testing.T, path, text string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

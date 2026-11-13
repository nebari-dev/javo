//go:build e2e && (linux || darwin)

package cli

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/authn"
)

// localJavo is one run of the binary `make local` builds.
type localJavo struct {
	t    *testing.T
	cmd  *exec.Cmd
	base string
	// link is the sign-in link it printed.
	link string
	done chan error
	http *http.Client
}

var (
	localAddress = regexp.MustCompile(`Javo is at (http://127\.0\.0\.1:\d+) `)
	localLink    = regexp.MustCompile(`http://127\.0\.0\.1:\d+/signin\?token=\S+`)
)

// startLocal runs `javo local` in dir on a free port, with no other flag
// and no config, and waits for the address and the link it prints.
func startLocal(t *testing.T, bin, dir string) *localJavo {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	j := &localJavo{t: t, done: make(chan error, 1), http: &http.Client{
		Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	j.cmd = exec.Command(bin, "local", "--port", "0", "--dir", dir)
	out, err := j.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	j.cmd.Stderr = os.Stderr
	if err := j.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string)
	go func() {
		defer close(lines)
		for s := bufio.NewScanner(out); s.Scan(); {
			lines <- s.Text()
		}
	}()
	t.Cleanup(func() { _ = j.cmd.Process.Kill() })
	timeout := time.After(time.Minute)
	for j.link == "" {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("javo local ended before it printed a sign-in link")
			}
			t.Log(line)
			if m := localAddress.FindStringSubmatch(line); m != nil {
				j.base = m[1]
			}
			j.link = localLink.FindString(line)
		case <-timeout:
			t.Fatal("javo local printed no sign-in link within a minute")
		}
	}
	if j.base == "" {
		t.Fatal("javo local printed no address")
	}
	go func() {
		// Whatever it prints later is read, so it never blocks on the pipe.
		for range lines {
		}
		j.done <- j.cmd.Wait()
	}()
	return j
}

// get is the status and body of a GET, with headers given as pairs.
func (j *localJavo) get(url string, headers ...string) (int, string) {
	j.t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		j.t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
			continue
		}
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := j.http.Do(req)
	if err != nil {
		j.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// stop ends it as Ctrl-C does and waits for it.
func (j *localJavo) stop() {
	j.t.Helper()
	if err := j.cmd.Process.Signal(syscall.SIGINT); err != nil {
		j.t.Fatal(err)
	}
	select {
	case err := <-j.done:
		if err != nil {
			j.t.Fatalf("javo local ended with %v", err)
		}
	case <-time.After(time.Minute):
		j.t.Fatal("javo local did not stop within a minute of Ctrl-C")
	}
}

// The binary `make local` builds starts with nothing prepared: no config
// file, no folders, no account, no --runner-image. It prints where it is
// and a link that signs the one person in, serves the UI it carries,
// refuses a request that is not for its own address, runs a shipped
// template as a process when this machine has pixi, stops with its apps
// on Ctrl-C, and leaves one private folder that a second start reuses
// without unpacking its files again.
//
// JAVO_E2E_UI_DIST names a built UI (ui/dist) to carry; without it the
// binary carries a one-page stand-in, which is all this test reads.
func TestJavoLocalStartsWithNothingPrepared(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ui := os.Getenv("JAVO_E2E_UI_DIST")
	if ui == "" {
		ui = t.TempDir()
		write(t, filepath.Join(ui, "index.html"), "<html>the UI of the test</html>", 0o644)
	}
	out := t.TempDir()
	build := exec.Command(filepath.Join("scripts", "build", "local-javo.sh"), out)
	build.Dir = repo
	build.Env = append(os.Environ(), "UI_DIST="+ui)
	if msg, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the local binary: %v\n%s", err, msg)
	}
	bin := filepath.Join(out, runtime.GOOS+"_"+runtime.GOARCH, "javo")
	if _, err := os.Stat(filepath.Join(repo, "internal", "bundle", "assets", "bundle.zip")); err == nil {
		t.Error("the build left its archive in the repository")
	}

	dir := filepath.Join(t.TempDir(), "javo")
	j := startLocal(t, bin, dir)

	index, err := os.ReadFile(filepath.Join(ui, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if code, page := j.get(j.base + "/"); code != http.StatusOK || page != string(index) {
		t.Errorf("the UI: %d %q", code, page)
	}
	if code, page := j.get(j.base + "/apps/new"); code != http.StatusOK || page != string(index) {
		t.Errorf("a route of the UI: %d", code)
	}
	if code, _ := j.get(j.base + "/api/me"); code != http.StatusUnauthorized {
		t.Errorf("the API before sign-in: %d, want 401", code)
	}
	if code, _ := j.get(j.base+"/", "Host", "rebind.example.com"); code != http.StatusForbidden {
		t.Errorf("a request for another host: %d, want 403", code)
	}
	if code, _ := j.get(j.base+"/api/me", "Origin", "http://127.0.0.1:1"); code != http.StatusForbidden {
		t.Errorf("a request from another origin: %d, want 403", code)
	}

	// The link signs in once.
	if code, _ := j.get(j.link); code != http.StatusSeeOther {
		t.Fatalf("the sign-in link: %d, want 303", code)
	}
	var me struct {
		Username string
		Roles    []string
	}
	code, body := j.get(j.base + "/api/me")
	if err := json.Unmarshal([]byte(body), &me); code != http.StatusOK || err != nil || me.Username != "local" || !strings.Contains(strings.Join(me.Roles, " "), "platform-admin") {
		t.Fatalf("the signed-in person: %d %s", code, body)
	}
	if code, _ := j.get(j.link); code != http.StatusForbidden {
		t.Errorf("the sign-in link a second time: %d, want 403", code)
	}

	// The templates it carries: the ones that run without containers.
	var templates struct{ Items []struct{ Name string } }
	_, body = j.get(j.base + "/api/templates")
	if err := json.Unmarshal([]byte(body), &templates); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tpl := range templates.Items {
		names[tpl.Name] = true
	}
	if !names["jupyterlab"] || !names["terminal"] || names["container"] || names["rstudio"] {
		t.Errorf("the templates: %v", names)
	}

	var appURL string
	if _, err := exec.LookPath("pixi"); err != nil {
		t.Log("no pixi on PATH: no app is started")
	} else {
		appURL = j.runSample()
	}

	unpacked, err := filepath.Glob(filepath.Join(dir, "files", "*", "pod-user.sh"))
	if err != nil || len(unpacked) != 1 {
		t.Fatalf("the platform's files: %v, %v", unpacked, err)
	}
	before, err := os.Stat(unpacked[0])
	if err != nil {
		t.Fatal(err)
	}
	j.stop()
	if appURL != "" {
		if resp, err := http.Get(appURL); err == nil {
			resp.Body.Close()
			t.Error("the app still answers on its own port after Javo stopped")
		}
	}

	// What is left: one folder only this user can open.
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("%s: %v, %v", dir, info, err)
	}
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want := "files homes javo.db keys state"
	if appURL == "" {
		// The stand-in that says pixi is missing.
		want = "files homes javo.db keys no-pixi state"
	}
	if got := strings.Join(left, " "); got != want {
		t.Errorf("the folder holds %q, want %q", got, want)
	}

	// A second start of the same binary: the same person's link works,
	// and the files are the ones unpacked before.
	again := startLocal(t, bin, dir)
	if code, _ := again.get(again.link); code != http.StatusSeeOther {
		t.Errorf("the sign-in link of the second start: %d", code)
	}
	after, err := os.Stat(unpacked[0])
	if err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the platform's files were unpacked again: %v, %v", after, err)
	}
	again.stop()
}

// runSample starts the sample of the custom template, the lightest of
// the shipped ones (its environment is Python alone), waits until it
// runs, reads its page through the gateway and returns the address of
// its own port.
func (j *localJavo) runSample() string {
	j.t.Helper()
	req, err := http.NewRequest(http.MethodPost, j.base+"/api/workspaces", strings.NewReader(`{"displayName":"Demo","template":"custom","sample":true}`))
	if err != nil {
		j.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(authn.RequestHeader, "1")
	resp, err := j.http.Do(req)
	if err != nil {
		j.t.Fatal(err)
	}
	var app appView
	err = json.NewDecoder(resp.Body).Decode(&app)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || err != nil {
		j.t.Fatalf("create the app: %d, %v", resp.StatusCode, err)
	}
	var body string
	// pixi may have to download Python the first time.
	for deadline := time.Now().Add(15 * time.Minute); app.Phase != "Running"; time.Sleep(time.Second) {
		if time.Now().After(deadline) || app.Phase == "Failed" {
			j.t.Fatalf("the app is %s: %s", app.Phase, body)
		}
		_, body = j.get(j.base + "/api/workspaces/" + app.Name)
		if err := json.Unmarshal([]byte(body), &app); err != nil {
			j.t.Fatal(err)
		}
	}
	if code, page := j.get(j.base + app.URL); code != http.StatusOK || !strings.Contains(page, "Your custom app is running") {
		j.t.Errorf("the app through the gateway: %d %.200q", code, page)
	}
	for _, step := range app.Steps {
		if step.Type == "Ready" && len(step.Evidence) > 0 {
			_, port, _ := strings.Cut(strings.TrimSuffix(step.Evidence[0], app.URL), ":")
			return "http://127.0.0.1:" + port + app.URL
		}
	}
	j.t.Error("the app's steps do not say which port it answers on")
	return ""
}

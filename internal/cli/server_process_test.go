//go:build linux || darwin

package cli

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// processInstall is the files of a Javo that runs apps as processes, in a
// folder of the test: its config, an empty database with one account, a
// fixture template and the stand-ins for pixi and the platform's files.
type processInstall struct {
	dir, config, db string
	args            []string
}

// fixtureTemplate is an app whose command is a shell one-liner that
// serves on the port it is given. It runs in a pixi environment, as every
// template that needs no image does; the environment here is installed by
// a stand-in for pixi that puts the fixture app on its PATH.
const fixtureTemplate = `apiVersion: javo.nebari.dev/v1alpha1
kind: WorkspaceTemplate
metadata:
  name: fixture
spec:
  displayName: Fixture
  kind: custom
  environment: fixture
  port: 8080
  command: ["/opt/javo/pod-user.sh", "sh", "-c", "exec ` + fixtureApp + ` {{.Port}}"]
  appLogin: Enabled
  readinessPath: "/"
`

func newProcessInstall(t *testing.T, accounts ...string) processInstall {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The platform's files: the script every app starts through, from the
	// repository, and one environment.
	files := filepath.Join(dir, "files")
	script, err := os.ReadFile(filepath.Join("..", "..", "pod", "files", "pod-user.sh"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(files, "pod-user.sh"), string(script), 0o700)
	write(t, filepath.Join(files, "envs", "fixture", "pixi.toml"), "[workspace]\nname = \"fixture\"\n", 0o600)
	// pixi, as far as the install step uses it: "install" makes the
	// environment's folder with the fixture app in it, and "shell-hook"
	// prints the activation that puts it on PATH.
	pixi := write(t, filepath.Join(dir, "pixi"), `#!/bin/sh
bin=$PWD/.pixi/envs/default/bin
case "$1" in
install) mkdir -p "$bin" && ln -s "`+self+`" "$bin/`+fixtureApp+`" ;;
shell-hook) echo "export PATH=\"$bin:\$PATH\"" ;;
esac
`, 0o700)
	in := processInstall{dir: dir, db: filepath.Join(dir, "javo.db")}
	in.config = write(t, filepath.Join(dir, "config.yaml"), `auth:
  local:
    enabled: true
urls:
  scheme: http
executor: process
keysDir: `+filepath.Join(dir, "keys")+`
process:
  stateDir: `+filepath.Join(dir, "state")+`
  homesDir: `+filepath.Join(dir, "homes")+`
  pixi: `+pixi+`
  filesDir: `+files+`
`, 0o600)
	templates := filepath.Join(dir, "templates")
	write(t, filepath.Join(templates, "fixture.yaml"), fixtureTemplate, 0o600)
	seedAccount(t, in.db, accounts...)
	in.args = []string{"--config", in.config, "--database-path", in.db, "--templates-dir", templates, "--runner-image", "example.com/javo:1"}
	return in
}

// `javo server` with executor process starts with no cluster and no
// kubeconfig, signs a person in, runs their app as a process of this
// machine until it is Running, and serves it through the gateway.
func TestServerRunsAnAppAsAProcess(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "none"))
	in := newProcessInstall(t, testPerson)
	s := startServer(t, in.args...)
	if code := s.signIn(testPerson); code != http.StatusOK {
		t.Fatalf("sign in: %d", code)
	}
	name := s.createApp("Demo", "fixture")
	app := s.waitApp(name, "Running", func(a appView) bool { return a.Phase == "Running" })

	// Through the gateway's proxy, which asks the executor where it is.
	resp, err := s.http.Get(s.base + app.URL)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(page) != "fixture page" {
		t.Fatalf("GET %s: %d %q", app.URL, resp.StatusCode, page)
	}

	// The person's home is a folder of this machine, read without a pod.
	if code := s.call(http.MethodGet, "/api/files", nil, nil); code != http.StatusOK {
		t.Errorf("list the home folder: %d", code)
	}
	// What only a cluster has answers that it is not available, or with
	// nothing, and never fails for want of a cluster.
	if code := s.call(http.MethodGet, "/api/storage/placement?template=fixture", nil, nil); code != http.StatusNotImplemented {
		t.Errorf("placement preview: %d, want 501", code)
	}
	if code := s.call(http.MethodGet, "/api/workspaces/"+name+"/events", nil, nil); code != http.StatusOK {
		t.Errorf("events: %d", code)
	}

	// Stopping Javo stops the app: nothing serves on its port any more.
	var ready string
	for _, step := range app.Steps {
		if step.Type == "Ready" && len(step.Evidence) > 0 {
			ready = step.Evidence[0] // "checked GET :<port>/"
		}
	}
	_, port, _ := strings.Cut(strings.TrimSuffix(ready, "/"), ":")
	if _, err := http.Get("http://127.0.0.1:" + port + "/"); err != nil {
		t.Fatalf("the app does not answer on its own port %q: %v", port, err)
	}
	s.stop()
	if err := <-s.done; err != nil {
		t.Fatalf("javo server = %v", err)
	}
	s.done <- nil
	if resp, err := http.Get("http://127.0.0.1:" + port + "/"); err == nil {
		resp.Body.Close()
		t.Error("the app still answers after the server stopped")
	}
}

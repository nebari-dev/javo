//go:build linux || darwin

package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/executor/docker/dockerlive"
	"github.com/nebari-dev/javo/internal/executor/docker/testrun"
)

// dockerTemplate is an app that serves one page from the test image.
const dockerTemplate = `apiVersion: javo.nebari.dev/v1alpha1
kind: WorkspaceTemplate
metadata:
  name: page
spec:
  displayName: Page
  kind: custom
  image: ` + dockerlive.Busybox + `
  port: 8080
  command: ["sh", "-c", "mkdir -p /tmp/w && echo page > /tmp/w/index.html && exec httpd -f -p {{.Port}} -h /tmp/w"]
  appLogin: Enabled
  readinessPath: "/"
  resources:
    limits: {cpu: 500m, memory: 64Mi}
`

// The scopes a Javo marks what it makes on a daemon with (wiring), each
// a label whose value is its gateway container's name.
var dockerScopes = []string{"javo.gateway", "javo.gateway.files", "javo.gateway.homes", "javo.gateway.probe"}

// gatewayContainer starts a container that stands in for the one Javo
// runs in, under a name of this test process and with this machine's
// host name, which is how Javo tells that it runs inside it. When the
// test ends it removes what a Javo of that name made on the daemon, in
// every scope, and fails the test for anything left.
func gatewayContainer(t *testing.T, d *dockerlive.Daemon) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	name := "javo-wiring-gateway-" + testrun.ID
	self, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	var made struct {
		ID string `json:"Id"`
	}
	if err := d.Call(ctx, http.MethodPost, "/containers/create", map[string][]string{"name": {name}}, map[string]any{
		"Image": dockerlive.Busybox, "Cmd": []string{"sleep", "86400"}, "Hostname": self, "Labels": dockerlive.Stamp(),
	}, &made); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		for _, scope := range dockerScopes {
			c := docker.New(testrun.Socket(t))
			mark := scope + "=" + name
			containers, _ := c.ContainerList(ctx, mark)
			networks, _ := c.NetworkList(ctx, mark)
			for _, have := range containers {
				if err := docker.Teardown(ctx, c, docker.RefOf(have.Labels), name); err != nil {
					t.Errorf("removing what the server left: %v", err)
				}
			}
			for _, have := range networks {
				if err := docker.Teardown(ctx, c, docker.RefOf(have.Labels), name); err != nil {
					t.Errorf("removing what the server left: %v", err)
				}
			}
			volumes, _ := c.VolumeList(ctx, mark)
			for _, v := range volumes {
				if err := c.VolumeRemove(ctx, v.Name); err != nil {
					t.Errorf("removing what the server left: %v", err)
				}
			}
			containers, _ = c.ContainerList(ctx, mark)
			networks, _ = c.NetworkList(ctx, mark)
			volumes, _ = c.VolumeList(ctx, mark)
			if len(containers)+len(networks)+len(volumes) != 0 {
				t.Errorf("left on the daemon in scope %s: %v %v %v", scope, containers, networks, volumes)
			}
		}
	})
	if err := d.Call(ctx, http.MethodPost, "/containers/"+made.ID+"/start", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	return name
}

// rcloneImage is the rclone image the chart names for file pods, which
// the file helpers run too; the test is skipped when the daemon cannot
// get it.
func rcloneImage(t *testing.T, d *dockerlive.Daemon) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "helm", "javo", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*image: (rclone/rclone:\S+@sha256:[0-9a-f]{64})\s*$`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("the chart's values name no rclone image pinned by digest")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := docker.EnsureImage(ctx, d.Client, string(m[1]), false, nil); err != nil {
		t.Skipf("cannot pull %s: %v", m[1], err)
	}
	return string(m[1])
}

// `javo server` with executor docker starts against a real daemon with no
// cluster and no kubeconfig, finds its own container, signs a person in,
// gives them a home that is a volume of theirs, starts their app's
// containers and a file helper for their home, and keeps the two apart:
// what runs the apps never sees the helper.
//
// The app does not reach Running here and the helper is never reached:
// Javo checks an app's port, and talks to a helper, from inside the
// gateway container, and this test's Javo runs on the host and only
// takes itself to be there. The whole of it, with Javo in its container,
// is what deploy/compose/docker.yaml runs.
func TestServerStartsOnDocker(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the javo binary into an image")
	}
	d := dockerlive.New(t)
	ctx := context.Background()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "none"))
	gateway := gatewayContainer(t, d)
	runner := d.Image(t, map[string]string{"/javo": "./cmd/javo"})
	dir := t.TempDir()
	db := filepath.Join(dir, "javo.db")
	config := write(t, filepath.Join(dir, "config.yaml"), `auth:
  local:
    enabled: true
urls:
  scheme: http
executor: docker
keysDir: `+filepath.Join(dir, "keys")+`
docker:
  socket: `+testrun.Socket(t)+`
  gateway: `+gateway+`
  proxyPort: `+freePort(t)+`
`, 0o600)
	templates := filepath.Join(dir, "templates")
	write(t, filepath.Join(templates, "page.yaml"), dockerTemplate, 0o600)
	seedAccount(t, db, testPerson)

	s := startServer(t, "--config", config, "--database-path", db, "--templates-dir", templates,
		"--runner-image", runner, "--files-image", rcloneImage(t, d))
	if code := s.signIn(testPerson); code != http.StatusOK {
		t.Fatalf("sign in: %d", code)
	}
	name := s.createApp("Page", "page")
	in := func(scope string) []docker.ContainerSummary {
		t.Helper()
		all, err := d.Client.ContainerList(ctx, scope+"="+gateway)
		if err != nil {
			t.Fatal(err)
		}
		return all
	}
	// The home is a volume of the person's, handed to them, and the app's
	// containers start on it.
	s.waitApp(name, "past its home", func(a appView) bool {
		for _, step := range a.Steps {
			if step.Type == "HomeReady" && step.Status == "True" {
				return len(in("javo.gateway")) > 0
			}
		}
		return false
	})
	homes, err := docker.New(testrun.Socket(t)).VolumeList(ctx, "javo.gateway.homes="+gateway)
	if err != nil || len(homes) != 1 || homes[0].Labels[docker.LabelOwner] == "" {
		t.Fatalf("the person's home volume: %+v, %v", homes, err)
	}
	// Asking for the files starts the person's helper, in a scope of its
	// own; it is not ready, never "not available".
	if code := s.call(http.MethodGet, "/api/files", nil, nil); code == http.StatusNotImplemented || code == http.StatusOK {
		t.Errorf("list the home folder: %d, want it to say the home is opening", code)
	}
	deadline := time.Now().Add(time.Minute)
	for len(in("javo.gateway.files")) == 0 && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	helpers := in("javo.gateway.files")
	if len(helpers) == 0 {
		t.Fatal("no file helper was started")
	}
	for _, c := range append(helpers, in("javo.gateway")...) {
		if c.Labels["javo.gateway"] != "" && c.Labels["javo.gateway.files"] != "" {
			t.Errorf("container %v is in two scopes: %v", c.Names, c.Labels)
		}
	}
	// Removing the app removes its containers and leaves the helper.
	if code := s.call(http.MethodDelete, "/api/workspaces/"+name, nil, nil); code >= 300 {
		t.Fatalf("delete the app: %d", code)
	}
	for deadline = time.Now().Add(time.Minute); len(in("javo.gateway")) != 0 && time.Now().Before(deadline); {
		time.Sleep(200 * time.Millisecond)
	}
	if left := in("javo.gateway"); len(left) != 0 {
		t.Errorf("the app's containers stayed: %+v", left)
	}
	if len(in("javo.gateway.files")) == 0 {
		t.Error("the file helper went with the app")
	}
}

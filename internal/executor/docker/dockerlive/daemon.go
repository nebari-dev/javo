// Package dockerlive gives tests outside the docker package a real
// Docker daemon to run against: a client whose every creation carries a
// label of this test process, a cleanup that removes what carries that
// label and nothing else, and a small test image that holds programs of
// this repository. Tests are skipped when no daemon answers.
package dockerlive

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/executor/docker"
)

// Busybox is busybox 1.37.0, by the digest of its image index on Docker
// Hub: the image the docker package's own daemon tests run.
const Busybox = "busybox@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec"

// Labels of what the tests create: Label marks all of it, RunLabel this
// test process's own.
const (
	Label    = "javo.test"
	RunLabel = "javo.test.run"
)

// run is this test process, in RunLabel.
var run = strings.ToLower(rand.Text())

// Daemon is the Docker daemon of this machine.
type Daemon struct {
	// Client stamps Label and RunLabel on everything it creates.
	Client *docker.Client
	// Arch is the daemon's processor, as Go names it.
	Arch string

	raw *http.Client
}

// Mark is the label filter that finds what this test process made.
func Mark() string { return RunLabel + "=" + run }

// Stamp is the labels every creation of this test process carries.
func Stamp() map[string]string { return map[string]string{Label: "1", RunLabel: run} }

// New finds the daemon and makes it hold Busybox, or skips the test. When
// the test ends, everything this process made is removed.
func New(t *testing.T) *Daemon {
	t.Helper()
	home, _ := os.UserHomeDir()
	sockets := []string{"/var/run/docker.sock", filepath.Join(home, ".colima/default/docker.sock"), filepath.Join(home, ".docker/run/docker.sock")}
	if h, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok {
		sockets = []string{h}
	}
	for _, sock := range sockets {
		c := docker.New(sock)
		c.Labels = Stamp()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, err := c.Daemon(ctx)
		cancel()
		if errors.Is(err, docker.ErrTooOld) {
			t.Skipf("the Docker daemon at %s is too old: %v", sock, err)
		}
		if err != nil {
			continue
		}
		d := &Daemon{Client: c, raw: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", sock)
			},
		}}}
		ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := docker.EnsureImage(ctx, c, Busybox, false, nil); err != nil {
			t.Skipf("cannot pull %s: %v", Busybox, err)
		}
		var v struct{ Arch string }
		if err := d.Call(ctx, http.MethodGet, "/version", nil, nil, &v); err != nil {
			t.Fatal(err)
		}
		d.Arch = v.Arch
		t.Cleanup(func() { d.sweep(t) })
		return d
	}
	t.Skip("no Docker daemon answers")
	return nil
}

// Call sends one request of the Engine API as it is, for what the docker
// package has no call for (it only makes confined things): body is sent
// as JSON, or as a tar stream when it is a reader, and the answer is read
// into out unless that is nil.
func (d *Daemon) Call(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var send io.Reader
	contentType := "application/json"
	switch b := body.(type) {
	case nil:
	case io.Reader:
		send, contentType = b, "application/x-tar"
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		send = bytes.NewReader(raw)
	}
	u := "http://docker/v" + docker.MinAPI + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, send)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := d.raw.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("docker: %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// sweep removes what this test process created, and only that.
func (d *Daemon) sweep(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	del := func(path string, query url.Values) {
		if err := d.Call(ctx, http.MethodDelete, path, query, nil, nil); err != nil && !strings.Contains(err.Error(), "HTTP 404") {
			t.Errorf("cleanup of %s: %v", path, err)
		}
	}
	containers, err := d.Client.ContainerList(ctx, Mark())
	if err != nil {
		t.Errorf("cleanup: %v", err)
	}
	for _, x := range containers {
		del("/containers/"+x.ID, url.Values{"force": {"1"}, "v": {"1"}})
	}
	networks, err := d.Client.NetworkList(ctx, Mark())
	if err != nil {
		t.Errorf("cleanup: %v", err)
	}
	for _, x := range networks {
		del("/networks/"+x.ID, nil)
	}
	var images []struct {
		ID string `json:"Id"`
	}
	filter, _ := json.Marshal(map[string][]string{"label": {Mark()}})
	if err := d.Call(ctx, http.MethodGet, "/images/json", url.Values{"filters": {string(filter)}}, nil, &images); err != nil {
		t.Errorf("cleanup: %v", err)
	}
	for _, x := range images {
		del("/images/"+x.ID, url.Values{"force": {"1"}})
	}
}

// Image builds an image for this test process alone: Busybox plus, at
// each path of programs, the Linux build of the Go package named there
// (a path from the repository's root, such as "./cmd/javo"). It is made
// with nothing but the go tool and the daemon: the programs are put in a
// container of Busybox that never starts, and the container is committed.
// The image carries the test labels and is removed with everything else.
func (d *Daemon) Image(t *testing.T, programs map[string]string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	root, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("the repository's root: %v", err)
	}
	var files bytes.Buffer
	w := tar.NewWriter(&files)
	for at, pkg := range programs {
		bin := filepath.Join(t.TempDir(), "program")
		build := exec.CommandContext(ctx, "go", "build", "-o", bin, pkg)
		build.Dir = strings.TrimSpace(string(root))
		build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+d.Arch, "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build of %s: %v\n%s", pkg, err, out)
		}
		raw, err := os.ReadFile(bin)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.WriteHeader(&tar.Header{Name: strings.TrimPrefix(at, "/"), Mode: 0o755, Size: int64(len(raw))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var made struct {
		ID string `json:"Id"`
	}
	if err := d.Call(ctx, http.MethodPost, "/containers/create", nil, map[string]any{"Image": Busybox, "Labels": Stamp()}, &made); err != nil {
		t.Fatal(err)
	}
	if err := d.Client.Extract(ctx, made.ID, "/", &files); err != nil {
		t.Fatal(err)
	}
	repo := "javo-test-" + run
	tag := strings.ToLower(rand.Text())
	// The container's labels become the image's.
	if err := d.Call(ctx, http.MethodPost, "/commit", url.Values{"container": {made.ID}, "repo": {repo}, "tag": {tag}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.Call(ctx, http.MethodDelete, "/containers/"+made.ID, url.Values{"force": {"1"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	return repo + ":" + tag
}

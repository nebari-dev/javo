// Package dockerlive gives tests outside the docker package a real
// Docker daemon to run against: a client whose every creation carries
// the label of this test process, a cleanup that removes what carries
// that label and nothing else (both are testrun's, which the docker
// package's own daemon tests use too), and a small test image that holds
// programs of this repository. Tests are skipped when no daemon answers.
package dockerlive

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/executor/docker/testrun"
)

// Busybox is busybox 1.37.0, by the digest of its image index on Docker
// Hub: the image the docker package's own daemon tests run.
const Busybox = "busybox@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec"

// Daemon is the Docker daemon of this machine.
type Daemon struct {
	// Client stamps this test process's label on everything it creates.
	Client *docker.Client
	// Arch is the daemon's processor, as Go names it.
	Arch string

	socket string
}

// Mark is the label filter that finds what this test process made.
func Mark() string { return testrun.Mark() }

// Stamp is the labels every creation of this test process carries.
func Stamp() map[string]string { return testrun.Stamp() }

// New finds the daemon and makes it hold Busybox, or skips the test. When
// the test ends, everything this process made is removed.
func New(t *testing.T) *Daemon {
	t.Helper()
	d := &Daemon{socket: testrun.Socket(t)}
	d.Client = docker.New(d.socket)
	d.Client.Labels = Stamp()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := d.Client.Daemon(ctx); err != nil {
		t.Skipf("the Docker daemon at %s: %v", d.socket, err)
	}
	if err := docker.EnsureImage(ctx, d.Client, Busybox, false, nil); err != nil {
		t.Skipf("cannot pull %s: %v", Busybox, err)
	}
	var v struct{ Arch string }
	if err := d.Call(ctx, http.MethodGet, "/version", nil, nil, &v); err != nil {
		t.Fatal(err)
	}
	d.Arch = v.Arch
	t.Cleanup(func() { testrun.Sweep(t, d.socket) })
	return d
}

// Call sends one request of the Engine API as it is, for what the docker
// package has no call for (it only makes confined things): body is sent
// as JSON, or as a tar stream when it is a reader, and the answer is read
// into out unless that is nil.
func (d *Daemon) Call(ctx context.Context, method, path string, query url.Values, body, out any) error {
	return testrun.Call(ctx, d.socket, method, path, query, body, out)
}

// Image builds an image for this test process alone: Busybox plus, at
// each path of programs, the Linux build of the Go package named there
// (a path from the repository's root, such as "./cmd/javo"). It is made
// with nothing but the go tool and the daemon: the programs are put in a
// container of Busybox that never starts, and the container is committed.
// The image carries the test labels and is removed with everything else.
func (d *Daemon) Image(t *testing.T, programs map[string]string) string {
	t.Helper()
	return d.ImageWith(t, programs, nil)
}

// ImageWith is Image with, at each path of files, the file named there,
// executable: a file of this repository by its path from the root (such
// as "pod/files/pod-user.sh"), or any other by its absolute path.
func (d *Daemon) ImageWith(t *testing.T, programs, files map[string]string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	root, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("the repository's root: %v", err)
	}
	dir := strings.TrimSpace(string(root))
	var layer bytes.Buffer
	w := tar.NewWriter(&layer)
	put := func(at, file string) {
		raw, err := os.ReadFile(file)
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
	for at, pkg := range programs {
		bin := filepath.Join(t.TempDir(), "program")
		build := exec.CommandContext(ctx, "go", "build", "-o", bin, pkg)
		build.Dir = dir
		build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+d.Arch, "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build of %s: %v\n%s", pkg, err, out)
		}
		put(at, bin)
	}
	for at, file := range files {
		if !filepath.IsAbs(file) {
			file = filepath.Join(dir, file)
		}
		put(at, file)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	repo := "javo-test-" + testrun.ID
	tag := strings.ToLower(rand.Text())
	if err := d.commit(ctx, layer.Bytes(), repo, tag); err != nil {
		t.Fatal(err)
	}
	return repo + ":" + tag
}

// commit makes the image repo:tag from Busybox and the files of a tar
// stream.
func (d *Daemon) commit(ctx context.Context, files []byte, repo, tag string) error {
	var made struct {
		ID string `json:"Id"`
	}
	if err := d.Call(ctx, http.MethodPost, "/containers/create", nil, map[string]any{"Image": Busybox, "Labels": Stamp()}, &made); err != nil {
		return err
	}
	defer func() {
		_ = d.Call(ctx, http.MethodDelete, "/containers/"+made.ID, url.Values{"force": {"1"}}, nil, nil)
	}()
	if err := d.Client.Extract(ctx, made.ID, "/", bytes.NewReader(files)); err != nil {
		return err
	}
	// The container's labels become the image's.
	return d.Call(ctx, http.MethodPost, "/commit", url.Values{"container": {made.ID}, "repo": {repo}, "tag": {tag}}, nil, nil)
}

// Package dockertest plays the part of a Docker daemon that storage needs,
// in memory, for the unit tests of packages that hold a docker.Client:
// volumes, and the container that is created, written to and never
// started when a volume is handed to its owner (docker.Own).
package dockertest

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/nebari-dev/javo/internal/executor/docker"
)

// Volume is one volume of the fake daemon.
type Volume struct {
	Labels map[string]string
	// UID, GID and Mode are those of the volume's root; a fresh volume
	// belongs to root, as a real one does.
	UID, GID int
	Mode     int64
	// Handed counts how many times the root was given an owner.
	Handed int
}

// Daemon is a fake Docker daemon.
type Daemon struct {
	mu      sync.Mutex
	volumes map[string]*Volume
	// containers are the volume each container mounts, by mount path.
	containers map[string]map[string]string
	// Down makes every call fail, as a daemon that is not there.
	down bool
}

// New starts a fake daemon and returns it with a client of it.
func New(t *testing.T) (*Daemon, *docker.Client) {
	t.Helper()
	// A socket path is at most about 100 bytes: t.TempDir is too deep.
	dir, err := os.MkdirTemp("", "jd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{volumes: map[string]*Volume{}, containers: map[string]map[string]string{}}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(d.serve))
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return d, docker.New(sock)
}

// Volume is a copy of the volume called name; false when there is none.
func (d *Daemon) Volume(name string) (Volume, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.volumes[name]
	if !ok {
		return Volume{}, false
	}
	return *v, true
}

// Names are the names of every volume.
func (d *Daemon) Names() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for name := range d.volumes {
		out = append(out, name)
	}
	return out
}

// Put plants a volume with labels, as someone else would have made it.
func (d *Daemon) Put(name string, labels map[string]string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.volumes[name] = &Volume{Labels: labels, Mode: 0o755}
}

// Remove takes the volume called name away behind Javo's back.
func (d *Daemon) Remove(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.volumes, name)
}

// Down makes every call fail from now on (or work again).
func (d *Daemon) Down(down bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.down = down
}

var version = regexp.MustCompile(`^/v[0-9.]+`)

func (d *Daemon) serve(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.down {
		fail(w, http.StatusInternalServerError, "the daemon is down")
		return
	}
	path := version.ReplaceAllString(r.URL.Path, "")
	rest := func(prefix string) (string, bool) { return strings.CutPrefix(path, prefix) }
	switch {
	case r.Method == http.MethodPost && path == "/volumes/create":
		var body struct {
			Name   string
			Labels map[string]string
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		// As the daemon does: a volume of that name is answered as it is.
		if d.volumes[body.Name] == nil {
			d.volumes[body.Name] = &Volume{Labels: body.Labels, Mode: 0o755}
		}
		d.volume(w, body.Name)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/volumes/"):
		d.volume(w, strings.TrimPrefix(path, "/volumes/"))
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/images/"):
		reply(w, http.StatusOK, map[string]string{"Id": "sha256:fake"})
	case r.Method == http.MethodPost && path == "/containers/create":
		var body struct {
			HostConfig struct {
				Mounts []struct{ Source, Target string }
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		mounts := map[string]string{}
		for _, m := range body.HostConfig.Mounts {
			mounts[strings.Trim(m.Target, "/")] = m.Source
		}
		name := r.URL.Query().Get("name")
		d.containers[name] = mounts
		reply(w, http.StatusCreated, map[string]string{"Id": name})
	case r.Method == http.MethodPut && strings.HasSuffix(path, "/archive"):
		id, _ := rest("/containers/")
		if err := d.extract(strings.TrimSuffix(id, "/archive"), r.Body); err != nil {
			fail(w, http.StatusNotFound, err.Error())
			return
		}
		reply(w, http.StatusOK, struct{}{})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/json"):
		id, _ := rest("/containers/")
		id = strings.TrimSuffix(id, "/json")
		if d.containers[id] == nil {
			fail(w, http.StatusNotFound, "no such container")
			return
		}
		reply(w, http.StatusOK, map[string]any{"Id": id, "Config": map[string]any{"Labels": map[string]string{docker.LabelManaged: "true"}}})
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
		delete(d.containers, strings.TrimPrefix(path, "/containers/"))
		w.WriteHeader(http.StatusNoContent)
	default:
		fail(w, http.StatusNotImplemented, "the fake daemon does not play "+r.Method+" "+path)
	}
}

func (d *Daemon) volume(w http.ResponseWriter, name string) {
	v := d.volumes[name]
	if v == nil {
		fail(w, http.StatusNotFound, "no such volume")
		return
	}
	reply(w, http.StatusOK, map[string]any{"Name": name, "Driver": "local", "Labels": v.Labels})
}

// extract gives the volumes the container id mounts the owner and mode of
// each folder entry of the tar stream that names a mount.
func (d *Daemon) extract(id string, body io.Reader) error {
	mounts := d.containers[id]
	if mounts == nil {
		return errors.New("no such container")
	}
	for r := tar.NewReader(body); ; {
		h, err := r.Next()
		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		if v := d.volumes[mounts[strings.Trim(h.Name, "/")]]; v != nil && h.Typeflag == tar.TypeDir {
			v.UID, v.GID, v.Mode = h.Uid, h.Gid, h.Mode
			v.Handed++
		}
	}
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"message": msg})
}

package docker

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/workload"
)

// host is a fake daemon for the volume and secret-file steps. It records
// every call, the bodies of created containers and volumes, and what was
// unpacked where ("<path>: <name> <uid>:<gid> <mode> <content>").
type host struct {
	calls
	created  []Body
	volumes  []map[string]any
	unpacked []string
	// exists are the paths a container is said to hold; sandboxRuns is
	// whether the workload's sandbox runs; foreign names things that do
	// not carry Javo's label.
	exists      map[string]bool
	sandboxRuns bool
	foreign     map[string]bool
}

func (h *host) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.add(r)
	p := strings.TrimPrefix(r.URL.Path, "/v"+MinAPI)
	labels := func(name string) map[string]string {
		if h.foreign[name] {
			return map[string]string{}
		}
		return map[string]string{LabelManaged: "true"}
	}
	switch {
	case r.Method == "POST" && p == "/containers/create":
		var b Body
		json.NewDecoder(r.Body).Decode(&b)
		h.created = append(h.created, b)
		reply(w, 201, map[string]string{"Id": "new"})
	case r.Method == "PUT" && strings.HasSuffix(p, "/archive"):
		tr := tar.NewReader(r.Body)
		for {
			hdr, err := tr.Next()
			if err != nil {
				break
			}
			content, _ := io.ReadAll(tr)
			h.unpacked = append(h.unpacked, fmt.Sprintf("%s: %s %d:%d %o %s", r.URL.Query().Get("path"), hdr.Name, hdr.Uid, hdr.Gid, hdr.Mode, content))
		}
	case r.Method == "HEAD" && strings.HasSuffix(p, "/archive"):
		if !h.exists[r.URL.Query().Get("path")] {
			w.WriteHeader(404)
		}
	case r.Method == "GET" && p == "/containers/json":
		reply(w, 200, []map[string]any{
			{"Id": "sandbox-id", "Labels": map[string]string{LabelUnit: Sandbox}},
			{"Id": "app-id", "Labels": map[string]string{LabelUnit: "app"}},
		})
	case r.Method == "GET" && strings.HasPrefix(p, "/containers/"):
		name := strings.Split(p, "/")[2]
		if name == "javo-javo-alice.notebook.setup" {
			fail(w, 404, "no such container")
			return
		}
		reply(w, 200, map[string]any{"Id": name, "State": map[string]any{"Running": h.sandboxRuns}, "Config": map[string]any{"Labels": labels(name)}})
	case r.Method == "POST" && p == "/volumes/create":
		var v map[string]any
		json.NewDecoder(r.Body).Decode(&v)
		h.volumes = append(h.volumes, v)
		reply(w, 201, v)
	case r.Method == "GET" && p == "/volumes":
		reply(w, 200, map[string]any{"Volumes": []map[string]any{{"Name": "javo-javo-alice.notebook.scratch"}}})
	case r.Method == "GET" && strings.HasPrefix(p, "/volumes/"):
		name := strings.TrimPrefix(p, "/volumes/")
		reply(w, 200, map[string]any{"Name": name, "Labels": labels(name)})
	case r.Method == "GET" && strings.HasPrefix(p, "/networks/"):
		fail(w, 404, "no such network")
	default:
		w.WriteHeader(204)
	}
}

func (h *host) did(prefix string) []string {
	var out []string
	for _, c := range h.all() {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func TestOwnGivesAVolumeToTheUserWithoutRunningAnything(t *testing.T) {
	h := &host{}
	c := fake(t, h.ServeHTTP)
	if err := Own(context.Background(), c, spec(), "registry.example.com/javo/javo:1", "home-alice", 0o750); err != nil {
		t.Fatal(err)
	}
	if len(h.created) != 1 {
		t.Fatalf("created %d containers", len(h.created))
	}
	b := h.created[0]
	if b.User != "1000:100" || b.HostConfig.NetworkMode != "none" || len(b.HostConfig.CapDrop) != 1 || b.Labels[LabelUnit] != Setup {
		t.Errorf("setup container = %+v", b)
	}
	if len(b.HostConfig.Mounts) != 1 || b.HostConfig.Mounts[0].Source != "home-alice" || b.HostConfig.Mounts[0].Target != "/v" {
		t.Errorf("mounts = %+v", b.HostConfig.Mounts)
	}
	if got := strings.Join(h.unpacked, "|"); got != "/: v/ 1000:100 750 " {
		t.Errorf("unpacked %q", got)
	}
	if started := h.did("POST /containers/new/start"); len(started) != 0 {
		t.Error("the setup container was started")
	}
	if removed := h.did("DELETE /containers/new"); len(removed) != 1 {
		t.Errorf("the setup container was removed %d times", len(removed))
	}
}

func TestPrepareMakesTheWorkloadsVolumesAndMissingFolders(t *testing.T) {
	h := &host{exists: map[string]bool{"/v/data": true}}
	c := fake(t, h.ServeHTTP)
	s := spec()
	s.Main.Mounts[1].ReadOnly = false // /data shows the folder data/in of the home
	s.Steps = []workload.Unit{{Name: "clone", Mounts: []workload.Mount{
		{Volume: "home", Path: "/src", SubPath: "data/in"},
		{Volume: "shared", Path: "/shared", SubPath: "never/made"}, // a read-only volume
	}}}
	if err := Prepare(context.Background(), c, s, "registry.example.com/javo/javo:1"); err != nil {
		t.Fatal(err)
	}
	if len(h.volumes) != 2 {
		t.Fatalf("volumes = %v", h.volumes)
	}
	scratch, nebi := h.volumes[0], h.volumes[1]
	if scratch["Name"] != "javo-javo-alice.notebook.scratch" || scratch["DriverOpts"] != nil || scratch["Labels"].(map[string]any)[LabelEnds] != "true" {
		t.Errorf("scratch = %v", scratch)
	}
	opts, _ := nebi["DriverOpts"].(map[string]any)
	if nebi["Name"] != "javo-javo-alice.notebook.nebi" || opts["type"] != "tmpfs" || opts["o"] != "size=1m,mode=0700,uid=1000,gid=100,noexec,nosuid,nodev" {
		t.Errorf("nebi = %v", nebi)
	}
	// The home's folder data exists and is left as it is; in is made
	// once. The scratch folder's root is given to the user.
	want := "/: v/data/in/ 1000:100 755 |/: v/ 1000:100 700 "
	if got := strings.Join(h.unpacked, "|"); got != want {
		t.Errorf("unpacked %q, want %q", got, want)
	}
}

func TestPrepareRefusesStorageThatIsNotJavos(t *testing.T) {
	h := &host{foreign: map[string]bool{"home-alice": true}}
	c := fake(t, h.ServeHTTP)
	if err := Prepare(context.Background(), c, spec(), "registry.example.com/javo/javo:1"); !errors.Is(err, ErrForeign) {
		t.Fatalf("err = %v", err)
	}
}

func TestSecretFilesAreUnpackedIntoTheRunningSandbox(t *testing.T) {
	s := spec()
	nebi := s.Volumes[3]
	files := map[string][]byte{"token": []byte("s3cr3t"), "url": []byte("https://nebi.example.com")}
	ctx := context.Background()

	h := &host{sandboxRuns: true}
	c := fake(t, h.ServeHTTP)
	if err := SecretFiles(ctx, c, s, nebi, files); err != nil {
		t.Fatal(err)
	}
	want := "/run/javo/secrets/nebi: token 1000:100 400 s3cr3t|/run/javo/secrets/nebi: url 1000:100 400 https://nebi.example.com"
	if got := strings.Join(h.unpacked, "|"); got != want {
		t.Errorf("unpacked %q", got)
	}

	// Only the keys the volume names, readable by all when it says so.
	h.unpacked = nil
	nebi.Secret = &workload.SecretFiles{Bundle: "notebook-nebi", Keys: []string{"url"}, WorldReadable: true}
	if err := SecretFiles(ctx, c, s, nebi, files); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.unpacked, "|"); got != "/run/javo/secrets/nebi: url 1000:100 444 https://nebi.example.com" {
		t.Errorf("unpacked %q", got)
	}

	for name, tc := range map[string]struct {
		volume workload.Volume
		files  map[string][]byte
	}{
		"a key the bundle lacks": {workload.Volume{Name: "nebi", Secret: &workload.SecretFiles{Keys: []string{"nope"}}}, files},
		"no bundle":              {workload.Volume{Name: "nebi", Secret: &workload.SecretFiles{}}, nil},
		"a key that is a path":   {workload.Volume{Name: "nebi", Secret: &workload.SecretFiles{}}, map[string][]byte{"../x": nil}},
		"not a secret volume":    {workload.Volume{Name: "scratch"}, files},
	} {
		h.unpacked = nil
		if err := SecretFiles(ctx, c, s, tc.volume, tc.files); err == nil || len(h.unpacked) != 0 {
			t.Errorf("%s: err %v, unpacked %v", name, err, h.unpacked)
		}
	}
	// A bundle that may be missing leaves the volume empty.
	optional := workload.Volume{Name: "nebi", Secret: &workload.SecretFiles{Optional: true}}
	if err := SecretFiles(ctx, c, s, optional, nil); err != nil || len(h.unpacked) != 0 {
		t.Errorf("optional: err %v, unpacked %v", err, h.unpacked)
	}
}

// Into a stopped sandbox the daemon would write to memory it frees at
// once, and say it worked.
func TestSecretFilesNeedARunningSandboxOfJavos(t *testing.T) {
	s := spec()
	files := map[string][]byte{"token": []byte("s3cr3t")}
	h := &host{sandboxRuns: false}
	c := fake(t, h.ServeHTTP)
	if err := SecretFiles(context.Background(), c, s, s.Volumes[3], files); err == nil || len(h.unpacked) != 0 {
		t.Errorf("stopped sandbox: err %v, unpacked %v", err, h.unpacked)
	}
	h = &host{sandboxRuns: true, foreign: map[string]bool{"javo-javo-alice.notebook": true}}
	c = fake(t, h.ServeHTTP)
	if err := SecretFiles(context.Background(), c, s, s.Volumes[3], files); !errors.Is(err, ErrForeign) || len(h.unpacked) != 0 {
		t.Errorf("foreign sandbox: err %v, unpacked %v", err, h.unpacked)
	}
}

func TestTeardownRemovesUnitsThenTheSandboxThenItsVolumes(t *testing.T) {
	h := &host{}
	c := fake(t, h.ServeHTTP)
	if err := Teardown(context.Background(), c, ref, "javo-gateway"); err != nil {
		t.Fatal(err)
	}
	want := "DELETE /containers/app-id?force=1&v=1|DELETE /containers/sandbox-id?force=1&v=1|DELETE /volumes/javo-javo-alice.notebook.scratch"
	if got := strings.Join(h.did("DELETE"), "|"); got != want {
		t.Errorf("deletes = %q", got)
	}
	if lists := h.did("GET /volumes?"); len(lists) != 1 || !strings.Contains(lists[0], "javo.ends-with-workload%3Dtrue") {
		t.Errorf("volume list = %v", lists)
	}
}

// Whatever a list returns, a thing without Javo's label stays.
func TestTeardownLeavesWhatIsNotJavos(t *testing.T) {
	h := &host{foreign: map[string]bool{"app-id": true, "javo-javo-alice.notebook.scratch": true}}
	c := fake(t, h.ServeHTTP)
	err := Teardown(context.Background(), c, ref, "javo-gateway")
	if !errors.Is(err, ErrForeign) {
		t.Fatalf("err = %v", err)
	}
	if got := strings.Join(h.did("DELETE"), "|"); got != "DELETE /containers/sandbox-id?force=1&v=1" {
		t.Errorf("deletes = %q", got)
	}
}

func TestAVolumeOfThatNameThatIsNotJavosIsRefused(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, 201, map[string]any{"Name": "home-alice", "Labels": map[string]string{"com.example.stack": "db"}})
	})
	if err := c.VolumeCreate(context.Background(), "home-alice", Labels(ref)); !errors.Is(err, ErrForeign) {
		t.Fatalf("err = %v", err)
	}
}

//go:build linux || darwin

package wiring

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/status"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// processConfig is a config for apps as processes whose folders are in a
// folder of the test.
func processConfig(t *testing.T) config.Platform {
	t.Helper()
	dir := t.TempDir()
	return config.Platform{
		Executor: config.ExecutorProcess, KeysDir: filepath.Join(dir, "keys"),
		Process: config.ProcessCfg{StateDir: filepath.Join(dir, "state"), HomesDir: filepath.Join(dir, "homes")},
	}
}

func hostInputs(t *testing.T) HostInputs {
	return HostInputs{
		DB: storetest.New(t), Namespace: "javo-apps", Listen: "127.0.0.1:8080", RunnerImage: "example.com/javo:1",
		FilesImage: "example.com/rclone:1",
	}
}

// wantRefused builds the host and fails unless it is refused in words
// that hold every one of want.
func wantRefused(t *testing.T, cfg config.Platform, in HostInputs, want ...string) {
	t.Helper()
	h, err := NewHost(context.Background(), cfg, in)
	if err == nil {
		h.Close()
		t.Fatalf("not refused; want an error with %q", want)
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("refused with %q, which lacks %q", err, w)
		}
	}
}

// On Kubernetes there is no host to build: the cluster runs the apps.
func TestKubernetesHasNoHost(t *testing.T) {
	h, err := NewHost(context.Background(), config.Platform{Executor: config.ExecutorKubernetes}, HostInputs{})
	if h != nil || err != nil {
		t.Fatalf("NewHost = %v, %v", h, err)
	}
}

// A machine that runs apps as processes gets its folders and keys made on
// the first start, and the same keys are read on the next.
func TestAProcessHostMakesItsFoldersAndKeepsItsKeys(t *testing.T) {
	cfg, in := processConfig(t), hostInputs(t)
	first, err := NewHost(context.Background(), cfg, in)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := first.SessionsKey.Encrypt([]byte("token"), []byte("ad"))
	first.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{cfg.KeysDir, cfg.Process.StateDir, cfg.Process.HomesDir} {
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v, %v; want a folder of mode 0700", dir, fi, err)
		}
	}
	second, err := NewHost(context.Background(), cfg, in)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if plain, err := second.SessionsKey.Decrypt(sealed, []byte("ad")); err != nil || string(plain) != "token" {
		t.Fatalf("the next start does not open what the first sealed: %v", err)
	}
	if second.Runs == nil || second.Files == nil || second.Setup == nil || second.PlanFor == nil {
		t.Fatalf("a process host: runs %v, files %v", second.Runs, second.Files)
	}
}

// As processes of one person's machine an app with no login of its own
// starts: the gate that holds such an app back is answered there without
// a probe.
func TestAProcessHostLetsAppsWithNoLoginStart(t *testing.T) {
	h, err := NewHost(context.Background(), processConfig(t), hostInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	res, err := h.NetworkPolicy().Get(context.Background())
	if err != nil || status.LoginlessBlocked("Disabled", res.State) {
		t.Fatalf("an app with no login of its own is held back: %+v, %v", res, err)
	}
}

// Running apps as processes isolates nothing, so Javo does not start as
// root, beyond loopback, or for more than one person.
func TestAProcessHostRefusesWhatItCannotKeepSafe(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		euid = func() int { return 0 }
		defer func() { euid = os.Geteuid }()
		wantRefused(t, processConfig(t), hostInputs(t), "root", "executor process")
	})
	t.Run("a listener beyond loopback", func(t *testing.T) {
		for _, listen := range []string{":8080", "0.0.0.0:8080", "192.0.2.10:8080", "example.com:8080"} {
			in := hostInputs(t)
			in.Listen = listen
			wantRefused(t, processConfig(t), in, "--listen", "loopback", "executor process")
		}
	})
	t.Run("a second person", func(t *testing.T) {
		in := hostInputs(t)
		for _, name := range []string{"alice", "bob"} {
			if _, err := in.DB.People(store.IDRange{}).Ensure(context.Background(), store.Claims{Issuer: "local", Subject: name, Username: name}); err != nil {
				t.Fatal(err)
			}
		}
		wantRefused(t, processConfig(t), in, "2 people", "executor process")
	})
}

// With the coding agents' lock on (workloads.landlock), a machine that
// runs apps as processes must be able to lock them in: Landlock is
// Linux's, so on a Mac Javo does not start rather than run the agents with
// every file of the person's in reach; on Linux it starts once the
// javo-sandbox of the platform's files has locked a command in.
func TestAProcessHostRefusesTheAgentLockItCannotApply(t *testing.T) {
	defer func(was string) { goos = was }(goos)
	locked := func(t *testing.T, sandbox string) config.Platform {
		cfg := processConfig(t)
		cfg.Workloads.Landlock = true
		if sandbox != "" {
			cfg.Process.FilesDir = t.TempDir()
			bin := filepath.Join(cfg.Process.FilesDir, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "javo-sandbox"), []byte(sandbox), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return cfg
	}
	t.Run("a Mac", func(t *testing.T) {
		goos = "darwin"
		wantRefused(t, locked(t, ""), hostInputs(t), "workloads.landlock", "Linux", "darwin", "executor process")
	})
	t.Run("Linux without the platform's files", func(t *testing.T) {
		goos = "linux"
		wantRefused(t, locked(t, ""), hostInputs(t), "workloads.landlock", "process.filesDir")
	})
	t.Run("Linux whose kernel has no Landlock", func(t *testing.T) {
		goos = "linux"
		cfg := locked(t, "#!/bin/sh\necho 'javo-sandbox: this kernel has no Landlock' >&2\nexit 125\n")
		wantRefused(t, cfg, hostInputs(t), "workloads.landlock", "this kernel has no Landlock")
	})
	t.Run("Linux with Landlock", func(t *testing.T) {
		goos = "linux"
		// A stand-in that locks nothing in and runs the command it is given.
		cfg := locked(t, "#!/bin/sh\nwhile [ \"$1\" != -- ]; do shift; done\nshift\nexec \"$@\"\n")
		h, err := NewHost(context.Background(), cfg, hostInputs(t))
		if err != nil {
			t.Fatal(err)
		}
		h.Close()
	})
	t.Run("the lock off", func(t *testing.T) {
		goos = "darwin"
		h, err := NewHost(context.Background(), processConfig(t), hostInputs(t))
		if err != nil {
			t.Fatal(err)
		}
		h.Close()
	})
}

// The folders Javo keeps homes, state and keys in must be its user's
// alone: one that others can open is refused, not repaired.
func TestAHostRefusesFoldersOpenToOthers(t *testing.T) {
	for name, pick := range map[string]func(config.Platform) string{
		"homes": func(c config.Platform) string { return c.Process.HomesDir },
		"state": func(c config.Platform) string { return c.Process.StateDir },
		"keys":  func(c config.Platform) string { return c.KeysDir },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := processConfig(t)
			dir := pick(cfg)
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			wantRefused(t, cfg, hostInputs(t), dir)
		})
	}
	t.Run("a key file", func(t *testing.T) {
		cfg := processConfig(t)
		if err := os.Mkdir(cfg.KeysDir, 0o700); err != nil {
			t.Fatal(err)
		}
		// A key of the test's own making, readable by everyone.
		if err := os.WriteFile(filepath.Join(cfg.KeysDir, secretsKeyFile), make([]byte, 32), 0o644); err != nil {
			t.Fatal(err)
		}
		wantRefused(t, cfg, hostInputs(t), secretsKeyFile, "0644")
	})
}

// fakeDocker is a Docker daemon as far as a start asks one: its version,
// what it has, and whether a container exists.
type fakeDocker struct {
	api      string
	runtimes map[string]any
	gateway  string
	// hostname is the gateway container's.
	hostname string
}

// inside makes Javo take itself to run on a machine called name, until
// the test ends.
func inside(t *testing.T, name string) {
	t.Helper()
	before := hostname
	hostname = func() (string, error) { return name, nil }
	t.Cleanup(func() { hostname = before })
}

// socket serves d on a unix socket and returns its path.
func (d fakeDocker) socket(t *testing.T) string {
	t.Helper()
	// Short: a unix socket's path has little room.
	dir, err := os.MkdirTemp("", "wiring-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "docker.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch p := r.URL.Path; {
		case p == "/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"Version": "test", "ApiVersion": d.api})
		case strings.HasSuffix(p, "/info"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Runtimes": d.runtimes, "CgroupVersion": "2"})
		case strings.HasSuffix(p, "/containers/"+d.gateway+"/json") && d.gateway != "":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "gw", "Config": map[string]string{"Hostname": d.hostname}})
		case strings.HasSuffix(p, "/json") && strings.Contains(p, "/containers/") && !strings.HasSuffix(p, "/containers/json"):
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "No such container"})
		default:
			_, _ = w.Write([]byte("[]"))
		}
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return path
}

func dockerConfig(t *testing.T, socket string) config.Platform {
	return config.Platform{
		Executor: config.ExecutorDocker, KeysDir: filepath.Join(t.TempDir(), "keys"),
		Docker: config.DockerCfg{Socket: socket, Gateway: "javo"},
	}
}

// Javo does not start on a Docker daemon it cannot run apps safely on:
// one that does not answer or is too old, one without the runtime the
// admin asked for, or one that does not have Javo's own container.
func TestADockerHostRefusesADaemonItCannotUse(t *testing.T) {
	good := fakeDocker{api: "1.53", runtimes: map[string]any{"runc": nil}, gateway: "javo", hostname: "javo-1"}
	inside(t, "javo-1")
	t.Run("no daemon", func(t *testing.T) {
		wantRefused(t, dockerConfig(t, filepath.Join(t.TempDir(), "none.sock")), hostInputs(t), "docker executor")
	})
	t.Run("too old", func(t *testing.T) {
		old := good
		old.api = "1.43"
		wantRefused(t, dockerConfig(t, old.socket(t)), hostInputs(t), "too old", "1.43")
	})
	t.Run("no such runtime", func(t *testing.T) {
		cfg := dockerConfig(t, good.socket(t))
		cfg.Docker.Runtime = "runsc"
		wantRefused(t, cfg, hostInputs(t), `no runtime "runsc"`)
	})
	t.Run("no gateway container", func(t *testing.T) {
		gone := good
		gone.gateway = ""
		wantRefused(t, dockerConfig(t, gone.socket(t)), hostInputs(t), `gateway container "javo"`)
	})
	t.Run("a Javo outside its gateway container", func(t *testing.T) {
		// Its readiness checks, the file helpers and the proxy all need
		// Javo on the workloads' networks, where only that container is.
		inside(t, "a-laptop")
		wantRefused(t, dockerConfig(t, good.socket(t)), hostInputs(t), `docker.gateway "javo"`, "a-laptop", "inside the container")
	})
	t.Run("a daemon it can use", func(t *testing.T) {
		in := hostInputs(t)
		// What a Javo on a cluster, or on another daemon, measured there.
		stale := &netprobe.Store{Facts: in.DB.Facts()}
		if err := stale.Put(context.Background(), netprobe.Result{State: netprobe.Enforced}); err != nil {
			t.Fatal(err)
		}
		h, err := NewHost(context.Background(), dockerConfig(t, good.socket(t)), in)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		if h.Runs == nil || h.Files == nil || h.Setup == nil || h.PlanFor != nil {
			t.Fatalf("a docker host: %+v", h)
		}
		// Until this Javo's own probe has measured it, an app with no
		// login of its own does not start.
		res, err := h.NetworkPolicy().Get(context.Background())
		if err != nil || !status.LoginlessBlocked("Disabled", res.State) {
			t.Fatalf("an app with no login of its own starts unprobed: %+v, %v", res, err)
		}
	})
}

// What one Javo makes on a daemon in one scope is never found in
// another: the labels a scope's client puts on a thing do not match the
// filter of any other scope's client, nor that of another Javo's.
func TestTheScopesOfADockerHostAreApart(t *testing.T) {
	scopes := []string{scopeWorkloads, scopeFiles, scopeHomes, scopeProbe}
	type client struct{ scope, gateway string }
	var all []client
	for _, gw := range []string{"javo", "javo-2"} {
		for _, s := range scopes {
			all = append(all, client{s, gw})
		}
	}
	for _, maker := range all {
		made := scoped("/none.sock", maker.scope, maker.gateway).Labels
		for _, finder := range all {
			found := true
			for k, v := range scoped("/none.sock", finder.scope, finder.gateway).Labels {
				found = found && made[k] == v
			}
			if len(scoped("/none.sock", finder.scope, finder.gateway).Labels) == 0 {
				t.Fatalf("%+v has no label: it would find everything", finder)
			}
			if found != (maker == finder) {
				t.Errorf("what %+v makes: found by %+v = %v", maker, finder, found)
			}
		}
	}
}

// Apps as processes all run as one user, so the first person to sign in
// on such a machine stays the only one: a second is turned away at
// sign-in, and the first signs in again as before.
func TestAProcessHostTurnsASecondPersonAway(t *testing.T) {
	ctx := context.Background()
	in := hostInputs(t)
	h, err := NewHost(ctx, processConfig(t), in)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	people := h.People(in.DB.People(store.IDRange{}))
	alice := store.Claims{Issuer: "local", Subject: "alice", Username: "alice"}
	for range 2 {
		if _, err := people.Ensure(ctx, alice); err != nil {
			t.Fatal(err)
		}
	}
	_, err = people.Ensure(ctx, store.Claims{Issuer: "local", Subject: "bob", Username: "bob"})
	if err == nil || !strings.Contains(err.Error(), "one person") {
		t.Fatalf("a second person: %v", err)
	}
	if ids, _ := in.DB.People(store.IDRange{}).IDs(ctx); len(ids) != 1 {
		t.Fatalf("people in the database: %v", ids)
	}
}

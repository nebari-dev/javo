//go:build linux || darwin

package process

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/executortest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/templates"
	"github.com/nebari-dev/javo/internal/workload"
)

// pixiPlatforms is what pixi calls the machines this executor runs on.
var pixiPlatforms = map[string]string{
	"darwin/arm64": "osx-arm64", "darwin/amd64": "osx-64", "linux/amd64": "linux-64", "linux/arm64": "linux-aarch64",
}

// A shipped template, for real: the custom template is planned for this
// machine, its environment installed by the install step with the real
// pixi and Javo's own sha256sum and flock, its command started by
// pod-user.sh, and the app answers on loopback until it is stopped, which
// leaves nothing behind.
//
// The environments the templates bundle are locked for Linux only, so the
// one installed here is picked for the app, as one from Nebi is: python
// alone, locked for this machine by the test. It needs pixi on PATH and
// the network (pixi fetches the packages); it is skipped with -short.
func TestAShippedTemplateRunsAsProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("installs a pixi environment")
	}
	pixi, err := exec.LookPath("pixi")
	if err != nil {
		t.Skip("no pixi on PATH")
	}
	platform := pixiPlatforms[runtime.GOOS+"/"+runtime.GOARCH]
	if platform == "" {
		t.Skipf("no pixi platform for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	run := func(dir string, env []string, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir, cmd.Env = dir, append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %q: %v\n%s", name, args, err, out)
		}
	}

	// The Javo binary the platform's steps and tools run.
	javo := filepath.Join(tmp, "javo")
	run(repo, nil, "go", "build", "-o", javo, "./cmd/javo")

	// The picked environment, locked for this machine.
	lockDir, home := filepath.Join(tmp, "lock"), filepath.Join(tmp, "home")
	for _, d := range []string{lockDir, home} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manifest := "[workspace]\nname = \"e2e\"\nchannels = [\"conda-forge\"]\nplatforms = [\"" + platform + "\"]\n\n[dependencies]\npython = \"3.13.*\"\n"
	if err := os.WriteFile(filepath.Join(lockDir, "pixi.toml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	run(lockDir, []string{"HOME=" + home, "PIXI_CACHE_DIR=" + filepath.Join(home, ".javo", "pixi-cache")}, pixi, "lock")
	lock, err := os.ReadFile(filepath.Join(lockDir, "pixi.lock"))
	if err != nil {
		t.Fatal(err)
	}

	const runner = "example.com/javo:1"
	x, err := New(Options{
		Dir: filepath.Join(tmp, "state"), Secrets: &executortest.Secrets{}, RunnerImage: runner,
		Pixi: pixi, Files: filepath.Join(repo, "pod", "files"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.Close)
	x.eng.self = javo

	raw, err := os.ReadFile(filepath.Join(repo, "deploy", "helm", "javo", "files", "templates", "custom.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := templates.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	tpl.Spec.ApplyDefaults()
	app := &model.App{
		ID: "app-1", Namespace: "alice", Name: "demo", Owner: "alice", Template: tpl.Name,
		Command:     "python -m http.server {{.Port}} --bind 0.0.0.0",
		Environment: &model.Environment{Source: "nebi", ID: "e-1", Name: "e2e"},
	}
	ref := workspace.RefOf(app)
	addr, err := x.Addr(t.Context(), ref, tpl.Spec.Port)
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(port)
	host := x.Host(ref)
	spec, err := workspace.Plan(workspace.Inputs{
		App: app, Template: &tpl, BaseURL: "/",
		Layout: podstorage.Build(app.Owner, podstorage.Input{Home: &model.Home{Status: model.HomeStatus{ClaimName: home}}}),
		Images: workspace.Images{Runner: podspec.Image{Ref: runner}},
		Port:   int32(n), Host: &host,
		EnvFiles: &model.EnvironmentFiles{Manifest: manifest, Lock: string(lock)},
	})
	if err != nil {
		t.Fatal(err)
	}

	changed := make(chan struct{}, 1)
	watching, stopWatch := context.WithCancel(t.Context())
	defer stopWatch()
	go func() {
		_ = x.Watch(watching, func(workload.Ref) {
			select {
			case changed <- struct{}{}:
			default:
			}
		})
	}()
	if err := x.Start(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	logs := func() string {
		var all string
		for _, u := range append(spec.Steps, spec.Main) {
			if rc, err := x.Logs(t.Context(), ref, executor.LogOptions{Unit: u.Name}); err == nil {
				b, _ := io.ReadAll(rc)
				_ = rc.Close()
				all += "== " + u.Name + "\n" + string(b)
			}
		}
		return all
	}
	deadline := time.After(15 * time.Minute)
	for ready := false; !ready; {
		st, err := x.Observe(t.Context(), ref)
		switch {
		case err != nil:
			t.Fatal(err)
		case st.Phase == "Failed":
			t.Fatalf("the app failed: %s %q\n%s", st.Code, st.Evidence, logs())
		case st.Phase == "Running":
			ready = true
			continue
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("the app is not ready: %+v\n%s", st, logs())
		}
	}

	// The typed command asked for every address; it was told loopback, or
	// the app would have been killed. The install went through the script.
	if all := logs(); !strings.Contains(all, "Installing the picked environment") {
		t.Errorf("the install step did not install the picked environment:\n%s", all)
	}
	if words := slices.Concat(spec.Main.Command, spec.Main.Args); !slices.Contains(words, "127.0.0.1") {
		t.Errorf("the app runs %q", words)
	}

	resp, err := http.Get("http://" + addr + spec.Main.Ready.HTTPPath)
	if err != nil {
		t.Fatalf("%v\n%s", err, logs())
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s\n%s", spec.Main.Ready.HTTPPath, resp.Status, logs())
	}

	if err := x.Stop(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if conn, err := net.Dial("tcp", addr); err == nil {
		_ = conn.Close()
		t.Error("the app still answers after Stop")
	}
	if _, err := os.Stat(filepath.Join(x.eng.dir, "w", ref.Namespace, ref.Name)); !os.IsNotExist(err) {
		t.Errorf("the workload's folder stayed: %v", err)
	}
}

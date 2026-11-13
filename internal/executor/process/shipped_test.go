//go:build e2e && (linux || darwin)

package process

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

// machine is this machine set up to run shipped templates for real: an
// executor with the repository's pod files, the real pixi and a Javo
// binary built from this tree, and one home that every app of a test
// shares, as one person's apps do.
type machine struct {
	x                      *Executor
	repo, home, pixi, lock string
	platform               string
}

const e2eRunner = "example.com/javo:1"

// newMachine skips the test where it cannot run: with -short, without
// pixi on PATH, or on a machine pixi has no name for. It needs the
// network: pixi fetches the packages.
func newMachine(t *testing.T) *machine {
	t.Helper()
	pixi, err := exec.LookPath("pixi")
	if err != nil {
		t.Skip("no pixi on PATH")
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	m := &machine{repo: repo, home: filepath.Join(tmp, "home"), lock: filepath.Join(tmp, "lock"), pixi: pixi}
	for _, d := range []string{m.lock, m.home} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The Javo binary the platform's steps and tools run.
	javo := filepath.Join(tmp, "javo")
	m.run(t, repo, nil, "go", "build", "-o", javo, "./cmd/javo")
	m.x, err = New(Options{
		Dir: filepath.Join(tmp, "state"), Secrets: &executortest.Secrets{}, RunnerImage: e2eRunner,
		Pixi: pixi, Files: m.files(t, filepath.Join(tmp, "files")),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.x.Close)
	m.x.eng.self = javo
	if m.platform = m.x.Host(workload.Ref{}).Platform; m.platform == "" {
		t.Skip("pixi has no name for this machine")
	}
	return m
}

// files lays the platform's files out in dir as the platform's image
// holds them: the repository's pod/files, and the terminal page beside
// the agents' scripts. The image build makes that page from ttyd's own
// (Dockerfile, pod/terminal); here it is a page that only says so, which
// ttyd serves the same way.
func (m *machine) files(t *testing.T, dir string) string {
	t.Helper()
	from := filepath.Join(m.repo, "pod", "files")
	link := func(from, to string) {
		t.Helper()
		entries, err := os.ReadDir(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(to, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if err := os.Symlink(filepath.Join(from, e.Name()), filepath.Join(to, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	link(from, dir)
	agents := filepath.Join(dir, "agents")
	if err := os.Remove(agents); err != nil {
		t.Fatal(err)
	}
	link(filepath.Join(from, "agents"), agents)
	page := "<!doctype html><title>terminal</title><p>The terminal page is built with the image.</p>\n"
	if err := os.WriteFile(filepath.Join(agents, "terminal.html"), []byte(page), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func (m *machine) run(t *testing.T, dir string, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = dir, append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %q: %v\n%s", name, args, err, out)
	}
}

func (m *machine) template(t *testing.T, name string) model.Template {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(m.repo, "deploy", "helm", "javo", "files", "templates", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := templates.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	tpl.Spec.ApplyDefaults()
	return tpl
}

// served is an app that serve started and has not stopped yet.
type served struct {
	spec workload.Spec
	// addr is where it answers, and folder its folder in the state
	// directory.
	addr, folder string
}

// serve plans app for this machine, starts it, waits until it runs and
// answers its template's ready check on loopback and on no other address
// of this machine, then stops it and checks that nothing is left. It
// returns the plan and what the units logged. during, when there is one,
// looks at the app while it runs, and what it returns looks at the
// machine once the app is stopped.
func (m *machine) serve(t *testing.T, tpl model.Template, app *model.App, files *model.EnvironmentFiles, during func(*testing.T, served) (after func())) (workload.Spec, string) {
	t.Helper()
	x := m.x
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
		Layout: podstorage.Build(app.Owner, podstorage.Input{Home: &model.Home{Status: model.HomeStatus{ClaimName: m.home}}}),
		Images: workspace.Images{Runner: podspec.Image{Ref: e2eRunner}},
		Port:   int32(n), Host: &host, EnvFiles: files,
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
		for _, u := range append(slices.Clone(spec.Steps), spec.Main) {
			if rc, err := x.Logs(t.Context(), ref, executor.LogOptions{Unit: u.Name}); err == nil {
				b, _ := io.ReadAll(rc)
				_ = rc.Close()
				all += "== " + u.Name + "\n" + string(b)
			}
		}
		return all
	}
	// Whatever happens below, the app does not outlive the test.
	defer func() { _ = x.Stop(context.Background(), ref) }()
	deadline := time.After(30 * time.Minute)
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

	resp, err := http.Get("http://" + addr + spec.Main.Ready.HTTPPath)
	if err != nil {
		t.Fatalf("%v\n%s", err, logs())
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s\n%s", spec.Main.Ready.HTTPPath, resp.Status, logs())
	}
	// Loopback and nothing else: no other address of this machine takes
	// a connection on the port, and the guard, which has looked by now,
	// left the app running.
	for _, ip := range otherAddresses(t) {
		if dials(ip, n) {
			t.Errorf("the app answers on %s: it is open to the network", net.JoinHostPort(ip, port))
		}
	}
	time.Sleep(2 * guardEvery)
	if st, err := x.Observe(t.Context(), ref); err != nil || st.Phase != "Running" {
		t.Fatalf("after it answered the app is %+v (%v)\n%s", st, err, logs())
	}
	all := logs()
	folder := filepath.Join(x.eng.dir, "w", ref.Namespace, ref.Name)
	after := func() {}
	if during != nil {
		after = during(t, served{spec: spec, addr: addr, folder: folder})
	}

	if err := x.Stop(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	after()
	if conn, err := net.Dial("tcp", addr); err == nil {
		_ = conn.Close()
		t.Error("the app still answers after Stop")
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Errorf("the workload's folder stayed: %v", err)
	}
	return spec, all
}

// otherAddresses are the addresses of this machine that are not loopback.
func otherAddresses(t *testing.T) []string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			out = append(out, n.IP.String())
		}
	}
	return out
}

// sizeOf is how many bytes the files under dir take; 0 when it is not
// there.
func sizeOf(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			n += info.Size()
		}
		return nil
	})
	return n
}

// An environment picked for an app, as one from Nebi is: the custom
// template is planned for this machine with python alone, locked for it
// by the test, installed by the install step with the real pixi and
// Javo's own sha256sum and flock, its command started by pod-user.sh, and
// the app answers on loopback until it is stopped, which leaves nothing
// behind.
func TestAPickedEnvironmentRunsAsProcesses(t *testing.T) {
	m := newMachine(t)
	manifest := "[workspace]\nname = \"e2e\"\nchannels = [\"conda-forge\"]\nplatforms = [\"" + m.platform + "\"]\n\n[dependencies]\npython = \"3.13.*\"\n"
	if err := os.WriteFile(filepath.Join(m.lock, "pixi.toml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	m.run(t, m.lock, []string{"HOME=" + m.home, "PIXI_CACHE_DIR=" + filepath.Join(m.home, ".javo", "pixi-cache")}, m.pixi, "lock")
	lock, err := os.ReadFile(filepath.Join(m.lock, "pixi.lock"))
	if err != nil {
		t.Fatal(err)
	}
	tpl := m.template(t, "custom")
	app := &model.App{
		ID: "app-1", Namespace: "alice", Name: "demo", Owner: "alice", Template: tpl.Name,
		Command:     "python -m http.server {{.Port}} --bind 0.0.0.0",
		Environment: &model.Environment{Source: "nebi", ID: "e-1", Name: "e2e"},
	}
	spec, logs := m.serve(t, tpl, app, &model.EnvironmentFiles{Manifest: manifest, Lock: string(lock)}, nil)
	// The typed command asked for every address; it was told loopback, or
	// the app would have been killed. The install went through the script.
	if !strings.Contains(logs, "Installing the picked environment") {
		t.Errorf("the install step did not install the picked environment:\n%s", logs)
	}
	if words := slices.Concat(spec.Main.Command, spec.Main.Args); !slices.Contains(words, "127.0.0.1") {
		t.Errorf("the app runs %q", words)
	}
}

// shippedOnAHost are the shipped templates that run as processes, each
// started the way New app starts it: from its sample where it has one.
var shippedOnAHost = []string{
	"streamlit", "jupyterlab", "terminal", "claude-code", "pi", "herdr",
	"custom", "backend-node", "backend-go",
	"bokeh", "dash", "fastapi", "gradio", "marimo", "panel", "voila", "vscode", "whybook",
	"pluto",
}

// notYetOnAHost are the templates that plan for a host but are not started
// here, each with why.
var notYetOnAHost = map[string]string{}

// agents are the coding agent templates, each with the program that is
// its agent.
var agents = map[string]string{"claude-code": "claude", "pi": "pi"}

// The shipped templates, for real: each is planned for this machine,
// its own bundled environment (pod/files/envs) installed by the install
// step with the real pixi, its program started by pod-user.sh, and the
// app answers its ready check on loopback, on no other address, until it
// is stopped, which leaves nothing behind. One after the other, in one
// home, so a second app of an environment finds it installed.
//
// A template the planner refuses on this machine (an environment not
// locked for it) is skipped with the planner's reason, and so is one of
// notYetOnAHost. The terminal template answers with ttyd's page; its
// program (bash) starts when a browser opens a tab, which nothing here
// does. The coding agents and herdr are looked at closer (agents_test.go):
// a tab is opened, what the app started is found, and none of it is left
// once the app is stopped. The coding agents run a second time in a state
// directory short enough to hold tmux's socket (shallow). Each run logs
// how long it took and how much its install added to the home.
func TestShippedTemplatesRunAsProcesses(t *testing.T) {
	m := newMachine(t)
	for _, name := range shippedOnAHost {
		t.Run(name, func(t *testing.T) {
			tpl := m.template(t, name)
			if err := workspace.RunsAsProcesses(&tpl, m.platform); err != nil {
				t.Skip(err)
			}
			if why := notYetOnAHost[name]; why != "" {
				t.Skip("not started: " + why)
			}
			app := &model.App{ID: "app-" + name, Namespace: "alice", Name: name, Owner: "alice", Template: tpl.Name}
			if tpl.Spec.Sample != nil {
				// New app fills the command in from the sample, where the
				// template takes one.
				app.Sample, app.File, app.Command = true, workspace.SampleFileOf(&tpl), tpl.Spec.Sample.Command
			}
			envs, cache := filepath.Join(m.home, ".javo", "envs"), filepath.Join(m.home, ".javo", "pixi-cache")
			envsBefore, cacheBefore := sizeOf(envs), sizeOf(cache)
			start := time.Now()
			var during func(*testing.T, served) func()
			switch {
			case agents[name] != "":
				during = m.agent(t, agents[name])
			case name == "herdr":
				during = m.herdr(t)
			}
			spec, logs := m.serve(t, tpl, app, nil, during)
			t.Logf("%s: ran in %s; package cache +%d MB, installed environments +%d MB",
				name, time.Since(start).Round(time.Second), (sizeOf(cache)-cacheBefore)>>20, (sizeOf(envs)-envsBefore)>>20)
			if !strings.Contains(logs, "the template's environment") {
				t.Errorf("the install step did not use the template's environment:\n%s", logs)
			}
			for _, w := range slices.Concat(spec.Main.Command, spec.Main.Args) {
				if strings.Contains(w, "0.0.0.0") {
					t.Errorf("the app runs %q", w)
				}
			}
			if agents[name] != "" {
				// Again where tmux's socket fits in the workload's folder.
				short := m.shallow(t)
				short.serve(t, tpl, app, nil, short.agent(t, agents[name]))
			}
		})
	}
}

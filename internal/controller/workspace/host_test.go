package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/controller/secretenv"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/podfiles"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/templates"
	"github.com/nebari-dev/javo/internal/workload"
)

// A machine that runs apps as processes, with folders no container has.
const (
	hostHome  = "/Users/alice/javo/home"
	hostPort  = 49231
	hostFiles = "/Applications/Javo.app/files"
)

var testHost = workload.Host{
	Volumes: "/Users/alice/javo/state/w/alice/demo/volumes", Files: hostFiles,
	InstallPath: "/Users/alice/javo/state/bin:/usr/bin:/bin", UID: 501, GID: 20,
	Platform: "osx-arm64",
}

// hostLayout is the storage of alice on that machine: her home is a folder.
func hostLayout() podstorage.Layout {
	return podstorage.Build("alice", podstorage.Input{
		Home: &model.Home{Status: model.HomeStatus{ClaimName: hostHome}}, UID: 1000, GID: 100,
	})
}

// hostInputs plans an app of the shipped template name for testHost.
func hostInputs(t *testing.T, name string) Inputs {
	t.Helper()
	tpl := shippedTemplate(t, name)
	tpl.Spec.ApplyDefaults()
	host := testHost
	return Inputs{
		App:      &model.App{ID: "app-1", Namespace: "alice", Name: "demo", Owner: "alice", Template: tpl.Name},
		Template: tpl, BaseURL: "/u/alice/demo/", Layout: hostLayout(), Images: testImages,
		Port: hostPort, Host: &host,
	}
}

// shippedNames are the names of all shipped templates.
func shippedNames(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "deploy", "helm", "javo", "files", "templates", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no shipped templates: %v", err)
	}
	var names []string
	for _, f := range files {
		names = append(names, strings.TrimSuffix(filepath.Base(f), ".yaml"))
	}
	return names
}

// asProcesses is every shipped template and why its apps cannot run as
// processes of testHost, a Mac; "" for one whose apps can.
var asProcesses = map[string]string{
	"backend-go": "", "backend-node": "", "bokeh": "", "claude-code": "", "custom": "", "dash": "",
	"fastapi": "", "gradio": "", "herdr": "", "jupyterlab": "", "marimo": "", "panel": "", "pi": "",
	"streamlit": "", "terminal": "", "voila": "", "vscode": "", "whybook": "",

	"pluto": "locked for Linux only",

	"harf":         "needs its image",
	"rstudio":      "needs its image",
	"container":    "the image each app names",
	"static-site":  "builds",
	"backup":       "only runs jobs",
	"jobs-command": "only runs jobs",
	"jobs-julia":   "only runs jobs",
	"jobs-python":  "only runs jobs",
	"jobs-r":       "only runs jobs",
}

func TestWhichShippedTemplatesRunAsProcesses(t *testing.T) {
	t.Parallel()
	names := shippedNames(t)
	if len(names) != len(asProcesses) {
		t.Fatalf("%d shipped templates, %d expected answers: say for each new one whether it runs as processes", len(names), len(asProcesses))
	}
	for _, name := range names {
		want, listed := asProcesses[name]
		if !listed {
			t.Errorf("%s: no expected answer", name)
			continue
		}
		tpl := shippedTemplate(t, name)
		tpl.Spec.ApplyDefaults()
		err := RunsAsProcesses(tpl, testHost.Platform)
		var no *executor.UnsupportedError
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: refused: %v", name, err)
		case want != "" && (!errors.As(err, &no) || !strings.Contains(no.Why, want)):
			t.Errorf("%s: %v, want it refused because %q", name, err, want)
		}
		// The plan agrees with the answer given in advance.
		if !tpl.Spec.JobOnly {
			in := hostInputs(t, name)
			in.App.Image = "example.com/team/app:1"
			if _, err := Plan(in); (err == nil) != (want == "") {
				t.Errorf("%s: Plan: %v, want refused: %v", name, err, want != "")
			}
		}
	}
}

// containerPath finds a path of a container at the start of a word or of
// the value of a name=value word.
var containerPath = regexp.MustCompile(`(^|[=:\s"'])(/opt/javo|/home/user|/scratch|/tmp|/srv/src|/run/javo|/etc/|/var/run/javo|/shared)`)

// hostVariants are the ways an app is planned for a host in these tests.
var hostVariants = map[string]func(*Inputs){
	"plain": func(*Inputs) {},
	"file":  func(in *Inputs) { in.App.File = "work/app.py" },
	"sample": func(in *Inputs) {
		in.App.Sample, in.App.File = true, SampleFileOf(in.Template)
	},
	"command": func(in *Inputs) { in.App.Command = "python {{.Home}}/serve.py --host 0.0.0.0 --port {{.Port}}" },
	"variables, secrets and API access": func(in *Inputs) {
		in.App.Env = []model.EnvVar{{Name: "GREETING", Value: "hello"}}
		in.Secrets = secretenv.Set{Fingerprint: "abc", Bindings: []store.SealedBinding{{Env: "DB_PASSWORD"}}}
		in.APIAccess, in.HasToken, in.APIURL = model.APIAccessReadWrite, true, "http://127.0.0.1:8080"
		in.Agent = agentModel{mode: model.AgentModePlatform, model: "model-1", baseURL: "http://127.0.0.1:8080/agent"}
	},
	"picked environment": func(in *Inputs) {
		in.App.Environment = &model.Environment{Source: "nebi", ID: "e1", Name: "data science", Digest: "d"}
		in.EnvFiles = &model.EnvironmentFiles{Manifest: "[workspace]\n", Lock: "version: 6\n"}
	},
}

// Every word of a plan made for a host names the host's folders and its
// loopback address: a process sees the machine's own paths, and whatever
// listens on every address is open to the network the machine is on.
func TestAPlanForAHostNamesNoContainerPathAndNoOpenAddress(t *testing.T) {
	t.Parallel()
	for name := range asProcesses {
		// A template whose environment has no macOS build is planned for a
		// Linux machine.
		platform := testHost.Platform
		if tpl := shippedTemplate(t, name); RunsAsProcesses(tpl, platform) != nil {
			if platform = "linux-aarch64"; RunsAsProcesses(tpl, platform) != nil {
				continue
			}
		}
		for variant, edit := range hostVariants {
			t.Run(name+"/"+variant, func(t *testing.T) {
				t.Parallel()
				in := hostInputs(t, name)
				in.Host.Platform = platform
				edit(&in)
				s, err := Plan(in)
				if err != nil {
					t.Fatal(err)
				}
				if s.Port != hostPort {
					t.Errorf("port = %d, want the executor's, %d", s.Port, hostPort)
				}
				if s.User.UID != testHost.UID || s.User.GID != testHost.GID || len(s.User.Groups) != 0 {
					t.Errorf("user = %+v, want Javo's own uid and gid", s.User)
				}
				folders := map[string]string{}
				for _, v := range s.Volumes {
					folders[v.Name] = cmpOr(v.Source, filepath.Join(testHost.Volumes, v.Name))
				}
				if folders[podstorage.HomeVolume] != hostHome {
					t.Errorf("home volume = %q, want the home folder %s", folders[podstorage.HomeVolume], hostHome)
				}
				for _, u := range append(slices.Clone(s.Steps), s.Main) {
					if u.Image != "" && (u.Image != testImages.Runner.Ref || u.Command[0] != "/"+model.ProductName) {
						t.Errorf("unit %s runs the image %s: only the platform's own binary can stand in for an image", u.Name, u.Image)
					}
					for _, m := range u.Mounts {
						if m.SubPath != "" || m.Path != folders[m.Volume] {
							t.Errorf("unit %s mounts %+v: a volume can only be its own folder, %q", u.Name, m, folders[m.Volume])
						}
					}
					if u.Dir != "" {
						if _, ok := u.MountAt(u.Dir); !ok {
							t.Errorf("unit %s runs in %s, in none of its volumes", u.Name, u.Dir)
						}
					}
					words := slices.Concat(u.Command, u.Args, []string{u.Dir})
					if u.Ready != nil {
						words = append(words, u.Ready.Exec...)
					}
					for _, e := range u.Env {
						if !strings.HasPrefix(e.Name, "JAVO_PIXI_") {
							words = append(words, e.Name+"="+e.Value)
						}
					}
					for _, w := range words {
						if u.Name == pixienv.InstallContainer && strings.HasPrefix(w, "set -eu") {
							continue // the install script, which names its folders by variable
						}
						if containerPath.MatchString(w) {
							t.Errorf("unit %s: %q names a path of a container", u.Name, w)
						}
						if strings.Contains(w, "0.0.0.0") {
							t.Errorf("unit %s: %q listens beyond this machine", u.Name, w)
						}
					}
				}
				if s.Step(podfiles.IdentityContainer) != nil || s.Step(podfiles.CopyContainer) != nil {
					t.Error("the platform's files are copied and a passwd written, as for a container")
				}
			})
		}
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func unitEnv(u workload.Unit) map[string]string {
	out := map[string]string{}
	for _, e := range u.Env {
		out[e.Name] = e.Value
	}
	return out
}

// Streamlit on a host: the program of the template's environment, started
// by the platform's script, on the port and the folders of the host.
func TestStreamlitPlannedForAHost(t *testing.T) {
	t.Parallel()
	in := hostInputs(t, "streamlit")
	in.App.Sample, in.App.File = true, SampleFileOf(in.Template)
	s, err := Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	app := hostHome + "/samples/streamlit/app.py"
	want := []string{
		hostFiles + "/pod-user.sh", "streamlit", "run", app,
		"--server.baseUrlPath=/u/alice/demo/", fmt.Sprintf("--server.port=%d", hostPort), "--server.address=127.0.0.1",
	}
	if got := slices.Concat(s.Main.Command, s.Main.Args); !slices.Equal(got[:len(want)], want) {
		t.Errorf("main runs %q, want it to start with %q", got, want)
	}
	envDir := testHost.Volumes + "/javo-env"
	env := unitEnv(s.Main)
	for name, want := range map[string]string{
		"HOME": hostHome, "JAVO_FILES_DIR": hostFiles, "JAVO_ENV_DIR": envDir,
		"TMPDIR": testHost.Volumes + "/tmp", "XDG_CACHE_HOME": testHost.Volumes + "/scratch/cache",
	} {
		if env[name] != want {
			t.Errorf("main: %s = %q, want %q", name, env[name], want)
		}
	}
	if s.Main.Dir != hostHome {
		t.Errorf("main runs in %q", s.Main.Dir)
	}

	var names []string
	for _, u := range s.Steps {
		names = append(names, u.Name)
	}
	if want := []string{model.SampleCopyContainer, pixienv.ToolsContainer, pixienv.InstallContainer}; !slices.Equal(names, want) {
		t.Fatalf("steps = %q, want %q", names, want)
	}
	javo := "/" + model.ProductName
	if cp := s.Steps[0]; !slices.Equal(slices.Concat(cp.Command, cp.Args), []string{
		javo, "pod-files", "--keep", "--from", hostFiles + "/samples/streamlit", "--to", hostHome + "/samples/streamlit",
	}) {
		t.Errorf("the sample is copied by %q", slices.Concat(cp.Command, cp.Args))
	}
	install := unitEnv(s.Steps[2])
	for name, want := range map[string]string{
		"PATH": testHost.InstallPath, "HOME": hostHome, "TMPDIR": testHost.Volumes + "/tmp",
		"JAVO_ENV_ROOT": hostHome + "/.javo/envs", "PIXI_CACHE_DIR": hostHome + "/.javo/pixi-cache",
		"JAVO_ENV_DIR": envDir, "JAVO_TOOL": envDir + "/javo", "JAVO_ENV_DEFAULT": hostFiles + "/envs/streamlit",
		"JAVO_TERMINATION_LOG": envDir + "/termination-log",
		// An environment's own install script finds the platform's files.
		"JAVO_FILES_DIR": hostFiles,
	} {
		if install[name] != want {
			t.Errorf("install step: %s = %q, want %q", name, install[name], want)
		}
	}
	if s.Main.Ready == nil || s.Main.Ready.HTTPPath != "/u/alice/demo/_stcore/health" {
		t.Errorf("ready check = %+v", s.Main.Ready)
	}
}

// The terminal program of the shell and coding agent templates is told
// where to listen (ttyd --interface): every address in a container, where
// the gateway reaches the pod at its own address, and loopback alone on a
// host, where every address is the network the machine is on.
func TestTheTerminalProgramListensOnLoopbackOnAHost(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"terminal", "claude-code", "herdr", "pi"} {
		in := hostInputs(t, name)
		onHost, err := Plan(in)
		if err != nil {
			t.Fatal(err)
		}
		in.Host, in.Port, in.Layout = nil, 0, testLayout(FixedUID)
		inContainer, err := Plan(in)
		if err != nil {
			t.Fatal(err)
		}
		for want, s := range map[string]workload.Spec{"--interface=127.0.0.1": onHost, "--interface=0.0.0.0": inContainer} {
			argv := slices.Concat(s.Main.Command, s.Main.Args)
			if i := slices.Index(argv, "ttyd"); i < 0 || !slices.Contains(argv[i:], want) {
				t.Errorf("%s runs %q, want ttyd with %s", name, argv, want)
			}
		}
	}
}

// Only words of the template and of a typed command are changed, and only
// where the whole word, or the whole value of a name=value word, is the
// platform's folder or the address that means every address.
func TestOnlyWholeWordsOfATemplateAreMovedToTheHost(t *testing.T) {
	t.Parallel()
	at := hostPlaces(&workload.Host{Files: hostFiles}, hostHome)
	for in, want := range map[string]string{
		"/opt/javo/pod-user.sh":                  hostFiles + "/pod-user.sh",
		"/opt/javo":                              hostFiles,
		"--index=/opt/javo/agents/terminal.html": "--index=" + hostFiles + "/agents/terminal.html",
		"--ip=0.0.0.0":                           "--ip=127.0.0.1",
		"HOST=0.0.0.0":                           "HOST=127.0.0.1",
		"0.0.0.0":                                "127.0.0.1",
		"--bind-addr=0.0.0.0:{{.Port}}":          "--bind-addr=127.0.0.1:{{.Port}}",

		"/opt/javo-env/activate.sh":          "/opt/javo-env/activate.sh",
		"/opt/javoX":                         "/opt/javoX",
		"see /opt/javo/pod-user.sh":          "see /opt/javo/pod-user.sh",
		"cd /opt/javo && ls":                 "cd /opt/javo && ls",
		"--note=a=/opt/javo/x":               "--note=a=/opt/javo/x",
		"10.0.0.0":                           "10.0.0.0",
		"0.0.0.01":                           "0.0.0.01",
		"--allow=0.0.0.0/0":                  "--allow=0.0.0.0/0",
		"{{.Home}}":                          "{{.Home}}",
		"--cwd={{if .File}}{{.File}}{{end}}": "--cwd={{if .File}}{{.File}}{{end}}",
	} {
		if got := at.words([]string{in})[0]; got != want {
			t.Errorf("%q became %q, want %q", in, got, want)
		}
	}
	keep := []string{"/opt/javo/pod-user.sh", "--ip=0.0.0.0"}
	if got := containerPlaces.words(keep); !slices.Equal(got, keep) {
		t.Errorf("a container's words changed: %q", got)
	}
}

func TestWhatAHostCannotRunIsRefused(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		edit func(*Inputs)
		why  string
	}{
		"from a repository": {func(in *Inputs) {
			in.App.Source = &model.Source{URL: "https://example.com/org/repo"}
		}, "repository"},
		"Nebi access": {func(in *Inputs) { in.NebiURL = "https://nebi.example.com" }, "Nebi"},
		"a home that is no folder": {func(in *Inputs) {
			in.Layout = podstorage.Build("alice", podstorage.Input{UID: 1000, GID: 100})
		}, "home"},
	} {
		in := hostInputs(t, "jupyterlab")
		c.edit(&in)
		_, err := Plan(in)
		var no *executor.UnsupportedError
		if !errors.As(err, &no) || !strings.Contains(no.Why, c.why) {
			t.Errorf("%s: %v, want it refused because %q", name, err, c.why)
		}
	}
}

// An environment that conda-forge cannot build for macOS stays locked for
// Linux, and its template is refused on a Mac with that reason, before
// anything is installed. On a Linux machine it runs, and so does an app
// with an environment picked in the template's place, which brings its own
// program.
func TestATemplateLockedForLinuxOnlyIsRefusedOnAMac(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"osx-arm64", "osx-64", ""} {
		in := hostInputs(t, "pluto")
		in.Host.Platform = platform
		var no *executor.UnsupportedError
		for what, err := range map[string]error{"RunsAsProcesses": RunsAsProcesses(in.Template, platform), "Plan": planError(in)} {
			if !errors.As(err, &no) || !strings.Contains(no.Why, "locked for Linux only") || !strings.Contains(no.Why, "julia") {
				t.Errorf("%s on %q: %v, want it refused for its Linux-only environment", what, platform, err)
			}
		}
		in.App.Environment = &model.Environment{Source: "nebi", ID: "e1", Name: "julia"}
		in.EnvFiles = &model.EnvironmentFiles{Manifest: "[workspace]\n", Lock: "version: 6\n"}
		if err := planError(in); err != nil {
			t.Errorf("Plan on %q with a picked environment: %v", platform, err)
		}
	}
	for _, platform := range []string{"linux-64", "linux-aarch64"} {
		in := hostInputs(t, "pluto")
		in.Host.Platform = platform
		if err := RunsAsProcesses(in.Template, platform); err != nil {
			t.Errorf("RunsAsProcesses on %s: %v", platform, err)
		}
		if err := planError(in); err != nil {
			t.Errorf("Plan on %s: %v", platform, err)
		}
	}
}

func planError(in Inputs) error {
	_, err := Plan(in)
	return err
}

var manifestPlatforms = regexp.MustCompile(`(?m)^platforms = \[(.*)\]$`)

// What the planner refuses on a Mac is what the bundled environments are
// not locked for: every environment of the pod files names macOS among
// its platforms, or is listed as Linux-only with the reason.
func TestLinuxOnlyEnvironmentsAreTheOnesNotLockedForMacOS(t *testing.T) {
	t.Parallel()
	manifests, err := filepath.Glob(filepath.Join("..", "..", "..", "pod", "files", "envs", "*", "pixi.toml"))
	if err != nil || len(manifests) == 0 {
		t.Fatalf("no bundled environments: %v", err)
	}
	for _, file := range manifests {
		name := filepath.Base(filepath.Dir(file))
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		m := manifestPlatforms.FindSubmatch(data)
		if m == nil {
			t.Errorf("%s names no platforms", file)
			continue
		}
		mac := strings.Contains(string(m[1]), `"osx-arm64"`)
		if _, listed := linuxOnly[name]; listed == mac {
			t.Errorf("environment %s: locked for macOS: %v, listed as Linux-only: %v", name, mac, listed)
		}
	}
}

// An upgrade restarts no app: the fingerprint of every shipped template is
// the one recorded before apps could run as processes. The four with a
// terminal program (terminal, claude-code, herdr, pi) were recorded again
// when they began to tell it where to listen, which restarts their apps
// once.
func TestShippedTemplatesKeepTheirFingerprint(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("testdata", "layout-fingerprints.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, line := range strings.Fields(string(data)) {
		name, fp, _ := strings.Cut(line, "=")
		want[name] = fp
	}
	layout := podstorage.Build("alice", podstorage.Input{UID: 1000, GID: 100})
	for _, name := range shippedNames(t) {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "helm", "javo", "files", "templates", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		tpl, err := templates.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		app := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "demo", Owner: "alice", Template: name}
		if got := LayoutFingerprint(app, &tpl, layout); got != want[name] {
			t.Errorf("%s: fingerprint %s, recorded %q: apps of it restart on upgrade", name, got, want[name])
		}
	}
}

package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podfiles"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/source"
	"github.com/nebari-dev/javo/internal/templates"
)

// shippedTemplate loads a built-in template from the Helm chart.
func shippedTemplate(t *testing.T, name string) *model.Template {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "helm", "javo", "files", "templates", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := templates.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return &tpl
}

// JupyterLab opens in the folder the app names: the sample's folder, the
// chosen folder, or the folder of a chosen notebook. jupyter-lab exits at
// start when preferred_dir is not an existing folder, so a folder that is
// gone falls back to the home. The pod's start command runs here against a
// stub jupyter-lab, with the home moved to a temporary folder.
func TestJupyterLabOpensTheChosenFolder(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "jupyterlab")
	home := t.TempDir()
	for _, d := range []string{"projects/q3", "samples/jupyterlab"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "projects/q3/report.ipynb"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	stub := "#!/bin/sh\nfor a; do echo \"$a\"; done\n"
	if err := os.WriteFile(filepath.Join(bin, "jupyter-lab"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		sample bool
		file   string
		want   string
	}{
		{"chosen folder", false, "projects/q3", "projects/q3"},
		{"chosen notebook", false, "projects/q3/report.ipynb", "projects/q3"},
		{"missing folder", false, "missing", ""},
		{"no file", false, "", ""},
		{"sample", true, SampleFileOf(tpl), "samples/jupyterlab"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "lab", Owner: "alice", Template: tpl.Name, File: c.file, Sample: c.sample}
			pod, err := BuildPod(ws, tpl, "/u/a/lab/", "", testLayout(FixedUID), sizes.Size{}, testImages)
			if err != nil {
				t.Fatal(err)
			}
			ctr := pod.Spec.Containers[0]
			argv := slices.Concat(ctr.Command, ctr.Args)
			if argv[0] != "/opt/javo/pod-user.sh" {
				t.Fatalf("command = %q, want it to start with pod-user.sh", argv)
			}
			for i, a := range argv {
				argv[i] = strings.ReplaceAll(a, homeDir, home)
			}
			cmd := exec.Command("/usr/bin/env", argv[1:]...)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%q: %v\n%s", argv, err, out)
			}
			want := "--ServerApp.preferred_dir=" + filepath.Join(home, c.want)
			if lines := strings.Split(strings.TrimSpace(string(out)), "\n"); !slices.Contains(lines, want) {
				t.Fatalf("jupyter-lab got %q, want %s", lines, want)
			}
		})
	}
}

// Whybook opens a notebook in its view: the sample's demo notebook, which
// `python -m whybook.demo` writes into the sample's folder at start, or the
// chosen notebook, through LabApp.default_url. A folder opens as in
// JupyterLab, with no notebook. The pod's start command runs here against
// stub jupyter-lab and python, with the home moved to a temporary folder.
func TestWhybookOpensTheChosenNotebook(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "whybook")
	home := t.TempDir()
	for _, d := range []string{"projects/q3", "samples/whybook"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "projects/q3/report.ipynb"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	stubs := map[string]string{
		"jupyter-lab": "#!/bin/sh\nfor a; do echo \"$a\"; done\n",
		// python -m whybook.demo DIR writes the demo notebook into DIR.
		"python": "#!/bin/sh\necho \"python $*\"\n[ \"$1 $2\" = \"-m whybook.demo\" ] && : > \"$3/pain_diary_cohort.ipynb\"\n",
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name   string
		sample bool
		file   string
		dir    string
		open   string
	}{
		{"sample", true, SampleFileOf(tpl), "samples/whybook", "samples/whybook/pain_diary_cohort.ipynb"},
		{"chosen notebook", false, "projects/q3/report.ipynb", "projects/q3", "projects/q3/report.ipynb"},
		{"chosen folder", false, "projects/q3", "projects/q3", ""},
		{"missing folder", false, "missing", "", ""},
		{"no file", false, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "why", Owner: "alice", Template: tpl.Name, File: c.file, Sample: c.sample}
			pod, err := BuildPod(ws, tpl, "/u/a/why/", "", testLayout(FixedUID), sizes.Size{}, testImages)
			if err != nil {
				t.Fatal(err)
			}
			ctr := pod.Spec.Containers[0]
			argv := slices.Concat(ctr.Command, ctr.Args)
			if argv[0] != "/opt/javo/pod-user.sh" {
				t.Fatalf("command = %q, want it to start with pod-user.sh", argv)
			}
			for i, a := range argv {
				argv[i] = strings.ReplaceAll(a, homeDir, home)
			}
			cmd := exec.Command("/usr/bin/env", argv[1:]...)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%q: %v\n%s", argv, err, out)
			}
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if want := "--ServerApp.preferred_dir=" + filepath.Join(home, c.dir); !slices.Contains(lines, want) {
				t.Errorf("jupyter-lab got %q, want %s", lines, want)
			}
			opened := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "--LabApp.default_url=") })
			switch {
			case c.open == "" && opened >= 0:
				t.Errorf("jupyter-lab got %s, want no notebook opened", lines[opened])
			case c.open != "" && (opened < 0 || lines[opened] != "--LabApp.default_url=/lab/tree/"+c.open):
				t.Errorf("jupyter-lab got %q, want --LabApp.default_url=/lab/tree/%s", lines, c.open)
			}
			wroteDemo := slices.Contains(lines, "python -m whybook.demo "+filepath.Join(home, "samples/whybook"))
			if wroteDemo != c.sample {
				t.Errorf("demo written: %v, want %v (%q)", wroteDemo, c.sample, lines)
			}
		})
	}
}

// The shipped custom template's sample starts with its own command, in the
// sample's folder, serving on the template's port under the app's prefix.
func TestCustomSampleRunsItsCommand(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "custom")
	ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "clock", Owner: "alice", Template: tpl.Name, File: SampleFileOf(tpl), Sample: true,
		Command: tpl.Spec.Sample.Command}
	if err := CheckCommand(ws.Command); err != nil {
		t.Fatal(err)
	}
	pod, err := BuildPod(ws, tpl, "/u/a/clock/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	c := pod.Spec.Containers[0]
	want := []string{"python", "sample.py", "--port=8080", "--base-url=/u/a/clock/"}
	if !slices.Equal(c.Args, want) || c.WorkingDir != "/home/user/samples/custom" {
		t.Fatalf("args = %q, workingDir = %q", c.Args, c.WorkingDir)
	}
}

// The shipped Container image template runs what each app names.
func TestShippedContainerTemplate(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "container")
	ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "web", Owner: "alice", Template: tpl.Name, Image: "nginxinc/nginx-unprivileged:1", Port: 8080}
	pod, err := BuildPod(ws, tpl, "/u/a/web/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.Containers[0].Image != ws.Image || tpl.Spec.BasePath != "Strip" {
		t.Fatalf("image = %q, basePath = %q", pod.Spec.Containers[0].Image, tpl.Spec.BasePath)
	}
}

// The coding agents run in tmux behind ttyd, with the platform's tmux
// settings from the pod files, in the folder the user chose (the sample's
// folder, or the home). agent-serve.sh starts the agent's tmux session
// detached before ttyd, so the agent starts working (pi on the app's first
// prompt) without anyone opening the app; every tab then attaches to that
// session, or makes a new one if the agent's shell has exited. The start
// command runs here against stub tmux and ttyd that log their arguments.
func TestAgentTemplatesStartTheSessionBeforeTtyd(t *testing.T) {
	t.Parallel()
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "pod", "files", "agents", "agent-serve.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, agent string }{
		{"claude-code", "claude --continue || claude; exec bash"},
		{"pi", "/opt/javo/agents/pi-start.sh"},
	} {
		tpl := shippedTemplate(t, tc.name)
		cases := []struct {
			label    string
			sample   bool
			file     string
			want     string
			tmuxFail bool
		}{
			{"folder", false, "projects/api", "/home/user/projects/api", false},
			{"no folder", false, "", "/home/user", false},
			{"sample", true, SampleFileOf(tpl), "/home/user/samples/" + tc.name, false},
			// A session that cannot be made at start does not keep the
			// terminal down: the first tab makes it.
			{"tmux fails", false, "", "/home/user", true},
		}
		for _, c := range cases {
			t.Run(tc.name+" "+c.label, func(t *testing.T) {
				t.Parallel()
				ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "agent", Owner: "alice", Template: tpl.Name, File: c.file, Sample: c.sample}
				pod, err := BuildPod(ws, tpl, "/u/a/agent/", "", testLayout(FixedUID), sizes.Size{}, testImages)
				if err != nil {
					t.Fatal(err)
				}
				argv := slices.Concat(pod.Spec.Containers[0].Command, pod.Spec.Containers[0].Args)
				if len(argv) < 2 || argv[0] != "/opt/javo/pod-user.sh" || argv[1] != "/opt/javo/agents/agent-serve.sh" {
					t.Fatalf("argv = %q, want pod-user.sh running agent-serve.sh", argv)
				}
				bin := t.TempDir()
				log := filepath.Join(bin, "calls")
				tmuxExit := "0"
				if c.tmuxFail {
					tmuxExit = "1"
				}
				for name, exit := range map[string]string{"tmux": tmuxExit, "ttyd": "0"} {
					stub := "#!/bin/sh\n{ printf '%s' " + name + "; for a; do printf ' [%s]' \"$a\"; done; echo; } >>" + log + "\nexit " + exit + "\n"
					if err := os.WriteFile(filepath.Join(bin, name), []byte(stub), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				cmd := exec.Command(script, argv[2:]...)
				cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%q: %v\n%s", argv, err, out)
				}
				data, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				session := " [-f] [/opt/javo/agents/tmux.conf] [new-session] %s [-s] [agent] [-c] [" + c.want + "] [" + tc.agent + "]"
				ttyd := "ttyd [--port=7681] [--writable] [--check-origin] [--index=/opt/javo/agents/terminal.html] [tmux]"
				want := []string{
					"tmux" + fmt.Sprintf(session, "[-d]"),
					ttyd + fmt.Sprintf(session, "[-A]"),
				}
				if got := strings.Split(strings.TrimSpace(string(data)), "\n"); !slices.Equal(got, want) {
					t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
				}
			})
		}
	}
}

// Claude Code reopens the folder's most recent conversation (claude
// --continue), so a restarted app shows it, and starts a new one when the
// folder has none: --continue then exits 1 with "No conversation found to
// continue" (Claude Code 2.1.302, the version package-lock.json pins).
func TestClaudeCodeContinuesItsLastConversation(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "claude-code")
	ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "agent", Owner: "alice", Template: tpl.Name}
	pod, err := BuildPod(ws, tpl, "/u/a/agent/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	argv := slices.Concat(pod.Spec.Containers[0].Command, pod.Spec.Containers[0].Args)
	if len(argv) < 4 || argv[1] != "/opt/javo/agents/agent-serve.sh" {
		t.Fatalf("argv = %q, want agent-serve.sh FOLDER COMMAND", argv)
	}
	for _, c := range []struct {
		label        string
		conversation bool
		want         []string
	}{
		{"with a conversation", true, []string{"claude [--continue]", "bash"}},
		{"without one", false, []string{"claude [--continue]", "claude", "bash"}},
	} {
		t.Run(c.label, func(t *testing.T) {
			t.Parallel()
			bin := t.TempDir()
			log := filepath.Join(bin, "calls")
			found := "1"
			if c.conversation {
				found = "0"
			}
			stubs := map[string]string{
				"claude": "[ \"$1\" = --continue ] && exit " + found + "\nexit 0",
				"bash":   "exit 0",
			}
			for name, body := range stubs {
				stub := "#!/bin/sh\n{ printf '%s' " + name + "; for a; do printf ' [%s]' \"$a\"; done; echo; } >>" + log + "\n" + body + "\n"
				if err := os.WriteFile(filepath.Join(bin, name), []byte(stub), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("/bin/sh", "-c", argv[3])
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%q: %v\n%s", argv[3], err, out)
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Split(strings.TrimSpace(string(data)), "\n"); !slices.Equal(got, c.want) {
				t.Fatalf("calls %q, want %q", got, c.want)
			}
		})
	}
}

// herdr is a multiplexer with its own background server, so ttyd runs it
// directly (no tmux) in the folder the user chose; every tab is a herdr
// client attached to the same server.
func TestHerdrTemplateStartsInTheChosenFolder(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "herdr")
	cases := []struct {
		label  string
		sample bool
		file   string
		want   string
	}{
		{"folder", false, "projects/api", "/home/user/projects/api"},
		{"no folder", false, "", "/home/user"},
		{"sample", true, SampleFileOf(tpl), "/home/user/samples/herdr"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "herd", Owner: "alice", Template: tpl.Name, File: c.file, Sample: c.sample}
			pod, err := BuildPod(ws, tpl, "/u/a/herd/", "", testLayout(FixedUID), sizes.Size{}, testImages)
			if err != nil {
				t.Fatal(err)
			}
			argv := append(append([]string{}, pod.Spec.Containers[0].Command...), pod.Spec.Containers[0].Args...)
			i := slices.Index(argv, "ttyd")
			if i < 0 || !slices.Contains(argv[i:], "--cwd="+c.want) || slices.Contains(argv, "tmux") {
				t.Fatalf("argv = %v, want ttyd with --cwd=%s and no tmux", argv, c.want)
			}
			if last := argv[len(argv)-1]; !strings.HasSuffix(last, "herdr") && !strings.HasSuffix(last, "herdr.sh") {
				t.Fatalf("argv = %v, want ttyd to run herdr", argv)
			}
		})
	}
}

// Pluto runs from the image's launcher, which takes the port, the URL prefix
// (Pluto requires it to start and end with a slash) and the notebook to open.
// With no notebook chosen, none is passed and Pluto starts on its file menu.
func TestPlutoOpensTheChosenNotebook(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "pluto")
	cases := []struct {
		label  string
		sample bool
		file   string
		want   []string
	}{
		{"notebook", false, "projects/q3/model.jl", []string{"/home/user/projects/q3/model.jl"}},
		{"no notebook", false, "", nil},
		{"sample", true, SampleFileOf(tpl), []string{"/home/user/samples/pluto/sample.jl"}},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "nb", Owner: "alice", Template: tpl.Name, File: c.file, Sample: c.sample}
			pod, err := BuildPod(ws, tpl, "/u/a/nb/", "", testLayout(FixedUID), sizes.Size{}, testImages)
			if err != nil {
				t.Fatal(err)
			}
			argv := slices.Concat(pod.Spec.Containers[0].Command, pod.Spec.Containers[0].Args)
			i := slices.Index(argv, "/opt/javo/pluto/run.jl")
			if i < 0 || argv[0] != "/opt/javo/pod-user.sh" {
				t.Fatalf("argv = %q, want pod-user.sh running the Pluto launcher", argv)
			}
			want := slices.Concat([]string{fmt.Sprint(tpl.Spec.Port), "/u/a/nb/"}, c.want)
			if got := argv[i+1:]; !slices.Equal(got, want) {
				t.Fatalf("launcher args = %q, want %q", got, want)
			}
		})
	}
}

// repoApp is an app of template tpl that runs from a repository folder.
func repoApp(tpl *model.Template) *model.App {
	return &model.App{ID: "app-1", Namespace: "javo-apps", Name: "site", Owner: "alice", Template: tpl.Name,
		Source: &model.Source{URL: "https://github.com/example-org/site", Ref: "main"}}
}

// Every template that accepts a repository builds a pod that clones it, and
// the ones that must not (an IDE editing a throwaway clone loses work) say so.
func TestShippedTemplatesTakeRepositoriesWhereTheyShould(t *testing.T) {
	t.Parallel()
	withRepo := []string{"streamlit", "panel", "bokeh", "dash", "gradio", "voila", "marimo", "pluto", "fastapi", "custom", "static-site", "backend-node", "backend-go"}
	without := []string{"jupyterlab", "vscode", "rstudio", "claude-code", "pi", "herdr", "container"}
	for _, name := range withRepo {
		t.Run(name, func(t *testing.T) {
			tpl := shippedTemplate(t, name)
			if tpl.Spec.FromRepo == model.FromRepoNone {
				t.Fatalf("fromRepo = %s", tpl.Spec.FromRepo)
			}
			ws := repoApp(tpl)
			if len(tpl.Spec.FileExtensions) > 0 && tpl.Spec.Build == nil {
				ws.File = "app" + tpl.Spec.FileExtensions[0]
			}
			if tpl.Spec.UserCommand == model.UserCommandRequired {
				ws.Command = "python server.py"
			}
			pod, err := BuildPod(ws, tpl, "/u/alice/site/", "", testLayout(FixedUID), sizes.Size{}, testImages)
			if err != nil {
				t.Fatal(err)
			}
			if got := initNames(pod); len(got) < 2 || got[0] != "clone" || got[1] != "source-check" {
				t.Fatalf("init containers = %v", got)
			}
		})
	}
	for _, name := range without {
		t.Run(name, func(t *testing.T) {
			if got := shippedTemplate(t, name).Spec.FromRepo; got != model.FromRepoNone {
				t.Fatalf("fromRepo = %s", got)
			}
		})
	}
}

// The static template builds a front-end with node and serves it with nginx
// under the app's prefix; a single HTML file of the home is copied instead.
func TestShippedStaticSite(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "static-site")
	if tpl.Spec.Kind != "static" || tpl.Spec.BasePath != "Strip" || tpl.Spec.AppLogin != "Disabled" ||
		tpl.Spec.Port != 8080 || tpl.Spec.ReadinessPath != "/" || len(tpl.Spec.Command)+len(tpl.Spec.Args) != 0 {
		t.Fatalf("spec = %+v", tpl.Spec)
	}
	b := tpl.Spec.Build
	if b == nil || b.Command != "npm ci && npm run build" || b.Output != "dist" || b.ServeDir != "/usr/share/nginx/html" {
		t.Fatalf("build = %+v", b)
	}
	if conf, err := b.RenderServerConfig(tpl.Spec.Port); err != nil || b.ServerConfigDir != "/etc/nginx/conf.d" ||
		!strings.Contains(conf, "listen 8080;") || !strings.Contains(conf, "root /usr/share/nginx/html;") || !strings.Contains(conf, "absolute_redirect off;") {
		t.Fatalf("server config in %s: %q, %v", b.ServerConfigDir, conf, err)
	}
	if !strings.Contains(tpl.Spec.Image, "nginx-unprivileged") || !strings.Contains(tpl.Spec.Image, "@sha256:") ||
		!strings.HasPrefix(b.Image, "node:") || !strings.Contains(b.Image, "@sha256:") {
		t.Fatalf("images are not pinned by digest: %s, %s", tpl.Spec.Image, b.Image)
	}
	if !slices.Equal(tpl.Spec.FileExtensions, []string{".html"}) {
		t.Fatalf("fileExtensions = %v", tpl.Spec.FileExtensions)
	}

	pod, err := BuildPod(repoApp(tpl), tpl, "/u/alice/site/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(initNames(pod), ","); got != "clone,source-check,build,site-copy,site-config,javo-files,pod-user" {
		t.Fatalf("init containers = %s", got)
	}

	home := &model.App{ID: "app-2", Namespace: "javo-apps", Name: "page", Owner: "alice", Template: tpl.Name, File: "art/page.html"}
	pod, err = BuildPod(home, tpl, "/u/alice/page/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(initNames(pod), ","); got != "javo-files,pod-user,site-copy,site-config" {
		t.Fatalf("init containers = %s", got)
	}
	if got := initNamed(t, pod, "site-copy").Command; !slices.Equal(got, []string{"cp", "/home/user/art/page.html", "/usr/share/nginx/html/index.html"}) {
		t.Fatalf("site-copy = %q", got)
	}
}

// The static template builds a folder that has a package.json with npm and
// serves any other folder as it is. The pod's build and site-copy steps run
// here, with the clone and the served folder moved to a temporary folder and
// a stub npm whose build writes dist/index.html.
func TestShippedStaticSiteBuildsOnlyWithAPackageJSON(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "static-site")
	ws := repoApp(tpl)
	ws.Source.Path = "web"
	pod, err := BuildPod(ws, tpl, "/u/alice/site/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	stub := "#!/bin/sh\necho \"npm $*\"\n[ \"$1 $2\" = \"run build\" ] && mkdir -p dist && echo built > dist/index.html\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		files  map[string]string
		served string
	}{
		{"package.json", map[string]string{"package.json": "{}", "index.html": "source"}, "built"},
		{"no package.json", map[string]string{"index.html": "as is"}, "as is"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			moved := func(s string) string {
				s = strings.ReplaceAll(s, SourceDir, filepath.Join(root, "src"))
				return strings.ReplaceAll(s, tpl.Spec.Build.ServeDir, filepath.Join(root, "html"))
			}
			folder := filepath.Join(root, "src", "current", "web")
			for _, d := range []string{folder, filepath.Join(root, "html")} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for name, text := range c.files {
				if err := os.WriteFile(filepath.Join(folder, name), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for _, step := range []string{"build", "site-copy"} {
				ctr := initNamed(t, pod, step)
				argv := make([]string, len(ctr.Command))
				for i, a := range ctr.Command {
					argv[i] = moved(a)
				}
				cmd := exec.Command(argv[0], argv[1:]...)
				cmd.Dir = moved(ctr.WorkingDir)
				cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%s: %v\n%s", step, err, out)
				}
			}
			got, err := os.ReadFile(filepath.Join(root, "html", "index.html"))
			if err != nil || strings.TrimSpace(string(got)) != c.served {
				t.Fatalf("served index.html = %q, %v; want %q", got, err, c.served)
			}
		})
	}
}

// The node and Go back-ends run the command their creator types, in their
// environment, from the repository or from the folder of a file in the home.
func TestShippedBackends(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, kind, command string }{
		{"backend-node", "node", "node server.js --port={{.Port}}"},
		{"backend-go", "go", "go run . --port={{.Port}}"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tpl := shippedTemplate(t, c.name)
			if tpl.Spec.Kind != c.kind || tpl.Spec.UserCommand != model.UserCommandRequired ||
				tpl.Spec.FromRepo != model.FromRepoOptional || tpl.Spec.BasePath != "Strip" || tpl.Spec.ReadinessPath != "/" {
				t.Fatalf("spec = %+v", tpl.Spec)
			}
			if tpl.Spec.Environment != c.name || tpl.Spec.Image != "" {
				t.Fatalf("environment %q, image %q: want the template's environment in the pixi image", tpl.Spec.Environment, tpl.Spec.Image)
			}
			want := strings.Fields(strings.ReplaceAll(c.command, "{{.Port}}", "8080"))
			ws := repoApp(tpl)
			ws.Source.Path = "api"
			ws.Command = c.command
			pod, err := BuildPod(ws, tpl, "/u/alice/api/", "", testLayout(FixedUID), sizes.Size{}, testImages)
			if err != nil {
				t.Fatal(err)
			}
			if app := pod.Spec.Containers[0]; !slices.Equal(app.Args, want) || app.WorkingDir != "/srv/src/current/api" {
				t.Fatalf("args = %q, workingDir = %q", app.Args, app.WorkingDir)
			}

			home := &model.App{ID: "app-2", Namespace: "javo-apps", Name: "api", Owner: "alice", Template: tpl.Name, File: "api/main.txt", Command: c.command}
			if pod, err = BuildPod(home, tpl, "/u/alice/api/", "", testLayout(FixedUID), sizes.Size{}, testImages); err != nil {
				t.Fatal(err)
			}
			if app := pod.Spec.Containers[0]; !slices.Equal(app.Args, want) || app.WorkingDir != "/home/user/api" {
				t.Fatalf("home app: args = %q, workingDir = %q", app.Args, app.WorkingDir)
			}
		})
	}
}

// Template cards are all one size: every shipped template says what it is in
// one short line, and the details live in the form.
func TestShippedTemplateDescriptionsAreOneShortLine(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "deploy", "helm", "javo", "files", "templates", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("templates: %v %v", files, err)
	}
	for _, f := range files {
		tpl := shippedTemplate(t, strings.TrimSuffix(filepath.Base(f), ".yaml"))
		if d := tpl.Spec.Description; d == "" || len([]rune(d)) > maxCardDescription || strings.ContainsAny(d, "\n;") || strings.Contains(d, ". ") {
			t.Errorf("%s: description %q is not one short line (at most %d characters)", tpl.Name, d, maxCardDescription)
		}
	}
}

// maxCardDescription is what one line of the narrowest template card holds
// (220px, in the New app form).
const maxCardDescription = 20

// adminTemplates are shipped templates only platform admins use; they have no
// sample to try.
var adminTemplates = map[string]bool{"backup": true}

// podFile is the repository path of a pod file the pods see under
// model.PodFilesDir (pod/files is copied there).
func podFile(t *testing.T, inPod string) string {
	t.Helper()
	rel, ok := strings.CutPrefix(inPod, model.PodFilesDir+"/")
	if !ok {
		t.Fatalf("%q is not under %s", inPod, model.PodFilesDir)
	}
	return filepath.Join("..", "..", "..", "pod", "files", rel)
}

// Every shipped template a person picks ships a sample that just runs: an
// app template names a sample folder of the pod files (and the file it
// opens) or, taking any image, an image pinned by digest; a job template
// names a sample job whose scripts are pod files. Picking the template and
// pressing Create then needs no code of the person's own.
func TestEveryShippedTemplateHasARunnableSample(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(filepath.Join("..", "..", "..", "deploy", "helm", "javo", "files", "templates"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".yaml")
		if !ok || adminTemplates[name] {
			continue
		}
		t.Run(name, func(t *testing.T) {
			tpl := shippedTemplate(t, name)
			// What the sample needs, so a create without a size runs it.
			if tpl.Spec.DefaultResources == nil {
				t.Error("no defaultResources")
			}
			if tpl.Spec.JobOnly {
				sm := tpl.Spec.Jobs.Sample
				if sm == nil {
					t.Fatal("no sample job (spec.jobs.sample)")
				}
				for _, w := range strings.Fields(sm.Command) {
					if strings.HasPrefix(w, model.PodFilesDir+"/") {
						if _, err := os.Stat(podFile(t, w)); err != nil {
							t.Errorf("the sample job runs %s: %v", w, err)
						}
					}
				}
				return
			}
			sm := tpl.Spec.Sample
			switch {
			case sm == nil:
				t.Fatal("no sample (spec.sample)")
			case tpl.Spec.UserImage:
				if !strings.Contains(sm.Image, "@sha256:") {
					t.Errorf("sample image %q is not pinned by digest", sm.Image)
				}
			default:
				dir := podFile(t, sm.Path)
				if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
					t.Fatalf("sample folder %s: %v", dir, err)
				}
				if sm.Open != "" {
					if _, err := os.Stat(filepath.Join(dir, sm.Open)); err != nil {
						t.Errorf("sample opens %s: %v", sm.Open, err)
					}
				}
			}
		})
	}
}

// A static site from the sample serves the sample's page: the pod files
// arrive, the sample is copied into the home, and only then is its page
// copied to the folder nginx serves.
func TestStaticSiteSampleIsCopiedBeforeItIsServed(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "static-site")
	ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "site", Owner: "alice", Template: tpl.Name, File: SampleFileOf(tpl), Sample: true}
	pod, err := BuildPod(ws, tpl, "/u/a/site/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range pod.Spec.InitContainers {
		names = append(names, c.Name)
	}
	at := func(n string) int {
		i := slices.Index(names, n)
		if i < 0 {
			t.Fatalf("no init container %s in %q", n, names)
		}
		return i
	}
	if !(at(podfiles.CopyContainer) < at(model.SampleCopyContainer) && at(model.SampleCopyContainer) < at(source.SiteCopyContainer)) {
		t.Fatalf("init containers run in the order %q", names)
	}
	site := pod.Spec.InitContainers[at(source.SiteCopyContainer)]
	if got := site.Command[1]; got != "/home/user/samples/static-site/index.html" {
		t.Fatalf("the site copy reads %q", got)
	}
}

// The shipped pi is the coding agent that "Build with pi" starts: its token
// may deploy and use the model endpoint. Its session runs the start script
// that sets the model up and passes the first prompt (checked in
// TestAgentTemplatesStartTheSessionBeforeTtyd).
func TestPiTemplateIsTheCodingAgent(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "pi")
	if !tpl.Spec.AgentDeploy || !tpl.Spec.AgentModel || model.MaxSharing(tpl) != model.AccessPrivate {
		t.Fatalf("spec %+v", tpl.Spec)
	}
}

// A template that hands its agent the API guide (apiGuidePaths) gives it the
// deploy rights the guide describes: its token may create and change the
// owner's apps and jobs (agentDeploy), on apps that are never shared.
func TestTemplatesWithTheAgentGuideMayDeploy(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "deploy", "helm", "javo", "files", "templates", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	guided := 0
	for _, f := range files {
		tpl := shippedTemplate(t, strings.TrimSuffix(filepath.Base(f), ".yaml"))
		if len(tpl.Spec.APIGuidePaths) == 0 {
			continue
		}
		guided++
		if !tpl.Spec.AgentDeploy || model.MaxSharing(tpl) != model.AccessPrivate {
			t.Errorf("%s mounts the API guide but its agent may not deploy (agentDeploy %v, maxSharing %s)", tpl.Name, tpl.Spec.AgentDeploy, model.MaxSharing(tpl))
		}
	}
	if guided < 2 {
		t.Fatalf("only %d shipped templates mount the API guide", guided)
	}
}

// Harf runs from its own image, pinned by digest, through pod-user.sh (which
// names the pod's user, the name Harf shows without a proxy header). It
// listens on the app's port, and Pi works in the folder the user chose, the
// sample's folder, or ~/workspace (Harf's own default) when none is chosen, so
// the uploads and images Harf writes there stay out of the home's top level.
// The kubelet checks Harf's /healthz. As a coding agent its pod gets the
// ssh-agent sidecar, which runs a pod file, so the pod files are there.
func TestHarfWorksInTheChosenFolder(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "harf")
	cases := []struct {
		label  string
		sample bool
		file   string
		want   string
	}{
		{"folder", false, "projects/api", "/home/user/projects/api"},
		{"no folder", false, "", "/home/user/workspace"},
		{"sample", true, SampleFileOf(tpl), "/home/user/samples/harf"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "chat", Owner: "alice", Template: tpl.Name, File: c.file, Sample: c.sample}
			pod, err := BuildPod(ws, tpl, "/u/a/chat/", "", testLayout(FixedUID), sizes.Size{}, testImages)
			if err != nil {
				t.Fatal(err)
			}
			ctr := pod.Spec.Containers[0]
			if !strings.HasPrefix(ctr.Image, "ghcr.io/aktech/harf:") || !strings.Contains(ctr.Image, "@sha256:") {
				t.Fatalf("image = %q, want ghcr.io/aktech/harf pinned by digest", ctr.Image)
			}
			argv := slices.Concat(ctr.Command, ctr.Args)
			want := []string{"/opt/javo/pod-user.sh", "/usr/local/bin/harf", fmt.Sprintf("-addr=:%d", tpl.Spec.Port), "-workspace=" + c.want}
			if !slices.Equal(argv, want) {
				t.Fatalf("argv = %q, want %q", argv, want)
			}
			if p := ctr.ReadinessProbe; p == nil || p.HTTPGet == nil || p.HTTPGet.Path != "/healthz" {
				t.Fatalf("readiness probe = %+v, want GET /healthz", p)
			}
			WithSSHAgent(pod, tpl)
			_, side := initContainer(pod, SSHAgentContainer)
			if side == nil {
				t.Fatalf("no %s sidecar", SSHAgentContainer)
			}
			if _, err := os.Stat(podFile(t, side.Command[0])); err != nil || !slices.ContainsFunc(side.VolumeMounts, func(m corev1.VolumeMount) bool { return m.MountPath == model.PodFilesDir }) {
				t.Fatalf("the sidecar runs %s, which it does not see (mounts %v): %v", side.Command[0], side.VolumeMounts, err)
			}
		})
	}
}

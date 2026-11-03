package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/sizes"
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
// settings from the pod files. The terminal opens in the folder the user
// chose (the sample's folder, or the home), and every tab attaches to the
// same tmux session, so closing a tab keeps the agent running.
func TestAgentTemplatesStartTmuxInTheChosenFolder(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"claude-code", "pi"} {
		tpl := shippedTemplate(t, name)
		cases := []struct {
			label  string
			sample bool
			file   string
			want   string
		}{
			{"folder", false, "projects/api", "/home/user/projects/api"},
			{"no folder", false, "", "/home/user"},
			{"sample", true, SampleFileOf(tpl), "/home/user/samples/" + name},
		}
		for _, c := range cases {
			t.Run(name+" "+c.label, func(t *testing.T) {
				ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "agent", Owner: "alice", Template: tpl.Name, File: c.file, Sample: c.sample}
				pod, err := BuildPod(ws, tpl, "/u/a/agent/", "", testLayout(FixedUID), sizes.Size{}, testImages)
				if err != nil {
					t.Fatal(err)
				}
				argv := append(append([]string{}, pod.Spec.Containers[0].Command...), pod.Spec.Containers[0].Args...)
				i := slices.Index(argv, "tmux")
				if i < 0 || !slices.Contains(argv[:i], "ttyd") {
					t.Fatalf("argv = %v, want ttyd running tmux", argv)
				}
				want := []string{"tmux", "-f", "/opt/javo/agents/tmux.conf", "new-session", "-A", "-s", "agent", "-c", c.want}
				if got := argv[i:min(len(argv), i+len(want))]; !slices.Equal(got, want) {
					t.Fatalf("tmux argv = %v, want %v", argv[i:], want)
				}
			})
		}
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
	if got := strings.Join(initNames(pod), ","); got != "clone,source-check,build,site-copy,site-config" {
		t.Fatalf("init containers = %s", got)
	}

	home := &model.App{ID: "app-2", Namespace: "javo-apps", Name: "page", Owner: "alice", Template: tpl.Name, File: "art/page.html"}
	pod, err = BuildPod(home, tpl, "/u/alice/page/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(initNames(pod), ","); got != "site-copy,site-config" {
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

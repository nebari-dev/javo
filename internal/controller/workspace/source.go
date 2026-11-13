package workspace

import (
	"cmp"
	"fmt"
	"path"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/kube"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/source"
	"github.com/nebari-dev/javo/internal/workload"
)

// Where an app that runs from Git keeps its clone and, for a template with a
// build step, the files it serves.
const (
	sourceVolume = "src"
	// SourceDir is the clone root; git-sync links "current" in it to the
	// checked out commit.
	SourceDir  = "/srv/src"
	cloneLink  = "current"
	sourceRoot = SourceDir + "/" + cloneLink

	siteVolume = "site"

	// siteConfigVolume is the empty folder the site-config step writes the
	// template's server config in, mounted over the template's
	// build.serverConfigDir in the app container.
	siteConfigVolume = "site-config"

	// cloneTmpVolume is the clone's own /tmp: git-sync keeps its git config
	// and credential cache socket there, and the build step, which runs pull
	// request code, must not see them.
	cloneTmpVolume = "clone-tmp"

	gitTokenVolume = "git-token"
	gitTokenDir    = "/etc/" + model.ProductName + "/git"
	gitTokenKey    = "token"
	gitTokenUser   = "x-access-token"
)

// Images are the platform's images an app's pod may need: git-sync clones
// an app from Git, the javo image (Runner) checks the clone and carries the
// pod files, and Pixi runs a template with an environment and no image of
// its own. GitConfig is the platform's git-sync --git-config value; empty
// adds no flag; Runner's pull secrets go on every pod that runs it, except
// a pod that runs an image the person named. AllowedRegistries is
// containerImages.allowedRegistries, which such an image must still pass.
type Images struct {
	Clone, Pixi, GitConfig string
	Runner                 podspec.Image
	AllowedRegistries      []string
}

// GitTokenSecretName is the Secret holding the repository token of the app
// whose Pod and Service are called objectName (see SecretName).
func GitTokenSecretName(objectName string) string {
	return kube.SecretName(objectName, executor.UseGit)
}

// sourceFolder is the folder of the clone the app runs from.
func sourceFolder(s *model.Source) string {
	return path.Join(sourceRoot, path.Clean("/"+s.Path))
}

// buildOf is what turns the clone into the files to serve: the app's own
// build settings over the template's. An app that sets build replaces the
// command wholesale (an empty one means nothing to build); an output it names
// replaces the template's. With nothing to build and no output named, the
// folder itself is served. ifFile is the template's: only its own command
// waits for that file.
func buildOf(ws *model.App, tb *model.TemplateBuild) (command, output, ifFile string) {
	command, output, ifFile = tb.Command, tb.Output, tb.IfFile
	explicit := false
	if b := ws.Source.Build; b != nil {
		command, ifFile = b.Command, ""
		if b.Output != "" {
			output, explicit = b.Output, true
		}
	}
	if command == "" && !explicit {
		output, ifFile = "", ""
	}
	return command, output, ifFile
}

// The scripts of a build that waits for a file ($1 in both): the build runs
// only with it, and the copy then takes the output folder ($3), else the
// folder itself ($2). Paths and names reach them as arguments, never as shell.
const (
	ifFileBuild = `if [ -e "$1" ]; then
%s
else
  echo "There is no $1 in the folder, so nothing is built: the folder is served as it is."
fi`
	ifFileCopy = `if [ -e "$2/$1" ]; then from="$3"; else from="$2"; fi; cp -r "$from/." "$4"`
)

// sourceSteps are the steps that make the clone and, for a template with a
// build step, the site: clone, source-check, build and site-copy.
func sourceSteps(ws *model.App, t *model.Template, baseURL string, img Images) ([]workload.Unit, error) {
	if img.Clone == "" || img.Runner.Ref == "" {
		return nil, fmt.Errorf("spec.source: the platform has no clone or runner image to clone with (set source.cloneImage and the controller's --runner-image)")
	}
	s := ws.Source
	if s.Ref != "" {
		if err := model.CheckGitRef(s.Ref); err != nil {
			return nil, fmt.Errorf("spec.source.ref: %w", err)
		}
	}
	srcMount := workload.Mount{Volume: sourceVolume, Path: SourceDir}
	tmpMount := workload.Mount{Volume: podspec.TmpVolume, Path: "/tmp"}
	step := func(name, image string, mounts ...workload.Mount) workload.Unit {
		return workload.Unit{Name: name, Image: image, Env: []workload.Env{{Name: "HOME", Value: "/tmp"}}, Mounts: mounts}
	}
	clone := step(source.CloneContainer, img.Clone, srcMount, workload.Mount{Volume: cloneTmpVolume, Path: "/tmp"})
	clone.Args = []string{"--repo=" + s.URL, "--ref=" + cmp.Or(s.Ref, "HEAD"), "--root=" + SourceDir, "--link=" + cloneLink, "--one-time", "--depth=1", "--submodules=off"}
	if img.GitConfig != "" {
		clone.Args = append(clone.Args, "--git-config="+img.GitConfig)
	}

	check := step(source.CheckContainer, img.Runner.Ref, srcMount)
	check.ReadOnlyRoot = true
	check.Command = []string{"/" + model.ProductName, "source-check", "--link=" + sourceRoot, "--path=" + s.Path}
	if ws.File != "" {
		check.Command = append(check.Command, "--file="+ws.File)
	}
	steps := []workload.Unit{clone, check}

	tb := t.Spec.Build
	if tb == nil {
		return steps, nil
	}
	command, output, ifFile := buildOf(ws, tb)
	folder := sourceFolder(s)
	if command != "" {
		build := step(source.BuildContainer, tb.Image, srcMount, tmpMount)
		build.Dir = folder
		build.Command = []string{"sh", "-c", command}
		if ifFile != "" {
			build.Command = []string{"sh", "-c", fmt.Sprintf(ifFileBuild, command), "sh", ifFile}
		}
		build.Env = append(build.Env,
			workload.Env{Name: "CI", Value: "true"},
			workload.Env{Name: "npm_config_cache", Value: "/tmp/npm"},
			workload.Env{Name: "JAVO_BASE_URL", Value: baseURL})
		steps = append(steps, build)
	}
	from := folder
	if output != "" {
		from = path.Join(folder, path.Clean("/"+output))
	}
	cp := step(source.SiteCopyContainer, tb.Image, srcMount, workload.Mount{Volume: siteVolume, Path: tb.ServeDir})
	cp.ReadOnlyRoot = true
	cp.Command = []string{"cp", "-r", from + "/.", tb.ServeDir}
	if ifFile != "" {
		cp.Command = []string{"sh", "-c", ifFileCopy, "sh", ifFile, folder, from, tb.ServeDir}
	}
	return withSiteConfigStep(append(steps, cp), t)
}

// withSiteConfigStep adds the step that writes the template's server
// config, when it has one, after the steps that fill the folder it serves.
// A template with none keeps the config of its image.
func withSiteConfigStep(steps []workload.Unit, t *model.Template) ([]workload.Unit, error) {
	tb := t.Spec.Build
	if tb.ServerConfig == "" {
		return steps, nil
	}
	conf, err := tb.RenderServerConfig(t.Spec.Port)
	if err != nil {
		return nil, fmt.Errorf("workspace template %q: spec.build.serverConfig: %w", t.Name, err)
	}
	return append(steps, workload.Unit{
		Name: source.SiteConfigContainer, Image: tb.Image, ReadOnlyRoot: true,
		Command: []string{"sh", "-c", `printf '%s\n' "$1" > "$2/site.conf"`, "sh", conf, tb.ServerConfigDir},
		Mounts:  []workload.Mount{{Volume: siteConfigVolume, Path: tb.ServerConfigDir}},
	}), nil
}

// siteFromHomeStep copies one file of the home to the folder a static
// template serves, as its index. It is the only unit that sees the home.
func siteFromHomeStep(t *model.Template, homeFile string) workload.Unit {
	tb := t.Spec.Build
	return workload.Unit{
		Name: source.SiteCopyContainer, Image: tb.Image, ReadOnlyRoot: true,
		Env:     []workload.Env{{Name: "HOME", Value: "/tmp"}},
		Command: []string{"cp", homeFile, path.Join(tb.ServeDir, "index.html")},
		Mounts: []workload.Mount{
			{Volume: homeVolume, Path: homeDir, ReadOnly: true},
			{Volume: siteVolume, Path: tb.ServeDir},
		},
	}
}

// addSourceVolumes gives s the volumes its source steps and main unit use.
func addSourceVolumes(s *workload.Spec, ws *model.App, t *model.Template) {
	app := &s.Main
	if ws.Source != nil {
		s.Volumes = append(s.Volumes, workload.Volume{Name: sourceVolume}, workload.Volume{Name: cloneTmpVolume})
		app.Mounts = append(app.Mounts, workload.Mount{Volume: sourceVolume, Path: SourceDir})
	}
	if tb := t.Spec.Build; tb != nil {
		s.Volumes = append(s.Volumes, workload.Volume{Name: siteVolume})
		app.Mounts = append(app.Mounts, workload.Mount{Volume: siteVolume, Path: tb.ServeDir})
		if tb.ServerConfig != "" {
			s.Volumes = append(s.Volumes, workload.Volume{Name: siteConfigVolume})
			app.Mounts = append(app.Mounts, workload.Mount{Volume: siteConfigVolume, Path: tb.ServerConfigDir})
		}
	}
}

// addGitToken lets the clone step read the app's repository token from the
// bundle of holder, as a file (never in the URL, an argument value or the
// environment). generation is which paste of the token that is; without one
// there is nothing to read. A fork preview runs code the owner has not
// written and gets nothing.
func addGitToken(s *workload.Spec, ws *model.App, holder, generation string) {
	if ws.Source == nil || ws.Source.Fork || generation == "" {
		return
	}
	s.Volumes = append(s.Volumes, workload.Volume{Name: gitTokenVolume, Secret: &workload.SecretFiles{
		Holder: holder, Use: executor.UseGit, Keys: []string{gitTokenKey},
	}})
	if c := s.Step(source.CloneContainer); c != nil {
		c.Args = append(c.Args, "--username="+gitTokenUser, "--password-file="+gitTokenDir+"/"+gitTokenKey)
		c.Mounts = append(c.Mounts, workload.Mount{Volume: gitTokenVolume, Path: gitTokenDir, ReadOnly: true})
	}
}

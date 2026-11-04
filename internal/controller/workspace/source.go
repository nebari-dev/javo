package workspace

import (
	"fmt"
	"path"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/source"
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
// adds no flag; Runner's pull secrets go on every pod that runs it.
type Images struct {
	Clone, Pixi, GitConfig string
	Runner                 podspec.Image
}

// GitTokenSecretName is the Secret holding the repository token of the app
// whose Pod and Service are called objectName (see SecretName).
func GitTokenSecretName(objectName string) string { return SecretName(objectName, "git") }

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

// sourceInitContainers are the steps that make the clone and, for a template
// with a build step, the site: clone, source-check, build and site-copy.
func sourceInitContainers(ws *model.App, t *model.Template, baseURL string, img Images) ([]corev1.Container, error) {
	if img.Clone == "" || img.Runner.Ref == "" {
		return nil, fmt.Errorf("spec.source: the platform has no clone or runner image to clone with (set source.cloneImage and the controller's --runner-image)")
	}
	s := ws.Source
	if s.Ref != "" {
		if err := model.CheckGitRef(s.Ref); err != nil {
			return nil, fmt.Errorf("spec.source.ref: %w", err)
		}
	}
	srcMount := corev1.VolumeMount{Name: sourceVolume, MountPath: SourceDir}
	tmpMount := corev1.VolumeMount{Name: podspec.TmpVolume, MountPath: "/tmp"}
	step := func(name, image string, mounts ...corev1.VolumeMount) corev1.Container {
		return corev1.Container{
			Name: name, Image: image, SecurityContext: podspec.RestrictedContext(),
			Env:          []corev1.EnvVar{{Name: "HOME", Value: "/tmp"}},
			VolumeMounts: mounts,
			// Errors go to stderr; the kubelet turns them into the termination message.
			TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
		}
	}
	ref := s.Ref
	if ref == "" {
		ref = "HEAD"
	}
	clone := step(source.CloneContainer, img.Clone, srcMount, corev1.VolumeMount{Name: cloneTmpVolume, MountPath: "/tmp"})
	clone.Args = []string{"--repo=" + s.URL, "--ref=" + ref, "--root=" + SourceDir, "--link=" + cloneLink, "--one-time", "--depth=1", "--submodules=off"}
	if img.GitConfig != "" {
		clone.Args = append(clone.Args, "--git-config="+img.GitConfig)
	}

	check := step(source.CheckContainer, img.Runner.Ref, srcMount)
	check.Command = []string{"/" + model.ProductName, "source-check", "--link=" + sourceRoot, "--path=" + s.Path}
	if ws.File != "" {
		check.Command = append(check.Command, "--file="+ws.File)
	}
	inits := []corev1.Container{clone, check}

	if tb := t.Spec.Build; tb != nil {
		command, output, ifFile := buildOf(ws, tb)
		folder := sourceFolder(s)
		if command != "" {
			build := step(source.BuildContainer, tb.Image, srcMount, tmpMount)
			build.WorkingDir = folder
			build.Command = []string{"sh", "-c", command}
			if ifFile != "" {
				build.Command = []string{"sh", "-c", fmt.Sprintf(ifFileBuild, command), "sh", ifFile}
			}
			build.Env = append(build.Env,
				corev1.EnvVar{Name: "CI", Value: "true"},
				corev1.EnvVar{Name: "npm_config_cache", Value: "/tmp/npm"},
				corev1.EnvVar{Name: "JAVO_BASE_URL", Value: baseURL})
			inits = append(inits, build)
		}
		from := folder
		if output != "" {
			from = path.Join(folder, path.Clean("/"+output))
		}
		cp := step(source.SiteCopyContainer, tb.Image, srcMount, corev1.VolumeMount{Name: siteVolume, MountPath: tb.ServeDir})
		cp.Command = []string{"cp", "-r", from + "/.", tb.ServeDir}
		if ifFile != "" {
			cp.Command = []string{"sh", "-c", ifFileCopy, "sh", ifFile, folder, from, tb.ServeDir}
		}
		inits = append(inits, cp)
		var err error
		if inits, err = withSiteConfig(inits, t); err != nil {
			return nil, err
		}
	}
	return inits, nil
}

// withSiteConfig adds the step that writes the template's server config, when
// it has one, after the steps that fill the folder it serves. A template with
// none keeps the config of its image.
func withSiteConfig(inits []corev1.Container, t *model.Template) ([]corev1.Container, error) {
	tb := t.Spec.Build
	if tb.ServerConfig == "" {
		return inits, nil
	}
	conf, err := tb.RenderServerConfig(t.Spec.Port)
	if err != nil {
		return nil, fmt.Errorf("workspace template %q: spec.build.serverConfig: %w", t.Name, err)
	}
	return append(inits, corev1.Container{
		Name: source.SiteConfigContainer, Image: tb.Image, SecurityContext: podspec.RestrictedContext(),
		Command:                  []string{"sh", "-c", `printf '%s\n' "$1" > "$2/site.conf"`, "sh", conf, tb.ServerConfigDir},
		VolumeMounts:             []corev1.VolumeMount{{Name: siteConfigVolume, MountPath: tb.ServerConfigDir}},
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
	}), nil
}

// siteFromHome copies one file of the home to the folder a static template
// serves, as its index. It is the only container that sees the home.
func siteFromHome(t *model.Template, homeFile string) corev1.Container {
	tb := t.Spec.Build
	return corev1.Container{
		Name: source.SiteCopyContainer, Image: tb.Image, SecurityContext: podspec.RestrictedContext(),
		Env:     []corev1.EnvVar{{Name: "HOME", Value: "/tmp"}},
		Command: []string{"cp", homeFile, path.Join(tb.ServeDir, "index.html")},
		VolumeMounts: []corev1.VolumeMount{
			{Name: homeVolume, MountPath: homeDir, ReadOnly: true},
			{Name: siteVolume, MountPath: tb.ServeDir},
		},
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
	}
}

// addSourceVolumes gives the pod the volumes its source steps and app use.
func addSourceVolumes(spec *corev1.PodSpec, ws *model.App, t *model.Template) {
	empty := corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
	app := &spec.Containers[0]
	if ws.Source != nil {
		spec.Volumes = append(spec.Volumes, corev1.Volume{Name: sourceVolume, VolumeSource: empty},
			corev1.Volume{Name: cloneTmpVolume, VolumeSource: empty})
		app.VolumeMounts = append(app.VolumeMounts, corev1.VolumeMount{Name: sourceVolume, MountPath: SourceDir})
	}
	if tb := t.Spec.Build; tb != nil {
		spec.Volumes = append(spec.Volumes, corev1.Volume{Name: siteVolume, VolumeSource: empty})
		app.VolumeMounts = append(app.VolumeMounts, corev1.VolumeMount{Name: siteVolume, MountPath: tb.ServeDir})
		if tb.ServerConfig != "" {
			spec.Volumes = append(spec.Volumes, corev1.Volume{Name: siteConfigVolume, VolumeSource: empty})
			app.VolumeMounts = append(app.VolumeMounts, corev1.VolumeMount{Name: siteConfigVolume, MountPath: tb.ServerConfigDir})
		}
	}
}

// WithGitToken lets the clone container read the app's repository token from
// the Secret secretName, as a file (never in the URL, an argument value or the
// environment), and records on the pod which paste (generation) it was built
// for. A fork preview runs code the owner has not written and gets nothing.
func WithGitToken(pod *corev1.Pod, ws *model.App, secretName, generation string) {
	if ws.Source == nil || ws.Source.Fork || generation == "" {
		return
	}
	metav1.SetMetaDataAnnotation(&pod.ObjectMeta, model.AnnotationGitToken, generation)
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: gitTokenVolume, VolumeSource: corev1.VolumeSource{
		Secret: &corev1.SecretVolumeSource{SecretName: secretName, Items: []corev1.KeyToPath{{Key: gitTokenKey, Path: gitTokenKey}}},
	}})
	for i := range pod.Spec.InitContainers {
		c := &pod.Spec.InitContainers[i]
		if c.Name != source.CloneContainer {
			continue
		}
		c.Args = append(c.Args, "--username="+gitTokenUser, "--password-file="+gitTokenDir+"/"+gitTokenKey)
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: gitTokenVolume, MountPath: gitTokenDir, ReadOnly: true})
	}
}

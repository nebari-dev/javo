// Package workspace builds and reconciles the objects behind an app: its Pod
// and Service. It asks for the owner's home, which the storage controller
// turns into a claim.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"text/template"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/nebari-dev/javo/internal/cmdline"
	"github.com/nebari-dev/javo/internal/homepath"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podfiles"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
)

const (
	homeDir    = podstorage.HomeDir
	homeVolume = podstorage.HomeVolume

	// SampleDir is the folder of the home that sample copies go under.
	SampleDir = "samples"
)

// templateData holds the fields available to template command, args and readinessPath.
type templateData struct {
	BaseURL string
	Port    int32
	File    string
	Home    string
	// Source is the folder of the clone an app from Git runs from; empty for
	// an app that runs from the home.
	Source string
	// Sample is the folder the template's sample is copied to, set only
	// when the workspace started from it (the init container makes it).
	Sample string
	// Host is the public host (and port, if any) browsers reach the gateway
	// at, for apps that check the websocket Origin. Empty when not configured.
	Host string
}

// labels are what the controller finds an app's runtime objects by: the row
// id (the only link back to the row) and the owner.
func labels(app *model.App) map[string]string {
	return map[string]string{
		model.LabelApp:   app.ID,
		model.LabelOwner: app.Owner,
	}
}

// templateFuncs are the functions template strings may call: dir and base
// split a path (uvicorn takes a folder and a module), trimSuffix drops a
// suffix as in {{trimSuffix "/" .BaseURL}} (Starlette's root_path).
var templateFuncs = template.FuncMap{
	"dir":        path.Dir,
	"base":       path.Base,
	"trimSuffix": func(suffix, s string) string { return strings.TrimSuffix(s, suffix) },
}

// render executes one template string; errors name the template and the field.
func render(tplName, where, text string, data any) (string, error) {
	tp, err := template.New(where).Funcs(templateFuncs).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("workspace template %q: %s: %w", tplName, where, err)
	}
	var b strings.Builder
	if err := tp.Execute(&b, data); err != nil {
		return "", fmt.Errorf("workspace template %q: %s: %w", tplName, where, err)
	}
	return b.String(), nil
}

// ProxyHeadersOf returns the template's proxyHeaders with {{.BaseURL}} filled
// in for the app served under baseURL. The gateway sets them on each request
// it forwards.
func ProxyHeadersOf(t *model.Template, baseURL string) (map[string]string, error) {
	out := make(map[string]string, len(t.Spec.ProxyHeaders))
	for k, v := range t.Spec.ProxyHeaders {
		r, err := render(t.Name, "proxyHeaders."+k, v, templateData{BaseURL: baseURL})
		if err != nil {
			return nil, err
		}
		out[k] = r
	}
	return out, nil
}

// renderAll renders each string. One that renders to the empty string is
// dropped, so a template can write {{if .File}}--f={{.File}}{{end}} for an
// optional argument.
func renderAll(tplName, where string, in []string, data any) ([]string, error) {
	var out []string
	for i, s := range in {
		r, err := render(tplName, fmt.Sprintf("%s[%d]", where, i), s, data)
		if err != nil {
			return nil, err
		}
		if r != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

// BadPlaceholder is a typed command whose {{...}} placeholder names no
// field or does not parse.
type BadPlaceholder struct{ Err error }

func (e *BadPlaceholder) Error() string { return e.Err.Error() }
func (e *BadPlaceholder) Unwrap() error { return e.Err }

// CommandWords splits a typed command line into words (internal/cmdline)
// and renders each like a template arg, with the placeholders data has as
// fields ({{.Home}} for a struct with Home); any other placeholder is a
// *BadPlaceholder. Words that render empty are dropped, as for template
// args. Apps (with templateData) and jobs share it.
func CommandWords(tplName, line string, data any) ([]string, error) {
	words, err := cmdline.Split(line)
	if err != nil {
		return nil, err
	}
	out, err := renderAll(tplName, "spec.command", words, data)
	if err != nil {
		return nil, &BadPlaceholder{Err: err}
	}
	return out, nil
}

// checkData fills every placeholder, so only unknown names and broken
// syntax fail a check.
var checkData = templateData{
	BaseURL: "/u/check/", Port: 8080, File: homeDir + "/file", Home: homeDir, Source: SourceDir + "/" + cloneLink, Sample: homeDir + "/sample", Host: "example.com",
}

// CheckCommand reports whether line can run as a typed command: a
// cmdline error (*cmdline.SyntaxError or cmdline.ErrVariable) or a
// *BadPlaceholder. A blank line is fine here; whether one is needed is the
// template's userCommand.
func CheckCommand(line string) error {
	_, err := CommandWords("", line, checkData)
	return err
}

// AppPortOf is the port app serves on: its own for a template with userImage
// (when set), else the template's.
func AppPortOf(app *model.App, t *model.Template) int32 {
	return appPort(app.Port, t.Spec.UserImage, t.Spec.Port)
}

func appPort(own int32, userImage bool, templatePort int32) int32 {
	if userImage && own > 0 {
		return own
	}
	return templatePort
}

// RunFingerprint identifies the command, image, port, plain variables and
// Git source app asks for, or "" when it names none (pods built before
// these fields keep running).
// An app without a source hashes exactly what it did before sources existed,
// so upgrading replaces no pod; a changed ref (a preview's new commit)
// replaces it once.
func RunFingerprint(app *model.App) string {
	if app.Command == "" && app.Image == "" && app.Port == 0 && app.Source == nil && len(app.Env) == 0 {
		return ""
	}
	parts := []any{app.Command, app.Image, app.Port}
	if len(app.Env) > 0 {
		parts = append(parts, app.Env)
	}
	if s := app.Source; s != nil {
		parts = append(parts, s.URL, s.Ref, s.Path, s.Build)
	}
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// RestartMark identifies the restart request app carries, or "" when none was
// made. A pod built after the request holds the same mark, so it is replaced
// once per request.
func RestartMark(app *model.App) string {
	if app.RestartRequestedAt.IsZero() {
		return ""
	}
	return app.RestartRequestedAt.UTC().Format(time.RFC3339Nano)
}

// workDir is where a command runs: the folder of the chosen file (an
// absolute path in the container), else fallback, the home or the clone folder.
func workDir(file, fallback string) string {
	if file == "" {
		return fallback
	}
	return path.Dir(file)
}

// sampleHome is where a template's sample is copied to inside the container.
func sampleHome(t *model.Template) string {
	return path.Join(homeDir, SampleDir, t.Name)
}

// SampleFileOf is the home-relative file or folder an app started from the
// template's sample opens.
func SampleFileOf(t *model.Template) string {
	return path.Join(SampleDir, t.Name, t.Spec.Sample.Open)
}

// sampleInitContainers copies the template's sample into the home before the
// app starts. The copy never overwrites a file, so the user's edits survive a
// restart. cp does not create parent folders, so the first container makes
// the target. Both run as the app does, with the home mounted and nothing else.
func sampleInitContainers(t *model.Template, image string) []corev1.Container {
	dest := sampleHome(t)
	step := func(name string, argv ...string) corev1.Container {
		return corev1.Container{
			Name:            name,
			Image:           image,
			Command:         argv,
			SecurityContext: podspec.RestrictedContext(),
			VolumeMounts:    []corev1.VolumeMount{{Name: homeVolume, MountPath: homeDir}},
			// cp reports errors on stderr; without this the kubelet only
			// reads the (empty) termination file and the reason has no text.
			TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
		}
	}
	cp := step(model.SampleCopyContainer, "cp", "-rn", strings.TrimSuffix(t.Spec.Sample.Path, "/")+"/.", dest)
	if t.Spec.UsesPodFiles() {
		// The samples are pod files, not part of the image.
		cp.VolumeMounts = append(cp.VolumeMounts, podfiles.Mount())
	}
	return []corev1.Container{step("sample-dir", "mkdir", "-p", dest), cp}
}

// FixedUID is the uid workspaces run as when uids are not allocated.
const FixedUID int64 = podspec.FixedUID

// FilePath is the file the app opens as a path inside its container: under
// the home folder, or under the folder of the clone for an app that runs
// from Git. Empty when the app opens no file.
func FilePath(ws *model.App) string {
	if ws.File == "" {
		return ""
	}
	return path.Join(appFolder(ws), path.Clean("/"+ws.File))
}

// appFolder is the folder the app's file is relative to: the home folder,
// or the folder of the clone for an app that runs from Git.
func appFolder(ws *model.App) string {
	if ws.Source != nil {
		return sourceFolder(ws.Source)
	}
	return homeDir
}

// BuildPod returns the desired Pod for an app served at baseURL on
// publicHost (empty when the deployment does not set one), with storage laid
// out as layout says (which also names the uid it runs as) and size (the zero
// Size keeps the template's resources). An app with a Source clones it with
// img first; the file it opens is then inside the clone, not the home.
func BuildPod(ws *model.App, t *model.Template, baseURL, publicHost string, layout podstorage.Layout, size sizes.Size, img Images) (*corev1.Pod, error) {
	port := AppPortOf(ws, t)
	data := templateData{BaseURL: baseURL, Port: port, Home: homeDir, Host: publicHost}
	base := appFolder(ws)
	if ws.Source != nil {
		data.Source = base
	}
	if ws.File != "" {
		if err := homepath.Check(ws.File); err != nil {
			return nil, fmt.Errorf("spec.file: %w", err)
		}
		data.File = FilePath(ws)
	}
	var inits []corev1.Container
	if ws.Source != nil {
		var err error
		if inits, err = sourceInitContainers(ws, t, baseURL, img); err != nil {
			return nil, err
		}
	} else if t.Spec.Build != nil && data.File != "" {
		var err error
		if inits, err = withSiteConfig(append(inits, siteFromHome(t, data.File)), t); err != nil {
			return nil, err
		}
	}
	image := t.Spec.RunImage(img.Pixi)
	if image == "" && !t.Spec.UserImage {
		return nil, fmt.Errorf("workspace template %q: it runs in the platform's pixi image, and none is set (environments.pixiImage)", t.Name)
	}
	var samples []corev1.Container
	if ws.Sample {
		if t.Spec.Sample == nil || t.Spec.Sample.Path == "" {
			return nil, fmt.Errorf("workspace template %q: spec.sample is set on the workspace but the template has no sample folder to copy", t.Name)
		}
		samples = sampleInitContainers(t, image)
		data.Sample = sampleHome(t)
	}
	if t.Spec.UsesPodFiles() && img.Runner.Ref == "" {
		return nil, fmt.Errorf("workspace template %q: its pods get the platform's files, and the platform has no image to copy them from (the controller's --runner-image)", t.Name)
	}
	if t.Spec.UserImage && ws.Image == "" {
		return nil, fmt.Errorf("spec.image: template %q runs the image each app names, and this app names none", t.Name)
	}
	command, err := renderAll(t.Name, "command", t.Spec.Command, data)
	if err != nil {
		return nil, err
	}
	var args []string
	if ws.Command != "" {
		args, err = CommandWords(t.Name, ws.Command, data)
	} else {
		args, err = renderAll(t.Name, "args", t.Spec.Args, data)
	}
	if err != nil {
		return nil, err
	}
	probe, err := readinessProbe(t, data)
	if err != nil {
		return nil, err
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: ws.ObjectName(), Namespace: ws.Namespace, Labels: labels(ws)},
		Spec:       podspec.Base(ws.Owner, image, t.Spec.Resources),
	}
	spec := &pod.Spec
	spec.InitContainers = inits
	if ws.Source != nil {
		img.Runner.Pull(spec) // the source check
	}
	if t.Spec.UsesPodFiles() {
		podfiles.Apply(spec, img.Runner)
	}
	spec.InitContainers = append(spec.InitContainers, samples...)
	app := &spec.Containers[0]
	if ws.ParentID != "" {
		layout = layout.WithoutFolders()
	}
	layout.Apply(pod, app)
	annotations := pod.Annotations
	if annotations == nil {
		annotations = map[string]string{}
	}
	if key := size.Key(); key != "" {
		size.Apply(spec, app, t.Spec.Resources)
		annotations[model.AnnotationSize] = key
	}
	if fp := RunFingerprint(ws); fp != "" {
		annotations[model.AnnotationRun] = fp
	}
	if mark := RestartMark(ws); mark != "" {
		annotations[model.AnnotationRestart] = mark
	}
	if len(annotations) > 0 {
		pod.Annotations = annotations
	}
	if t.Spec.UserImage {
		app.Image = ws.Image
		// The image's own user runs it; the kubelet refuses one that is
		// root (runAsNonRoot), which status reports as ImageRunsAsRoot.
		// The home's group stays as a supplemental group: containerd refuses
		// runAsGroup without runAsUser.
		sc := spec.SecurityContext
		if sc.RunAsGroup != nil {
			sc.SupplementalGroups = append(sc.SupplementalGroups, *sc.RunAsGroup)
		}
		sc.RunAsUser, sc.RunAsGroup = nil, nil
	}
	if ws.Command != "" || t.Spec.UserImage || ws.Source != nil {
		app.Env = append(app.Env, corev1.EnvVar{Name: "PORT", Value: fmt.Sprint(port)})
	}
	if ws.Source != nil {
		app.Env = append(app.Env, corev1.EnvVar{Name: "BASE_URL", Value: baseURL})
	}
	if (ws.Command != "" && !t.Spec.UserImage) || ws.Source != nil {
		app.WorkingDir = workDir(data.File, base)
	}
	app.Command = command
	app.Args = args
	app.Ports = []corev1.ContainerPort{{ContainerPort: port}}
	app.ReadinessProbe = probe
	addSourceVolumes(spec, ws, t)
	if ws.ParentID != "" || t.Spec.Build != nil {
		// A preview runs a stranger's pull request, and nginx serving a built
		// site never needs the home.
		podspec.ScratchHome(spec)
	}
	return pod, nil
}

// readinessProbe is an HTTP GET of the template's readinessPath, or, for a
// template with userImage, a TCP connect: an image of any kind is ready once
// its port takes connections, while a GET of / may fairly answer 404.
func readinessProbe(t *model.Template, data templateData) (*corev1.Probe, error) {
	port := intstr.FromInt32(data.Port)
	if t.Spec.UserImage {
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: port}}}, nil
	}
	readyPath := t.Spec.ReadinessPath
	if readyPath == "" {
		readyPath = "{{.BaseURL}}"
	}
	probePath, err := render(t.Name, "readinessPath", readyPath, data)
	if err != nil {
		return nil, err
	}
	return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: probePath, Port: port}}}, nil
}

// BuildService returns the desired Service; its name equals the Pod's.
func BuildService(ws *model.App, t *model.Template) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: ws.ObjectName(), Namespace: ws.Namespace, Labels: labels(ws)},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{model.LabelApp: ws.ID},
			Ports: []corev1.ServicePort{{
				Port:       AppPortOf(ws, t),
				TargetPort: intstr.FromInt32(AppPortOf(ws, t)),
			}},
		},
	}
}

// Package workspace builds and reconciles the objects behind an app: its Pod
// and Service. It asks for the owner's home, which the storage controller
// turns into a claim.
package workspace

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"text/template"
	"time"

	"github.com/nebari-dev/javo/internal/cmdline"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podfiles"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/workload"
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

// sampleSteps copy the template's sample into the home before the app
// starts. The copy never overwrites a file, so the user's edits survive a
// restart. cp does not create parent folders, so the first step makes the
// target's parent; cp -T copies the folder as the target, the same in GNU
// and BusyBox cp (whose -n skips a whole folder that exists, so a restart
// there adds nothing). Both run as the app does, with the home mounted and
// nothing else.
func sampleSteps(t *model.Template, image string) []workload.Unit {
	dest := sampleHome(t)
	step := func(name string, argv ...string) workload.Unit {
		return workload.Unit{
			Name: name, Image: image, Command: argv, ReadOnlyRoot: true,
			Mounts: []workload.Mount{{Volume: homeVolume, Path: homeDir}},
		}
	}
	cp := step(model.SampleCopyContainer, "cp", "-rnT", strings.TrimSuffix(t.Spec.Sample.Path, "/"), dest)
	if t.Spec.UsesPodFiles() {
		// The samples are pod files, not part of the image.
		cp.Mounts = append(cp.Mounts, podfiles.ReadOnly())
	}
	return []workload.Unit{step("sample-dir", "mkdir", "-p", path.Dir(dest)), cp}
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

// LayoutFingerprint identifies what app's pod mounts and runs as under
// layout (each claim and whether the app sees it read-only, the group ids,
// the uid) and the fields of t its pod is built from, so a change of any of
// them (a person leaving a group, an app shared, a new template image)
// replaces the pod.
func LayoutFingerprint(app *model.App, t *model.Template, layout podstorage.Layout) string {
	if app.ParentID != "" {
		layout = layout.WithoutFolders()
	}
	if Shared(app) {
		layout = layout.ReadOnlyFiles()
	}
	type mount struct {
		Volume, Claim, Path, SubPath string
		ReadOnly                     bool
	}
	claims := map[string]string{}
	for _, v := range layout.Volumes {
		claims[v.Name] = v.Source
	}
	var mounts []mount
	for _, m := range layout.Mounts {
		mounts = append(mounts, mount{m.Volume, claims[m.Volume], m.Path, m.SubPath, m.ReadOnly})
	}
	s := t.Spec
	b, _ := json.Marshal([]any{
		mounts, layout.User.Groups, layout.User.UID, layout.User.GID,
		s.Kind, s.Image, s.Environment, s.ServesKernels, s.PodFiles, s.Port, s.Command, s.Args, s.UserCommand,
		s.UserImage, s.FromRepo, s.Build, s.ReadinessPath, s.Activity, s.Resources, s.Sample, s.BasePath, s.AppLogin,
	})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Shared is whether people other than ws's owner may open it. Their use of
// the app must not change files in the owner's home or group folders: those
// hold the code the owner's secret-holding jobs and apps run.
func Shared(ws *model.App) bool {
	return ws.Sharing.Access != "" && ws.Sharing.Access != model.AccessPrivate
}

// userImagePullSecrets refuses a workload that would pull the image a
// person named with the platform's registry credentials (a platform offers
// a workload's credentials for each of its images): those are for the
// platform's own images, and could open private images the person may not
// read. A template that runs people's images with platform steps from a
// private registry (an app from Git, Nebi) cannot run.
func userImagePullSecrets(s *workload.Spec, t *model.Template) error {
	if !t.Spec.UserImage || len(s.PullSecrets) == 0 {
		return nil
	}
	return fmt.Errorf("template %q runs the image each app names, and this app also needs the platform's own containers, which pull with the platform's registry credentials; those are never offered for a person's image. Use a template without fromRepo or nebiToken, or a public platform image", t.Name)
}

// readyCheck is an HTTP GET of the template's readinessPath, or, for a
// template with userImage, a TCP connect: an image of any kind is ready once
// its port takes connections, while a GET of / may fairly answer 404.
func readyCheck(t *model.Template, data templateData) (*workload.Check, error) {
	if t.Spec.UserImage {
		return &workload.Check{TCP: true}, nil
	}
	probePath, err := render(t.Name, "readinessPath", cmp.Or(t.Spec.ReadinessPath, "{{.BaseURL}}"), data)
	if err != nil {
		return nil, err
	}
	return &workload.Check{HTTPPath: probePath}, nil
}

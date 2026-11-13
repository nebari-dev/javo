package workspace

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/homepath"
	"github.com/nebari-dev/javo/internal/model"
)

// Run is what an app's container runs.
type Run struct {
	Image string
	// Command is the container's command then its args, placeholders filled
	// in; empty when the image's own command runs.
	Command []string
}

// Platform is what an app's run takes from the install besides the app and
// its template. The controller builds pods with it and the API shows runs
// with it, so the two cannot differ.
type Platform struct {
	Exposure exposure.Backend
	// PublicHost is the host browsers reach the gateway at (the controller's
	// --public-host); empty when not configured.
	PublicHost string
	// PixiImage runs templates with an environment (environments.pixiImage).
	PixiImage string
}

// address is the base path app serves under and the host browsers load it
// from: its own host, else the public host.
func (p Platform) address(app *model.App) (baseURL, host string) {
	u := p.Exposure.URLFor(app.Owner, app.Name, app.ObjectName())
	return p.Exposure.BasePath(app.Owner, app.Name), cmp.Or(u.Host, p.PublicHost)
}

// RunOf is what app's pod runs on template t: the image and the command
// line BuildPod puts in its container.
func (p Platform) RunOf(app *model.App, t *model.Template) (Run, error) {
	base, host := p.address(app)
	r, err := runOf(app, t, base, host, p.PixiImage, 0, containerPlaces)
	if err != nil {
		return Run{}, err
	}
	run := Run{Image: r.image, Command: slices.Concat(r.command, r.args)}
	if t.Spec.UserImage {
		run.Image = app.Image
	}
	return run, nil
}

// runPlan is the part of an app's pod its run decides: the port, the
// placeholder values, the template's image and the rendered command and args.
type runPlan struct {
	port          int32
	data          templateData
	image         string
	command, args []string
}

// runOf renders ws's command on t for an app served at baseURL on
// publicHost, with pixi the platform's pixi image. The app serves on port,
// or on the template's when that is zero, and finds its files at the
// places at.
func runOf(ws *model.App, t *model.Template, baseURL, publicHost, pixi string, port int32, at places) (runPlan, error) {
	port = cmp.Or(port, AppPortOf(ws, t))
	data := templateData{BaseURL: baseURL, Port: port, Home: at.home, Host: publicHost}
	if ws.Source != nil {
		data.Source = at.appFolder(ws)
	}
	if ws.File != "" {
		if err := homepath.Check(ws.File); err != nil {
			return runPlan{}, fmt.Errorf("spec.file: %w", err)
		}
		data.File = at.filePath(ws)
	}
	image := t.Spec.RunImage(pixi)
	if image == "" && !t.Spec.UserImage && at.host == nil {
		return runPlan{}, fmt.Errorf("workspace template %q: it runs in the platform's pixi image, and none is set (environments.pixiImage)", t.Name)
	}
	if ws.Sample {
		if t.Spec.Sample == nil || t.Spec.Sample.Path == "" {
			return runPlan{}, fmt.Errorf("workspace template %q: spec.sample is set on the workspace but the template has no sample folder to copy", t.Name)
		}
		data.Sample = at.sampleHome(t)
	}
	if t.Spec.UserImage && ws.Image == "" {
		return runPlan{}, fmt.Errorf("spec.image: template %q runs the image each app names, and this app names none", t.Name)
	}
	command, err := renderAll(t.Name, "command", at.words(t.Spec.Command), data)
	if err != nil {
		return runPlan{}, err
	}
	var args []string
	if ws.Command != "" {
		args, err = commandWords(t.Name, ws.Command, data, at)
	} else {
		args, err = renderAll(t.Name, "args", at.words(t.Spec.Args), data)
	}
	if err != nil {
		return runPlan{}, err
	}
	return runPlan{port: port, data: data, image: image, command: command, args: args}, nil
}

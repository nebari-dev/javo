package templates

import (
	"strings"

	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/model"
)

// PodRun is what an app's container runs. Placeholders hold only paths,
// the port and the public host; variables and secrets are never part of it.
type PodRun struct {
	Image   string   `json:"image" doc:"Image the app's container runs"`
	Command []string `json:"command" doc:"The container's command then its args, placeholders filled in; empty when the image's own command runs"`
	Line    string   `json:"line" doc:"Command as one shell-quoted line; empty when the image's own command runs"`
	// Environment says which environment the app runs in; empty for the image's own.
	Environment string `json:"environment,omitempty" doc:"Which environment the app runs in, in words; empty for the image's own"`
}

// PodRunOf is what app's pod runs on t (workspace.Platform.RunOf); nil
// when the pod cannot be built (its status says why).
func PodRunOf(app *model.App, t *model.Template, p workspace.Platform) *PodRun {
	r, err := p.RunOf(app, t)
	if err != nil {
		return nil
	}
	return &PodRun{Image: r.Image, Command: r.Command, Line: shellLine(r.Command), Environment: workspace.EnvironmentOf(app, t)}
}

// The values a template's run is previewed with, before the person picks
// them; plain words, so quoting leaves them whole.
const (
	phFile    = "JAVOPLACEHOLDERFILE"
	phApp     = "javoplaceholderapp"
	phCommand = "JAVOPLACEHOLDERCOMMAND"
	phImage   = "JAVOPLACEHOLDERIMAGE"
)

var placeholders = strings.NewReplacer(phFile, "<your file>", phApp, "<app name>", phCommand, "<your command>", phImage, "<your image>")

// previewRun is what an app of owner's on t would run, with what the person
// picks (file, typed command, image) and the app's name as placeholders.
func previewRun(t *model.Template, owner string, p workspace.Platform) *PodRun {
	app := &model.App{Owner: owner, Name: phApp, Template: t.Name}
	if len(t.Spec.FileExtensions) > 0 {
		app.File = phFile
	}
	if t.Spec.UserCommand == model.UserCommandRequired {
		app.Command = phCommand
	}
	if t.Spec.UserImage {
		app.Image = phImage
	}
	v := PodRunOf(app, t, p)
	if v == nil {
		return nil
	}
	v.Image, v.Line = placeholders.Replace(v.Image), placeholders.Replace(v.Line)
	for i, w := range v.Command {
		v.Command[i] = placeholders.Replace(w)
	}
	return v
}

// shellLine writes words as one line a POSIX shell reads back as the same
// words, single-quoting only a word with a character outside the plain set.
func shellLine(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		if w != "" && strings.Trim(w, plainChars) == "" {
			out[i] = w
		} else {
			out[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
		}
	}
	return strings.Join(out, " ")
}

const plainChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-"

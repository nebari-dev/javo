package workspace

import (
	"cmp"
	"context"
	"errors"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/workload"
)

// managerName is how the person knows the environment manager with id.
func managerName(id string) string {
	if id == "nebi" {
		return "Nebi"
	}
	return id
}

// EnvironmentOf says in words which environment app runs in on t, the
// way withEnvironment installs it; empty when it runs in the image's own.
func EnvironmentOf(app *model.App, t *model.Template) string {
	def := t.Spec.Environment
	if e := app.Environment; e != nil {
		picked := e.Name
		if e.Version != "" {
			picked += ", version " + e.Version + ","
		}
		picked += " from " + managerName(e.Source)
		if t.Spec.ServesKernels && def != "" {
			return "The template's environment " + def + ", with " + picked + " beside it"
		}
		return picked
	}
	switch {
	case def == "":
		return ""
	case app.Source != nil:
		return "The repository's pixi.lock when it has one, else the template's environment " + def
	}
	return "The template's environment " + def
}

// addEnvironment adds the step that installs the app's environment: the
// one picked for it, else (on a template with an environment of its own)
// the pixi.lock at the root of its repository when there is one, else the
// template's (internal/pixienv); on a template that serves kernels the
// picked one goes beside the template's instead. An environment that
// replaces the template's must have the program the app starts. None for an
// app on a template without one and nothing picked.
func addEnvironment(s *workload.Spec, ws *model.App, t *model.Template, in Inputs) error {
	src := pixienv.Source{Default: t.Spec.Environment, Beside: t.Spec.ServesKernels}
	if ws.Source != nil {
		src.RepoDir = sourceRoot
	}
	if ws.Environment != nil {
		if in.EnvFiles == nil {
			return errors.New("environment: the files of " + ws.Environment.Name + " are missing; pick the environment again")
		}
		src.Files = in.EnvFiles
		src.Name, src.From = ws.Environment.Name, managerName(ws.Environment.Source)
	}
	src.Program = pixienv.ProgramOf(s.Main.Command, s.Main.Args)
	return pixienv.Add(s, in.Images.Pixi, in.Images.Runner, src)
}

// environmentFiles are the files of the environment picked for app, nil
// when none is picked or they are gone (Plan then says so). A preview
// installs its parent's.
func (r *Reconciler) environmentFiles(ctx context.Context, app *model.App) (*model.EnvironmentFiles, error) {
	if app.Environment == nil {
		return nil, nil
	}
	files, err := r.Store.Environments().ForApp(ctx, cmp.Or(app.ParentID, app.ID))
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return &files, nil
}

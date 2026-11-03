package workspace

import (
	"cmp"
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/store"
)

// managerName is how the person knows the environment manager with id.
func managerName(id string) string {
	if id == "nebi" {
		return "Nebi"
	}
	return id
}

// withEnvironment adds to pod the step that installs the app's environment:
// the one picked for it, else (on a template with an environment of its
// own) the pixi.lock at the root of its repository when there is one, else
// the template's (internal/pixienv); on a template that serves kernels the
// picked one goes beside the template's instead. An environment that
// replaces the template's must have the program the app starts. None for an app on a template without
// one and nothing picked. A preview installs its parent's picked files.
// problem is why the app cannot start (its files are gone or do not fit, or
// the platform has no image to install with); err is a failed read.
func (r *Reconciler) withEnvironment(ctx context.Context, app *model.App, tpl *model.Template, pod *corev1.Pod) (problem string, err error) {
	src := pixienv.Source{Default: tpl.Spec.Environment}
	if app.Source != nil {
		src.RepoDir = sourceRoot
	}
	if app.Environment != nil {
		id := cmp.Or(app.ParentID, app.ID)
		files, err := r.Store.Environments().ForApp(ctx, id)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return "environment: the files of " + app.Environment.Name + " are missing; pick the environment again", nil
		case err != nil:
			return "", err
		}
		src.Files = &files
		src.Name, src.From = app.Environment.Name, managerName(app.Environment.Source)
	}
	src.Beside = tpl.Spec.ServesKernels
	if c := pod.Spec.Containers; len(c) > 0 {
		src.Program = pixienv.ProgramOf(c[0].Command, c[0].Args)
	}
	if err := pixienv.Apply(&pod.Spec, r.PixiImage, r.RunnerImage, src); err != nil {
		return err.Error(), nil
	}
	return "", nil
}

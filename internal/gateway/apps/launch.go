package apps

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/cmdline"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/envvars"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/imageref"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// launch is what an app runs beyond its template: a typed command, and for
// a template with userImage the image and port. Create and change check it
// the same way (checkLaunch).
type launch struct {
	command, image string
	port           int32
}

// checkLaunch trims l and checks it against template t: the command against
// t's userCommand and the shell-word rules, the image and port against
// userImage and the allowed registries. A userImage app without a port gets
// t's port. Every problem is a field error; nothing is stored on error.
func (s *Workspaces) checkLaunch(t *model.Template, l launch) (launch, error) {
	l.command, l.image = strings.TrimSpace(l.command), strings.TrimSpace(l.image)
	var errs []problem.Field
	bad := func(field string, code reasons.MessageCode, args ...any) {
		errs = append(errs, problem.Field{Name: field, Msg: reasons.Msg(code, args...)})
	}
	switch mode := t.Spec.UserCommand; {
	case l.command == "" && mode == model.UserCommandRequired:
		bad("command", reasons.MsgCommandRequired)
	case l.command == "":
	case mode != model.UserCommandRequired && mode != model.UserCommandOptional:
		bad("command", reasons.MsgCommandNotTaken)
	case len(l.command) > model.MaxCommandLength:
		bad("command", reasons.MsgCommandTooLong)
	default:
		if err := workspace.CheckCommand(l.command); err != nil {
			errs = append(errs, commandError(err))
		}
	}
	if !t.Spec.UserImage {
		if l.image != "" {
			bad("image", reasons.MsgImageNotTaken)
		}
		if l.port != 0 {
			bad("port", reasons.MsgPortNotTaken)
		}
	} else {
		var na *imageref.NotAllowedError
		switch err := imageref.Check(l.image, s.images.AllowedRegistries); {
		case !s.images.IsEnabled():
			bad("template", reasons.MsgTemplateUnavailable)
		case l.image == "":
			bad("image", reasons.MsgImageRequired)
		case errors.As(err, &na):
			bad("image", reasons.MsgImageRegistryNotAllowed, na.Registry, strings.Join(s.images.AllowedRegistries, ", "))
		case err != nil:
			bad("image", reasons.MsgImageInvalid)
		}
		if l.port == 0 {
			l.port = t.Spec.Port
		}
	}
	if len(errs) > 0 {
		return launch{}, problem.Fields(errs...)
	}
	return l, nil
}

// commandError is the command field's message for a CheckCommand error.
func commandError(err error) problem.Field {
	var syn *cmdline.SyntaxError
	switch {
	case errors.Is(err, cmdline.ErrVariable):
		return problem.Field{Name: "command", Msg: reasons.Msg(reasons.MsgCommandVariable)}
	case errors.As(err, &syn):
		return problem.Field{Name: "command", Msg: reasons.Msg(reasons.MsgCommandInvalid, syn.Detail)}
	}
	return problem.Field{Name: "command", Msg: reasons.Msg(reasons.MsgCommandPlaceholder)}
}

type launchInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
	Body  LaunchBody
}

// LaunchBody is the part of a create request that launch covers, and the
// whole body of a change.
type LaunchBody struct {
	Command string `json:"command,omitempty" maxLength:"2048" doc:"Command line, for templates whose userCommand is Optional or Required; split into words like a shell, with {{.Port}}, {{.BaseURL}}, {{.Home}}, {{.File}} and {{.Source}} filled in"`
	Image   string `json:"image,omitempty" maxLength:"512" doc:"Image reference, for templates with userImage"`
	Port    int32  `json:"port,omitempty" minimum:"0" maximum:"65535" doc:"Port the image serves on, for templates with userImage; 0 means the template's port"`
}

func (b LaunchBody) launch() launch { return launch{command: b.Command, image: b.Image, port: b.Port} }

func (s *Workspaces) registerLaunch(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "setWorkspaceLaunch", Method: http.MethodPut, Path: "/api/workspaces/{name}/launch",
		Summary: "Change an app's command, and for an image app its image and port (a running app restarts)",
		Tags:    []string{"workspaces"},
		Errors:  []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *launchInput) (*workspaceOutput, error) {
		app, err := s.authorizedEdit(ctx, &nameInput{Name: in.Name, Owner: in.Owner}, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		t, err := s.db.Templates().Get(ctx, app.Template)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil, problem.FieldError("template", reasons.MsgTemplateUnavailable)
		case err != nil:
			return nil, problem.Internal("read template", err)
		}
		l, err := s.checkLaunch(&t, in.Body.launch())
		if err != nil {
			return nil, err
		}
		if app.Command != l.command || app.Image != l.image || app.Port != l.port {
			u := authn.User(ctx)
			if err := envvars.MayChangeCode(ctx, u, s.rolesOf(u), authz.Object{App: app}); err != nil {
				return nil, err
			}
			if app, err = s.updateApp(ctx, app, func(a *model.App) { a.Command, a.Image, a.Port = l.command, l.image, l.port }); err != nil {
				return nil, err
			}
		}
		return s.output(ctx, app)
	})
}

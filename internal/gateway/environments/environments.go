// Package environments lists the pixi environments of the platform's
// environment manager and copies in the one an app or job picks.
package environments

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/ext/envsource"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/reasons"
)

// EnvironmentPick names the environment an app or a job runs in, from GET
// /api/environments. Its newest version is copied in when the app or job is
// created; a run never asks the environment manager again.
type EnvironmentPick struct {
	ID string `json:"id" minLength:"1" maxLength:"255" doc:"An id from GET /api/environments"`
}

// EnvironmentView is the environment an app or a job runs in.
type EnvironmentView struct {
	Source  string `json:"source" doc:"The environment manager it came from, such as nebi"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty" doc:"The version that was copied in"`
}

// ViewOf is the view of a stored environment; nil for none.
func ViewOf(e *model.Environment) *EnvironmentView {
	if e == nil {
		return nil
	}
	return &EnvironmentView{Source: e.Source, ID: e.ID, Name: e.Name, Version: e.Version}
}

// EnvironmentList is what GET /api/environments answers.
type EnvironmentList struct {
	// Available is false when the platform has no environment manager: the
	// forms then offer nothing.
	Available bool `json:"available"`
	// Message says why Items is empty when the manager could not be asked.
	Message string                  `json:"message,omitempty"`
	Items   []envsource.Environment `json:"items"`
}

type environmentListOutput struct{ Body EnvironmentList }

// API offers the environments of the platform's environment
// manager, as the signed-in person: Source is nil when there is none, IDToken
// is the person's OpenID Connect ID token (empty without one).
type API struct {
	Source  envsource.Source
	IDToken func(context.Context) (string, error)
}

// Register adds GET /api/environments.
func (e *API) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "listEnvironments", Method: http.MethodGet, Path: "/api/environments",
		Summary:     "List the environments you can run an app or a job in",
		Description: "Environments from the platform's environment manager (Nebi) that you own or that are shared with you, asked as you. available is false when the platform has none.",
		Tags:        []string{"environments"},
		Errors:      []int{http.StatusUnauthorized},
	}, e.list)
}

func (e *API) list(ctx context.Context, _ *struct{}) (*environmentListOutput, error) {
	out := &environmentListOutput{Body: EnvironmentList{Items: []envsource.Environment{}}}
	if e.Source == nil {
		return out, nil
	}
	out.Body.Available = true
	tok, code, err := e.token(ctx)
	if err != nil {
		return nil, err
	}
	if code == "" {
		var items []envsource.Environment
		if items, err = e.Source.List(ctx, tok); err == nil {
			out.Body.Items = items
			return out, nil
		}
		code = sourceError(err)
	}
	out.Body.Message = reasons.Msg(code)
	return out, nil
}

// token is the person's ID token, or the message code saying why there is none.
func (e *API) token(ctx context.Context) (string, reasons.MessageCode, error) {
	tok, err := e.IDToken(ctx)
	if err != nil {
		return "", "", problem.Internal("renew the sign-in", err)
	}
	if tok == "" {
		return "", reasons.MsgEnvironmentSignIn, nil
	}
	return tok, "", nil
}

// sourceError is the message for a failed call to the manager.
func sourceError(err error) reasons.MessageCode {
	switch {
	case errors.Is(err, envsource.ErrRefused):
		return reasons.MsgEnvironmentRefused
	case errors.Is(err, envsource.ErrNotFound):
		return reasons.MsgEnvironmentUnknown
	}
	slog.Warn("environment manager", "err", err)
	return reasons.MsgEnvironmentUnreachable
}

// Pick copies in the newest version of the environment p names, as the
// person: the spec field to store and its files. A nil p is no environment.
// Every refusal is an error on the environment field.
func (e *API) Pick(ctx context.Context, p *EnvironmentPick) (*model.Environment, *model.EnvironmentFiles, error) {
	if p == nil {
		return nil, nil, nil
	}
	if e.Source == nil {
		return nil, nil, problem.FieldError("environment", reasons.MsgEnvironmentsOff)
	}
	tok, code, err := e.token(ctx)
	if err != nil {
		return nil, nil, err
	}
	if code != "" {
		return nil, nil, problem.FieldError("environment", code)
	}
	got, err := e.Source.Fetch(ctx, tok, p.ID)
	if err != nil {
		return nil, nil, problem.FieldError("environment", sourceError(err))
	}
	files := &model.EnvironmentFiles{Manifest: got.Manifest, Lock: got.Lock}
	if err := pixienv.Fits(*files); err != nil {
		return nil, nil, problem.FieldError("environment", reasons.MsgEnvironmentTooLarge, err.Error())
	}
	env := &model.Environment{Source: e.Source.ID(), ID: got.ID, Name: got.Name, Version: got.Version, Digest: files.Digest()}
	return env, files, nil
}

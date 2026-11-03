package apps

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/gateway/sizing"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

func (s *Workspaces) registerSizes(api huma.API) {
	s.Rules.RegisterList(api)

	huma.Register(api, huma.Operation{
		OperationID: "setWorkspaceSize", Method: http.MethodPut, Path: "/api/workspaces/{name}/size",
		Summary: "Change an app's size or custom amount (a running app restarts)", Tags: []string{"workspaces"},
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *sizing.SizeInput) (*workspaceOutput, error) {
		app, err := s.authorizedEdit(ctx, &nameInput{Name: in.Name, Owner: in.Owner}, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		if in.Body.Size == "" && in.Body.Resources == nil {
			return nil, problem.FieldError("size", reasons.MsgSizeUnavailable)
		}
		sz, err := s.Allowed(ctx, in.Body.Size, in.Body.Resources)
		if err != nil {
			return nil, err
		}
		if !sizing.Same(app.Size, app.Resources, sz) {
			if app, err = s.updateApp(ctx, app, func(a *model.App) { a.Size, a.Resources = sz.Stored() }); err != nil {
				return nil, err
			}
		}
		return s.output(ctx, app)
	})
}

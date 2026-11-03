package apps

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
)

// checkIdleStop refuses an idle stop timeout (minutes, 0 is Off) the admin's
// policy does not allow u; authz.Decide holds the rule.
func (s *Workspaces) checkIdleStop(u authz.Principal, minutes int32) error {
	ask := &authz.IdleAsk{Minutes: minutes, Policy: s.idle}
	if d := authz.Decide(u, authz.RolesOf(u, s.Roles), authz.ActIdleStop, authz.Object{Idle: ask}); !d.Allowed {
		return problem.FieldError("idleStopMinutes", d.Code, d.Args...)
	}
	return nil
}

// idleStopFor is the timeout a new app gets: the one asked for, checked, or
// the admin's default.
func (s *Workspaces) idleStopFor(u authz.Principal, asked *int32) (int32, error) {
	if asked == nil {
		return s.idle.DefaultMinutes, nil
	}
	return *asked, s.checkIdleStop(u, *asked)
}

// IdleStopPolicy is the admin's "Stop when idle" policy as it applies to the
// caller.
type IdleStopPolicy struct {
	// DefaultMinutes is what a new app gets when none is picked; 0 is Off.
	DefaultMinutes int32 `json:"defaultMinutes"`
	MinMinutes     int32 `json:"minMinutes"`
	MaxMinutes     int32 `json:"maxMinutes"`
	// CanTurnOff is whether the caller may pick Off.
	CanTurnOff bool `json:"canTurnOff"`
}

type idleStopPolicyOutput struct{ Body IdleStopPolicy }

type idleStopInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
	Body  struct {
		IdleStopMinutes int32 `json:"idleStopMinutes" minimum:"0" doc:"Stop the app after this many minutes without use (requests and open connections through the gateway); 0 is Off. GET /api/idle-stop gives the allowed range."`
	}
}

func (s *Workspaces) registerIdleStop(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "getIdleStopPolicy", Method: http.MethodGet, Path: "/api/idle-stop",
		Summary: "The allowed \"Stop when idle\" timeouts and the default for new apps",
		Tags:    []string{"workspaces"}, Errors: []int{http.StatusUnauthorized},
	}, func(ctx context.Context, _ *struct{}) (*idleStopPolicyOutput, error) {
		u := authn.User(ctx)
		return &idleStopPolicyOutput{Body: IdleStopPolicy{
			DefaultMinutes: s.idle.DefaultMinutes, MinMinutes: s.idle.MinMinutes, MaxMinutes: s.idle.MaxMinutes,
			CanTurnOff: s.checkIdleStop(u, 0) == nil,
		}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "setWorkspaceIdleStop", Method: http.MethodPut, Path: "/api/workspaces/{name}/idle-stop",
		Summary: "Change when an app stops for being idle (a running app keeps running)",
		Tags:    []string{"workspaces"},
		Errors:  []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *idleStopInput) (*workspaceOutput, error) {
		app, err := s.authorized(ctx, &nameInput{Name: in.Name, Owner: in.Owner}, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		minutes := in.Body.IdleStopMinutes
		if err := s.checkIdleStop(authn.User(ctx), minutes); err != nil {
			return nil, err
		}
		if app.IdleStopMinutes != minutes {
			if app, err = s.updateApp(ctx, app, func(a *model.App) { a.IdleStopMinutes = minutes }); err != nil {
				return nil, err
			}
		}
		return s.output(ctx, app)
	})
}

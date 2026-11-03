package admin

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// HeldRestore is one restore whose apps are waiting for an admin.
type HeldRestore struct {
	Name string `json:"name" doc:"The restore: the backup file name for a whole-database restore"`
	// Held counts the apps still waiting, Scheduled those of them released
	// to start later, WasRunning those that were running when backed up.
	Held       int `json:"held"`
	Scheduled  int `json:"scheduled"`
	WasRunning int `json:"wasRunning"`
}

type restoresOutput struct {
	Body struct {
		Items []HeldRestore `json:"items"`
	}
}

type releaseInput struct {
	Name string `path:"name" maxLength:"253"`
	Body struct {
		Mode      string `json:"mode" enum:"start,stopped" doc:"start: apps that were running start at perMinute; stopped: every app stays stopped"`
		PerMinute int    `json:"perMinute,omitempty" minimum:"1" maximum:"600" default:"10"`
	}
}

type releaseOutput struct {
	Body struct {
		Released int `json:"released"`
	}
}

func (a *API) registerRestores(api huma.API) {
	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminListRestores", Method: http.MethodGet, Path: "/api/admin/restores",
		Summary: "Restores whose apps wait to be released (platform admins)",
	}, func(ctx context.Context, _ *struct{}) (*restoresOutput, error) {
		now := a.Now()
		held, err := a.DB.Apps().ListHeld(ctx, now)
		if err != nil {
			return nil, problem.Internal("list held apps", err)
		}
		out := &restoresOutput{}
		out.Body.Items = []HeldRestore{}
		for _, app := range held {
			n := len(out.Body.Items)
			if n == 0 || out.Body.Items[n-1].Name != app.RestoreHold {
				out.Body.Items = append(out.Body.Items, HeldRestore{Name: app.RestoreHold})
				n++
			}
			r := &out.Body.Items[n-1]
			r.Held++
			if !app.ReleaseAt.IsZero() {
				r.Scheduled++
			}
			if !app.Paused {
				r.WasRunning++
			}
		}
		return out, nil
	})
	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminReleaseRestore", Method: http.MethodPost, Path: "/api/admin/restores/{name}/release",
		Summary: "Let a restore's held apps go (platform admins)",
		Errors:  []int{http.StatusNotFound},
	}, func(ctx context.Context, in *releaseInput) (*releaseOutput, error) {
		per := in.Body.PerMinute
		if per == 0 {
			per = 10
		}
		n, err := a.DB.Apps().Release(ctx, authn.Actor(ctx), in.Name, store.ReleaseMode(in.Body.Mode), per, a.Now())
		if err != nil {
			return nil, problem.Internal("release restored apps", err)
		}
		if n == 0 {
			return nil, problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgRestoreNotFound))
		}
		out := &releaseOutput{}
		out.Body.Released = n
		return out, nil
	})
}

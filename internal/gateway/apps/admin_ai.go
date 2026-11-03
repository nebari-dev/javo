package apps

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/admin"
	"github.com/nebari-dev/javo/internal/ai"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/reasons"
)

type reportOutput struct{ Body admin.ClusterReport }

type askInput struct {
	Body struct {
		Question string `json:"question" minLength:"1" maxLength:"1000"`
	}
}

type askOutput struct {
	RetryAfter string `header:"Retry-After"`
	Body       ai.AdminAnswer
}

// adminReport is the cluster report. The routes are registered with
// authn.RegisterAdmin, which refuses anyone but an admin before this runs.
func (a *aiAPI) adminReport(ctx context.Context) (admin.ClusterReport, error) {
	apps, err := a.ws.db.Apps().ListAll(ctx)
	if err != nil {
		return admin.ClusterReport{}, problem.Internal("list apps", err)
	}
	return admin.Build(apps, time.Now(), a.includeOwners), nil
}

func (a *aiAPI) registerAdmin(api huma.API) {
	authn.RegisterAdmin(api, a.ws.Roles, huma.Operation{
		OperationID: "getAdminReport", Method: http.MethodGet, Path: "/api/admin/report",
		Summary: "Platform admins: failures by cause, apps by state and template, idle apps",
	}, func(ctx context.Context, _ *struct{}) (*reportOutput, error) {
		r, err := a.adminReport(ctx)
		if err != nil {
			return nil, err
		}
		return &reportOutput{Body: r}, nil
	})

	authn.RegisterAdmin(api, a.ws.Roles, huma.Operation{
		OperationID: "askAdmin", Method: http.MethodPost, Path: "/api/admin/ai/ask",
		Summary: "Platform admins: ask about the report; the answer cites its rows and changes nothing",
	}, func(ctx context.Context, in *askInput) (*askOutput, error) {
		r, err := a.adminReport(ctx)
		if err != nil {
			return nil, err
		}
		if !a.on(a.features.Admin) {
			return &askOutput{Body: ai.AdminAnswer{Refs: []string{}, Message: reasons.Msg(reasons.MsgAIOff)}}, nil
		}
		ans := a.svc.AskAdmin(ctx, authn.User(ctx).ID, in.Body.Question, r)
		out := &askOutput{Body: ans}
		out.RetryAfter = retryAfter(ans.RetryAfter)
		return out, nil
	})
}

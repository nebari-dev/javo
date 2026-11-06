package admin

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/gateway/apps"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
)

// AgentSettings is the platform admin's switch for trusted coding agents.
type AgentSettings struct {
	AllowTrusted bool `json:"allowTrusted" doc:"Owners may let their coding agents act without asking; false refuses it and ends it where it is on"`
	ConfigAllows bool `json:"configAllows" doc:"agents.allowTrusted in the platform config; false: it is off whatever is set here"`
}

type agentSettingsOutput struct{ Body AgentSettings }

type agentSettingsInput struct {
	Body struct {
		AllowTrusted bool `json:"allowTrusted"`
	}
}

func (a *API) registerAgents(api huma.API) {
	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminGetAgentSettings", Method: http.MethodGet, Path: "/api/admin/agents",
		Summary: "Whether owners may let their coding agents act without asking (platform admins)",
	}, func(ctx context.Context, _ *struct{}) (*agentSettingsOutput, error) {
		return a.agentSettings(ctx)
	})

	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminSetAgentSettings", Method: http.MethodPut, Path: "/api/admin/agents",
		Summary:     "Allow or refuse letting coding agents act without asking (platform admins)",
		Description: "Off, no owner can turn it on, and every agent acting without asking goes back to asking at once. Each change is on the audit log.",
	}, func(ctx context.Context, in *agentSettingsInput) (*agentSettingsOutput, error) {
		actor := authn.Actor(ctx)
		if err := a.DB.AgentPolicy().SetTrustedOff(ctx, actor, !in.Body.AllowTrusted); err != nil {
			return nil, problem.Internal("save the agent settings", err)
		}
		if !in.Body.AllowTrusted {
			if _, err := apps.EndTrust(ctx, a.DB, actor, true, a.Now()); err != nil {
				return nil, problem.Internal("end trusted agents", err)
			}
		}
		return a.agentSettings(ctx)
	})
}

func (a *API) agentSettings(ctx context.Context) (*agentSettingsOutput, error) {
	off, err := a.DB.AgentPolicy().TrustedOff(ctx)
	if err != nil {
		return nil, problem.Internal("read the agent settings", err)
	}
	return &agentSettingsOutput{Body: AgentSettings{AllowTrusted: !off, ConfigAllows: a.Trust.Config}}, nil
}

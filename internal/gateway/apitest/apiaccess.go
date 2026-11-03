package apitest

import (
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
)

type AccessJSON struct {
	WorkspaceJSON
	APIAccess string `json:"apiAccess"`
}

// AgentTemplate gives its apps read and write API access by default.
func AgentTemplate() *model.Template {
	t := gatewaytest.Template("agent", "claude-code")
	t.Spec.APIToken = true
	return t
}

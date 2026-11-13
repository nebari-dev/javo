package workspace

import (
	"cmp"
	"context"
	"errors"
	"strings"

	"github.com/nebari-dev/javo/internal/ai/agentproxy"
	"github.com/nebari-dev/javo/internal/controller/secretenv"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/workload"
)

// The variables a coding-agent app (a template with agentModel) gets, for
// its start script to set the agent up (pod/files/agents/pi-setup.mjs).
const (
	envAgentMode    = "JAVO_AGENT_MODE"
	envAgentModel   = "JAVO_AGENT_MODEL"
	envAgentBaseURL = "JAVO_AGENT_BASE_URL"
	envAgentKey     = secretenv.AgentKeyEnv
	envAgentPrompt  = "JAVO_AGENT_PROMPT"
)

// agentModel is where a coding-agent app of owner gets its model
// (model.AgentMode): the admin's through the gateway, else the owner's own
// setting and key, which is returned sealed for the app's Secret.
type agentModel struct {
	mode, model, baseURL string
	key                  *store.SealedBinding
}

func (r *Reconciler) agentModelOf(ctx context.Context, app *model.App, tpl *model.Template) (agentModel, error) {
	if !tpl.Spec.AgentModel {
		return agentModel{}, nil
	}
	platform := ""
	if r.AgentModel != nil {
		m, err := r.AgentModel(ctx)
		if err != nil {
			return agentModel{}, err
		}
		platform = m
	}
	if platform != "" {
		base := cmp.Or(r.APIInternalURL, r.APIURL)
		return agentModel{mode: model.AgentModePlatform, model: platform, baseURL: strings.TrimRight(base, "/") + agentproxy.Route}, nil
	}
	own, err := r.Store.AgentSettings().Get(ctx, app.Owner)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return agentModel{mode: model.AgentModeLogin}, nil
	case err != nil:
		return agentModel{}, err
	}
	sec, err := r.Store.Secrets().Get(ctx, app.Owner, model.AgentKeySecret)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return agentModel{mode: model.AgentModeLogin}, nil
	case err != nil:
		return agentModel{}, err
	}
	sealed, err := r.Store.Secrets().Sealed(ctx, sec.ID)
	if err != nil {
		return agentModel{}, err
	}
	return agentModel{mode: model.AgentModeOwn, model: own.Model, baseURL: own.BaseURL,
		key: &store.SealedBinding{Env: envAgentKey, Secret: sec, Ciphertext: sealed}}, nil
}

// addAgent gives the main unit of a coding agent (a template with
// agentModel) its model and first prompt. On the platform's model the key
// is the app's own API token; the owner's own key comes with the app's
// other secrets (addAppSecrets).
func addAgent(s *workload.Spec, ws *model.App, t *model.Template, m agentModel, hasToken bool) {
	if !t.Spec.AgentModel {
		return
	}
	c := &s.Main
	c.Env = append(c.Env, workload.Env{Name: envAgentMode, Value: m.mode})
	if m.mode != model.AgentModeLogin {
		c.Env = append(c.Env, workload.Env{Name: envAgentModel, Value: m.model}, workload.Env{Name: envAgentBaseURL, Value: m.baseURL})
	}
	if m.mode == model.AgentModePlatform && hasToken {
		c.Env = append(c.Env, workload.Env{Name: envAgentKey, From: &workload.SecretRef{Holder: s.Ref.Name, Use: executor.UseAPI, Key: APITokenKey}})
	}
	if prompt := model.PrintableText(ws.AgentPrompt); prompt != "" {
		c.Env = append(c.Env, workload.Env{Name: envAgentPrompt, Value: prompt})
	}
}

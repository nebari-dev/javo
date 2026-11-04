package workspace

import (
	"cmp"
	"context"
	"errors"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/ai/agentproxy"
	"github.com/nebari-dev/javo/internal/controller/secretenv"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
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
	if r.AgentModel != "" {
		base := cmp.Or(r.APIInternalURL, r.APIURL)
		return agentModel{mode: model.AgentModePlatform, model: r.AgentModel, baseURL: strings.TrimRight(base, "/") + agentproxy.Route}, nil
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

// WithAgent gives the app container of pod, built from template t, its
// agent model and first prompt. On the platform's model the key is the
// app's own API token (from its token Secret tokenSecret); the owner's own
// key comes with the app's other secrets (WithAppSecrets). Nothing for a
// template without agentModel.
func WithAgent(pod *corev1.Pod, app *model.App, t *model.Template, m agentModel, tokenSecret string) {
	if !t.Spec.AgentModel {
		return
	}
	c := &pod.Spec.Containers[0]
	c.Env = append(c.Env, corev1.EnvVar{Name: envAgentMode, Value: m.mode})
	if m.mode != model.AgentModeLogin {
		c.Env = append(c.Env, corev1.EnvVar{Name: envAgentModel, Value: m.model}, corev1.EnvVar{Name: envAgentBaseURL, Value: m.baseURL})
	}
	if m.mode == model.AgentModePlatform && tokenSecret != "" {
		c.Env = append(c.Env, corev1.EnvVar{Name: envAgentKey, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: tokenSecret}, Key: APITokenKey,
		}}})
	}
	if app.AgentPrompt != "" {
		c.Env = append(c.Env, corev1.EnvVar{Name: envAgentPrompt, Value: app.AgentPrompt})
	}
}

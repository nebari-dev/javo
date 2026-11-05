package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/ai/aisettings"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/reasons"
)

// AISettings is the AI settings page: the settings in force and the
// platform config's. It never holds a key, only the stored one's end.
type AISettings struct {
	Enabled    bool   `json:"enabled" doc:"ai.enabled in the platform config; false: AI is off and nothing here applies"`
	Overridden bool   `json:"overridden" doc:"Settings saved here are in force; false: the platform config's"`
	Provider   string `json:"provider,omitempty" doc:"The provider in force"`
	BaseURL    string `json:"baseURL,omitempty" doc:"The provider's address in force; empty is the provider's own"`
	Model      string `json:"model,omitempty" doc:"The model in force"`
	KeyStored  bool   `json:"keyStored" doc:"A key is stored here"`
	KeyLast4   string `json:"keyLast4,omitempty" doc:"The last four characters of the stored key"`
	KeyFrom    string `json:"keyFrom,omitempty" enum:"settings,config,none" doc:"Where the key in force comes from: stored here, the platform config's (sent only to its provider and address), or none"`
	Config     struct {
		Provider string `json:"provider"`
		BaseURL  string `json:"baseURL"`
		Model    string `json:"model"`
		Key      bool   `json:"key" doc:"The platform config has a key"`
	} `json:"config" doc:"The platform config's values, which apply when nothing is saved here"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

type aiSettingsOutput struct{ Body AISettings }

type aiSettingsInput struct {
	Body struct {
		Provider  string `json:"provider" enum:"anthropic,openai" doc:"anthropic, or openai for any OpenAI-compatible server"`
		BaseURL   string `json:"baseURL,omitempty" maxLength:"512" doc:"https address of the provider's API; empty is the provider's own"`
		Model     string `json:"model" maxLength:"200"`
		APIKey    string `json:"apiKey,omitempty" maxLength:"4096" doc:"Write-only: replaces the stored key; leave out to keep it"`
		RemoveKey bool   `json:"removeKey,omitempty" doc:"Remove the stored key"`
	}
}

func (a *API) registerAI(api huma.API) {
	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminGetAISettings", Method: http.MethodGet, Path: "/api/admin/ai",
		Summary: "The AI settings in force (platform admins)",
	}, func(ctx context.Context, _ *struct{}) (*aiSettingsOutput, error) {
		return a.aiView(ctx)
	})

	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminSetAISettings", Method: http.MethodPut, Path: "/api/admin/ai",
		Summary:     "Set the AI provider, address, model and key (platform admins)",
		Description: "Used in place of the platform config's at once, by every AI feature and the coding agents' endpoint. The key is stored encrypted and shown again only as its last four characters. Each change is on the audit log, without the key.",
		Errors:      []int{http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *aiSettingsInput) (*aiSettingsOutput, error) {
		if a.AI == nil {
			return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgAISettingsOff))
		}
		b := in.Body
		err := a.AI.Save(ctx, authn.Actor(ctx), aisettings.Input{
			Provider: b.Provider, BaseURL: b.BaseURL, Model: b.Model, APIKey: b.APIKey, RemoveKey: b.RemoveKey,
		})
		var fe *aisettings.FieldError
		switch {
		case errors.As(err, &fe):
			return nil, problem.FieldError(fe.Field, fe.Code)
		case err != nil:
			return nil, problem.Internal("save the AI settings", err)
		}
		return a.aiView(ctx)
	})

	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminResetAISettings", Method: http.MethodDelete, Path: "/api/admin/ai",
		Summary:       "Go back to the platform config's AI settings (platform admins)",
		DefaultStatus: http.StatusNoContent, Errors: []int{http.StatusConflict},
	}, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		if a.AI == nil {
			return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgAISettingsOff))
		}
		if err := a.AI.Reset(ctx, authn.Actor(ctx)); err != nil {
			return nil, problem.Internal("reset the AI settings", err)
		}
		return nil, nil
	})
}

func (a *API) aiView(ctx context.Context) (*aiSettingsOutput, error) {
	out := &aiSettingsOutput{}
	if a.AI == nil {
		return out, nil
	}
	v, err := a.AI.View(ctx)
	if err != nil {
		return nil, problem.Internal("read the AI settings", err)
	}
	b := &out.Body
	b.Enabled, b.Overridden, b.Provider, b.BaseURL, b.Model = true, v.Overridden, v.Provider, v.BaseURL, v.Model
	b.KeyStored, b.KeyLast4, b.KeyFrom = v.KeyStored, v.KeyLast4, v.KeyFrom
	b.Config.Provider, b.Config.BaseURL, b.Config.Model, b.Config.Key = v.ConfigProvider, v.ConfigBaseURL, v.ConfigModel, v.ConfigKey
	if !v.UpdatedAt.IsZero() {
		b.UpdatedAt = &v.UpdatedAt
	}
	return out, nil
}

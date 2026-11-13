// Package openai is the provider adapter for OpenAI-compatible chat
// completion servers (OpenAI, Azure OpenAI, vLLM, Ollama, llama.cpp),
// through the official openai-go SDK. Structured output asks for a strict
// JSON schema response format; a server that rejects it gets a function
// call with that schema instead, from then on.
package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"github.com/nebari-dev/javo/internal/ai/provider"
)

// ID is the provider id in the config.
const ID = provider.OpenAI

type adapter struct {
	client  sdk.Client
	timeout time.Duration
	// viaFunction is set once the server refused a JSON schema response format.
	viaFunction atomic.Bool
}

// New builds the adapter. A local server may need no key; one is sent anyway
// (the SDK requires a value). Retries are off, as for the anthropic adapter.
func New(cfg provider.Config) (provider.Provider, error) {
	key := cfg.APIKey
	if key == "" {
		key = "none"
	}
	opts := []option.RequestOption{option.WithAPIKey(key), option.WithMaxRetries(0)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	return &adapter{client: sdk.NewClient(opts...), timeout: cfg.Timeout}, nil
}

func (a *adapter) ID() string { return ID }

func (a *adapter) Generate(ctx context.Context, req provider.Request) (provider.Response, error) {
	if a.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}
	schema, err := provider.SchemaMap(req.Schema)
	if err != nil {
		return provider.Response{}, fmt.Errorf("openai: schema: %w", err)
	}
	if !a.viaFunction.Load() {
		r, err := a.call(ctx, req, schema, false)
		var apiErr *sdk.Error
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
			return r, classify(err)
		}
		a.viaFunction.Store(true)
	}
	r, err := a.call(ctx, req, schema, true)
	return r, classify(err)
}

func (a *adapter) call(ctx context.Context, req provider.Request, schema map[string]any, viaFunction bool) (provider.Response, error) {
	p := sdk.ChatCompletionNewParams{
		Model:     sdk.ChatModel(req.Model),
		MaxTokens: sdk.Int(int64(req.MaxOutputTokens)),
		Messages:  []sdk.ChatCompletionMessageParamUnion{sdk.SystemMessage(req.System), sdk.UserMessage(provider.UserContent(req))},
	}
	if viaFunction {
		p.Tools = []sdk.ChatCompletionToolUnionParam{sdk.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: req.SchemaName, Parameters: shared.FunctionParameters(schema), Strict: sdk.Bool(true),
		})}
		p.ToolChoice = sdk.ToolChoiceOptionFunctionToolChoice(sdk.ChatCompletionNamedToolChoiceFunctionParam{Name: req.SchemaName})
	} else {
		p.ResponseFormat = sdk.ChatCompletionNewParamsResponseFormatUnion{OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
			JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{Name: req.SchemaName, Schema: schema, Strict: sdk.Bool(true)},
		}}
	}
	c, err := a.client.Chat.Completions.New(ctx, p)
	if err != nil {
		return provider.Response{}, err
	}
	out := provider.Response{Model: c.Model, Usage: provider.Usage{InputTokens: int(c.Usage.PromptTokens), OutputTokens: int(c.Usage.CompletionTokens)}}
	if len(c.Choices) == 0 {
		return out, provider.ErrNoAnswer
	}
	m := c.Choices[0].Message
	switch {
	case viaFunction && len(m.ToolCalls) > 0:
		out.JSON = []byte(m.ToolCalls[0].Function.Arguments)
	case !viaFunction && m.Content != "":
		out.JSON = []byte(m.Content)
	default:
		return out, provider.ErrNoAnswer
	}
	return out, nil
}

// classify maps SDK errors to the provider errors; nil stays nil.
func classify(err error) error {
	if err == nil || errors.Is(err, provider.ErrNoAnswer) {
		return err
	}
	var apiErr *sdk.Error
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("%w: %v", provider.ErrUnavailable, err)
	}
	switch {
	case apiErr.StatusCode == http.StatusTooManyRequests:
		return &provider.RateLimitedError{RetryAfter: provider.RetryAfter(apiErr.Response)}
	case apiErr.StatusCode >= 500:
		return fmt.Errorf("%w: status %d", provider.ErrUnavailable, apiErr.StatusCode)
	}
	return fmt.Errorf("openai: status %d: %w", apiErr.StatusCode, err)
}

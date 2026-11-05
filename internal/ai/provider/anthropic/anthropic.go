// Package anthropic is the provider adapter for the Claude API, through the
// official anthropic-sdk-go. Structured output uses output_config.format
// (JSON schema), which current Claude models support; forced tool use is
// refused by the newest models, so it is not used.
package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/nebari-dev/javo/internal/ai/provider"
)

// ID is the provider id in the config.
const ID = "anthropic"

type adapter struct {
	client  sdk.Client
	timeout time.Duration
}

// New builds the adapter. Retries are off: a rate limit is shown to the
// person with its wait, and an outage opens the circuit breaker.
func New(cfg provider.Config) (provider.Provider, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("anthropic: no API key (JAVO_AI_API_KEY)")
	}
	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey), option.WithMaxRetries(0)}
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
		return provider.Response{}, fmt.Errorf("anthropic: schema: %w", err)
	}
	msg, err := a.client.Messages.New(ctx, sdk.MessageNewParams{
		Model:     sdk.Model(req.Model),
		MaxTokens: int64(req.MaxOutputTokens),
		System:    []sdk.TextBlockParam{{Text: req.System}},
		Messages:  []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock(provider.UserContent(req)))},
		OutputConfig: sdk.OutputConfigParam{
			Format: sdk.JSONOutputFormatParam{Schema: schema},
		},
	})
	if err != nil {
		return provider.Response{}, classify(err)
	}
	out := provider.Response{Model: string(msg.Model), Usage: provider.Usage{
		InputTokens: int(msg.Usage.InputTokens), OutputTokens: int(msg.Usage.OutputTokens),
	}}
	if msg.StopReason == sdk.StopReasonRefusal || msg.StopReason == sdk.StopReasonMaxTokens {
		return out, fmt.Errorf("%w: stop reason %s", provider.ErrNoAnswer, msg.StopReason)
	}
	for _, b := range msg.Content {
		if t, ok := b.AsAny().(sdk.TextBlock); ok {
			out.JSON = []byte(t.Text)
			return out, nil
		}
	}
	return out, provider.ErrNoAnswer
}

// classify maps SDK errors to the provider errors.
func classify(err error) error {
	var apiErr *sdk.Error
	if !errors.As(err, &apiErr) {
		// No HTTP answer at all: refused, reset, timed out.
		return fmt.Errorf("%w: %v", provider.ErrUnavailable, err)
	}
	switch {
	case apiErr.StatusCode == http.StatusTooManyRequests:
		return &provider.RateLimitedError{RetryAfter: provider.RetryAfter(apiErr.Response)}
	case apiErr.StatusCode >= 500:
		return fmt.Errorf("%w: status %d", provider.ErrUnavailable, apiErr.StatusCode)
	}
	return fmt.Errorf("anthropic: status %d: %w", apiErr.StatusCode, err)
}

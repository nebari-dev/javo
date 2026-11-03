package provider_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/ai/aitest"
	"github.com/nebari-dev/javo/internal/ai/provider"
)

func TestBreakerStopsCallingAfterThreeOutages(t *testing.T) {
	down := aitest.New(
		aitest.Reply{Err: provider.ErrUnavailable}, aitest.Reply{Err: provider.ErrUnavailable}, aitest.Reply{Err: provider.ErrUnavailable},
		aitest.Reply{JSON: `{"ok":true}`},
	)
	p := provider.WithBreaker(down)
	for i := 0; i < 3; i++ {
		if _, err := p.Generate(context.Background(), provider.Request{}); !errors.Is(err, provider.ErrUnavailable) {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := p.Generate(context.Background(), provider.Request{}); !errors.Is(err, provider.ErrUnavailable) {
		t.Fatalf("fourth call: %v", err)
	}
	if down.Calls() != 3 {
		t.Fatalf("the provider was called %d times, want 3", down.Calls())
	}
}

func TestBreakerIgnoresRateLimits(t *testing.T) {
	rl := &provider.RateLimitedError{}
	busy := aitest.New(aitest.Reply{Err: rl}, aitest.Reply{Err: rl}, aitest.Reply{Err: rl}, aitest.Reply{JSON: `{}`})
	p := provider.WithBreaker(busy)
	for i := 0; i < 4; i++ {
		_, _ = p.Generate(context.Background(), provider.Request{})
	}
	if busy.Calls() != 4 {
		t.Fatalf("a rate-limited provider was cut off after %d calls", busy.Calls())
	}
}

func TestSelfTestNeedsTheRequestedJSON(t *testing.T) {
	if r := provider.SelfTest(context.Background(), aitest.New(aitest.Reply{JSON: `Sure! ok is true.`}), "m"); r.StructuredOutput || r.Err == "" {
		t.Fatalf("plain text passed: %+v", r)
	}
	if r := provider.SelfTest(context.Background(), aitest.New(aitest.Reply{JSON: `{"ok":true}`}), "m"); !r.StructuredOutput {
		t.Fatalf("JSON failed: %+v", r)
	}
}

func TestUnknownProviderNamesTheKnownOnes(t *testing.T) {
	var r provider.Registry
	r.Register("openai", func(provider.Config) (provider.Provider, error) { return nil, nil })
	r.Register("anthropic", func(provider.Config) (provider.Provider, error) { return nil, nil })
	_, err := r.New(provider.Config{Provider: "gemini"})
	if err == nil || !strings.Contains(err.Error(), "anthropic, openai") {
		t.Fatalf("err %v", err)
	}
}

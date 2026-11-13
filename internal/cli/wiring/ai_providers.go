//go:build !local

package wiring

import (
	"github.com/nebari-dev/javo/internal/ai/provider"
	"github.com/nebari-dev/javo/internal/ai/provider/anthropic"
	"github.com/nebari-dev/javo/internal/ai/provider/openai"
)

// aiProviders is the only place model adapters are wired in.
func aiProviders() *provider.Registry {
	var r provider.Registry
	r.Register(anthropic.ID, anthropic.New)
	r.Register(openai.ID, openai.New)
	return &r
}

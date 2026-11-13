//go:build local

package wiring

import "github.com/nebari-dev/javo/internal/ai/provider"

// aiProviders is the model adapters of a binary built with the tag local:
// none, since `javo local` never turns AI on and the providers' clients
// are a fifth of the binary. AIService refuses a config that turns it on.
func aiProviders() *provider.Registry { return &provider.Registry{} }

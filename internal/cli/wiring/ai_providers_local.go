//go:build local

package wiring

import "github.com/nebari-dev/javo/internal/ai/provider"

// aiProviders is the model adapters of a binary built with the tag local:
// none, since `javo local` never turns AI on and the providers' clients
// would add about 45 MB to it. AIService refuses a config that turns it on.
func aiProviders() *provider.Registry { return &provider.Registry{} }

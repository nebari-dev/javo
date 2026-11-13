//go:build local

package wiring

import (
	"github.com/nebari-dev/javo/internal/ai"
	"github.com/nebari-dev/javo/internal/ai/provider"
	"github.com/nebari-dev/javo/internal/buildinfo"
)

// aiProviders is the model adapters of a binary built with the tag local:
// none, since `javo local` never turns AI on and the providers' clients
// would add about 45 MB to it. AIService refuses a config that turns it on.
func aiProviders() *provider.Registry { return &provider.Registry{} }

// secretScanner is refused: nothing is sent to a model by a binary that
// has none, so the scanner's rules and the archive readers they come
// with (about 5 MB) are not carried.
func secretScanner() (ai.Redactor, error) {
	return nil, buildinfo.LeftOut("the AI assistant (ai.enabled)")
}

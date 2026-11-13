//go:build linux || darwin

package process

import (
	"context"

	"github.com/nebari-dev/javo/internal/executor/supervise"
)

// Adopt finds nothing: New kills what an earlier Javo left running
// (sweep), so there is never a workload to take over.
func (e *engine) Adopt(context.Context) ([]supervise.Found, error) { return nil, nil }

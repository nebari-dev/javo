package dockerprobe

import (
	"context"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/netprobe"
)

// Without a daemon the probe could not run: Unknown, never an error.
func TestDockerProbeIsUnknownWithoutADaemon(t *testing.T) {
	t.Parallel()
	p := &Prober{Client: docker.New("/nonexistent/docker.sock"), Gateway: "gw", Image: "example.com/javo:1", Timeout: 5 * time.Second}
	res := p.Probe(context.Background())
	if res.State != netprobe.Unknown || len(res.Evidence) == 0 {
		t.Fatalf("result %+v, want Unknown with the reason", res)
	}
}

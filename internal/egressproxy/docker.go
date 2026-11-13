package egressproxy

import (
	"context"
	"net/netip"

	"github.com/nebari-dev/javo/internal/executor/docker"
)

// Docker reads the platform's networks from the Docker daemon behind c,
// the client the Docker executor uses (its labels say which networks are
// this Javo's), with the executor's pool. The daemon is asked for each
// connection: a network made a moment ago counts at once, and one that
// is gone no longer does.
func Docker(c *docker.Client, pool netip.Prefix) NetworksFunc {
	return func(ctx context.Context) (Networks, error) {
		nets, err := docker.ReadNets(ctx, c, pool)
		if err != nil {
			return Networks{}, err
		}
		out := Networks{Internal: nets.Ranges}
		for subnet, ref := range nets.Workloads {
			out.Apps = append(out.Apps, AppNetwork{Range: subnet, App: ref})
		}
		return out, nil
	}
}

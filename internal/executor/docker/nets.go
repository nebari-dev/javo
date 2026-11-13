package docker

import (
	"context"
	"net/netip"

	"github.com/nebari-dev/javo/internal/workload"
)

// Nets are the address ranges of the daemon's networks, as the gateway's
// forward proxy needs them (internal/egressproxy): to keep apps from
// every one of them, and to tell which app a connection is from.
type Nets struct {
	// Ranges are the ranges of all the daemon's networks, whoever made
	// them, and the pool workloads' networks are cut from.
	Ranges []netip.Prefix
	// Workloads are the ranges of the networks this client's Javo made
	// for workloads (they carry Javo's labels and the client's own). A
	// workload's network holds its sandbox, whose address all its units
	// share, and the gateway.
	Workloads map[netip.Prefix]workload.Ref
}

// ReadNets asks the daemon for its networks. The zero pool is DefaultPool.
func ReadNets(ctx context.Context, c *Client, pool netip.Prefix) (Nets, error) {
	all, err := c.NetworkList(ctx)
	if err != nil {
		return Nets{}, err
	}
	if !pool.IsValid() {
		pool = DefaultPool
	}
	nets := Nets{Ranges: []netip.Prefix{pool.Masked()}, Workloads: map[netip.Prefix]workload.Ref{}}
	for _, n := range all {
		ref := RefOf(n.Labels)
		ours := Ours(n.Labels) && ref.Name != ""
		for k, v := range c.Labels {
			ours = ours && n.Labels[k] == v
		}
		for _, cfg := range n.IPAM.Config {
			subnet, err := netip.ParsePrefix(cfg.Subnet)
			if err != nil {
				continue
			}
			subnet = subnet.Masked()
			nets.Ranges = append(nets.Ranges, subnet)
			if ours {
				nets.Workloads[subnet] = ref
			}
		}
	}
	return nets, nil
}

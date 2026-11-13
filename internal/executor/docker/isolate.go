package docker

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"

	"github.com/nebari-dev/javo/internal/workload"
)

// Isolate gives the workload ref a network of its own that reaches
// nothing but the gateway container: no other workload, no address of the
// host, no internet and no outside DNS. The gateway is put on it, so it
// can reach the workload's port and the workload can reach the gateway
// (every port the gateway listens on). Calling it again changes nothing.
//
// pool is the address range the network's own is taken from, 16
// addresses at a time; the daemon's own pools are enough for about 30
// networks only. The zero pool leaves the choice to the daemon.
//
// A network that turns out not to be isolated (a daemon that ignores the
// option) is removed again and is an error: a workload never starts on it.
func Isolate(ctx context.Context, c *Client, ref workload.Ref, gateway string, pool netip.Prefix) error {
	name, err := NetworkName(ref)
	if err != nil {
		return err
	}
	n, err := c.NetworkInspect(ctx, name)
	if errors.Is(err, ErrNotFound) {
		subnet := ""
		if pool.IsValid() {
			all, err := c.NetworkList(ctx)
			if err != nil {
				return err
			}
			free, err := freeSubnet(pool, all)
			if err != nil {
				return err
			}
			subnet = free.String()
		}
		if err := c.NetworkCreate(ctx, name, subnet, Labels(ref)); err != nil && !errors.Is(err, ErrConflict) {
			return err
		}
		n, err = c.NetworkInspect(ctx, name)
	}
	if err != nil {
		return err
	}
	if !Ours(n.Labels) {
		return foreign("network", name)
	}
	if !n.isolated() {
		if err := c.NetworkRemove(ctx, name); err != nil {
			return err
		}
		return fmt.Errorf("docker: network %q is not isolated from the host (internal %v, options %v, addresses %v); it was removed", name, n.Internal, n.Options, n.IPAM.Config)
	}
	for id, member := range n.Containers {
		if id == gateway || member.Name == gateway {
			return nil
		}
	}
	return c.NetworkConnect(ctx, name, gateway)
}

// Release takes the gateway off the workload's network and removes the
// network. Its containers must be gone. Releasing what does not exist is
// not an error.
func Release(ctx context.Context, c *Client, ref workload.Ref, gateway string) error {
	name, err := NetworkName(ref)
	if err != nil {
		return err
	}
	n, err := c.NetworkInspect(ctx, name)
	if err != nil {
		return gone(err)
	}
	if !Ours(n.Labels) {
		return foreign("network", name)
	}
	if err := c.NetworkDisconnect(ctx, name, gateway); err != nil {
		return err
	}
	return c.NetworkRemove(ctx, name)
}

// ErrPoolFull means the pool has no range left for one more workload's
// network. Stopping a workload frees its range.
var ErrPoolFull = errors.New("the network pool is full")

// subnetBits is the size of a workload's network: 16 addresses, for its
// sandbox, the gateway and a probe.
const subnetBits = 28

// freeSubnet is the first range of pool that no network in taken overlaps.
func freeSubnet(pool netip.Prefix, taken []Network) (netip.Prefix, error) {
	pool = pool.Masked()
	if !pool.Addr().Is4() || pool.Bits() > subnetBits {
		return netip.Prefix{}, fmt.Errorf("docker: the network pool %s must be an IPv4 range of /%d or wider", pool, subnetBits)
	}
	var used []netip.Prefix
	for _, n := range taken {
		for _, cfg := range n.IPAM.Config {
			if p, err := netip.ParsePrefix(cfg.Subnet); err == nil {
				used = append(used, p)
			}
		}
	}
	start := binary.BigEndian.Uint32(pool.Addr().AsSlice())
	count := uint32(1) << (subnetBits - pool.Bits())
next:
	for i := range count {
		var raw [4]byte
		binary.BigEndian.PutUint32(raw[:], start+i<<(32-subnetBits))
		candidate := netip.PrefixFrom(netip.AddrFrom4(raw), subnetBits)
		for _, u := range used {
			if u.Overlaps(candidate) {
				continue next
			}
		}
		return candidate, nil
	}
	return netip.Prefix{}, fmt.Errorf("docker: %w: every /%d range of %s is some network's", ErrPoolFull, subnetBits, pool)
}

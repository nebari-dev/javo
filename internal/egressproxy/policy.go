package egressproxy

import (
	"context"
	"net/netip"
	"slices"

	"github.com/nebari-dev/javo/internal/egress"
	"github.com/nebari-dev/javo/internal/workload"
)

// Networks is what the platform's networks look like at the moment of one
// connection.
type Networks struct {
	// Internal are the address ranges of the platform's own networks:
	// every network of the Docker daemon and the pool workloads' networks
	// are cut from. No app reaches them through the proxy.
	Internal []netip.Prefix
	// Apps are the workloads' networks. A connection from one of them is
	// that app's.
	Apps []AppNetwork
}

// AppNetwork is the address range of one workload's network.
type AppNetwork struct {
	Range netip.Prefix
	App   workload.Ref
}

// app is the workload whose network holds addr.
func (n Networks) app(addr netip.Addr) (workload.Ref, bool) {
	for _, a := range n.Apps {
		if a.Range.Contains(addr) {
			return a.App, true
		}
	}
	return workload.Ref{}, false
}

// Rules are the operator's egress settings, the same three lists the
// NetworkPolicy of a Kubernetes namespace is made from
// (namespace.Egress): --egress-deny-cidrs, --egress-allow-cidrs and
// --egress-allow-cluster-cidrs.
type Rules struct {
	// Deny are ranges no app may reach.
	Deny []netip.Prefix
	// Allow, when set, are the only ranges apps may reach (less Deny),
	// besides AllowCluster. Empty: the public internet.
	Allow []netip.Prefix
	// AllowCluster are ranges apps may reach although they are private
	// (less Deny).
	AllowCluster []netip.Prefix
}

// refuses says why addr may not be connected to, or "" when it may. own
// are the addresses of the machine the proxy runs on.
//
// The order is the package's: what is never reachable first, then the
// operator's Deny, then the allow lists as a NetworkPolicy reads them.
func (r Rules) refuses(addr netip.Addr, nets Networks, own []netip.Addr) string {
	if !addr.IsValid() {
		return "not an address"
	}
	if addr.Zone() != "" {
		// A zone names an interface of the gateway; no app has a say in
		// which interface the gateway sends from.
		return "an address with a zone"
	}
	// ::ffff:10.0.0.1 is 10.0.0.1 on the wire: judge the IPv4 address.
	addr = addr.Unmap()
	switch {
	case addr.IsLoopback():
		return "a loopback address"
	case addr.IsUnspecified():
		return "the unspecified address"
	case addr.IsLinkLocalUnicast():
		return "a link-local address"
	case addr.IsMulticast():
		return "a multicast address"
	case slices.Contains(own, addr):
		return "an address of the gateway itself"
	case egress.Covered(addr, nets.Internal):
		return "an address of the platform's own networks"
	case egress.Covered(addr, r.Deny):
		return "a denied address"
	case egress.Covered(addr, r.AllowCluster):
		return ""
	}
	if len(r.Allow) > 0 {
		if egress.Covered(addr, r.Allow) {
			return ""
		}
		return "an address outside the allowed ranges"
	}
	if egress.Covered(addr, egress.Private()) {
		return "a private address"
	}
	if egress.Covered(addr, egress.Reserved()) {
		return "a reserved address"
	}
	return ""
}

// NetworksFunc reads the platform's networks.
type NetworksFunc func(ctx context.Context) (Networks, error)

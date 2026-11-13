// Package egress holds the address ranges an app is kept away from on
// every platform: the NetworkPolicy of a Kubernetes namespace
// (internal/controller/namespace) and the gateway's forward proxy for
// Docker apps (internal/egressproxy) both read them here, so the two
// cannot drift apart.
package egress

import (
	"net/netip"
	"slices"
)

// privateRanges are the addresses a deployment lives on (private,
// carrier-grade NAT, link-local, loopback; IPv6 unique-local and
// link-local): by default apps reach every address but these. Nodes, the
// API server, storage servers and the cloud's metadata address sit here.
var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
}

// reservedRanges are not private but are no public host either: "this
// network", IETF protocol assignments, benchmarking, multicast and the
// reserved class E (some clusters run pods or services on these), and on
// IPv6 the unspecified and loopback addresses, NAT64 (64:ff9b::/96 and the
// local-use 64:ff9b:1::/48 reach IPv4 addresses, private ones included,
// through a NAT64 gateway), 6to4 (deprecated, also carries IPv4
// addresses) and multicast.
var reservedRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("ff00::/8"),
}

// Private are the private ranges, and Reserved the ranges that are
// neither private nor a public host. Each call gives a list of its own.
func Private() []netip.Prefix  { return slices.Clone(privateRanges) }
func Reserved() []netip.Prefix { return slices.Clone(reservedRanges) }

// Covered reports whether one of ranges holds addr.
func Covered(addr netip.Addr, ranges []netip.Prefix) bool {
	return slices.ContainsFunc(ranges, func(p netip.Prefix) bool { return p.Contains(addr) })
}

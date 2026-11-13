package egressproxy

import (
	"net/netip"
	"testing"

	"github.com/nebari-dev/javo/internal/workload"
)

func prefixes(cidrs ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, c := range cidrs {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}

// testNets are the platform's networks of the tests: the daemon's default
// bridge and the executor's pool, with one app whose network is loopback,
// where the tests connect from.
var testNets = Networks{
	Internal: prefixes("172.17.0.0/16", "10.213.0.0/16", "192.0.2.0/24"),
	Apps:     []AppNetwork{{Range: netip.MustParsePrefix("127.0.0.0/8"), App: workload.Ref{Namespace: "alice", Name: "demo"}}},
}

// testOwn is the gateway's own address in the tests: a public one, so
// only the rule about the gateway itself refuses it.
var testOwn = []netip.Addr{netip.MustParseAddr("198.51.100.1")}

// Every kind of address an app must not reach is refused, whatever name
// led to it, and a public address is not.
func TestRulesRefuseEveryRangeAnAppMustNotReach(t *testing.T) {
	t.Parallel()
	deny := Rules{Deny: prefixes("203.0.113.64/26", "fd00:ec2::254/128")}
	for addr, want := range map[string]string{
		"127.0.0.1":              "a loopback address",
		"127.8.9.10":             "a loopback address",
		"::1":                    "a loopback address",
		"0.0.0.0":                "the unspecified address",
		"::":                     "the unspecified address",
		"169.254.169.254":        "a link-local address",
		"169.254.1.1":            "a link-local address",
		"fe80::1":                "a link-local address",
		"224.0.0.1":              "a multicast address",
		"239.255.255.250":        "a multicast address",
		"ff02::1":                "a multicast address",
		"198.51.100.1":           "an address of the gateway itself",
		"172.17.0.1":             "an address of the platform's own networks",
		"10.213.0.2":             "an address of the platform's own networks",
		"192.0.2.9":              "an address of the platform's own networks",
		"203.0.113.65":           "a denied address",
		"fd00:ec2::254":          "a denied address",
		"10.0.0.1":               "a private address",
		"172.16.0.1":             "a private address",
		"172.31.255.255":         "a private address",
		"192.168.1.1":            "a private address",
		"100.64.0.1":             "a private address",
		"100.100.100.200":        "a private address",
		"fc00::1":                "a private address",
		"fd12:3456::1":           "a private address",
		"0.1.2.3":                "a reserved address",
		"192.0.0.8":              "a reserved address",
		"198.18.0.1":             "a reserved address",
		"240.0.0.1":              "a reserved address",
		"255.255.255.255":        "a reserved address",
		"64:ff9b::a00:1":         "a reserved address",
		"2002:a00:1::1":          "a reserved address",
		"::ffff:10.0.0.1":        "a private address",
		"::ffff:127.0.0.1":       "a loopback address",
		"::ffff:169.254.169.254": "a link-local address",
		"::ffff:172.17.0.1":      "an address of the platform's own networks",
		"fe80::1%eth0":           "an address with a zone",
		"203.0.113.10":           "",
		"8.8.8.8":                "",
		"2001:4860:4860::8888":   "",
		"::ffff:203.0.113.10":    "",
	} {
		if got := deny.refuses(netip.MustParseAddr(addr), testNets, testOwn); got != want {
			t.Errorf("%s: %q, want %q", addr, got, want)
		}
	}
	if got := deny.refuses(netip.Addr{}, testNets, testOwn); got == "" {
		t.Error("the zero address is allowed")
	}
}

// The allow lists read as a NetworkPolicy reads them: with an allow list
// only its ranges are reachable, a private range named for the cluster
// is reachable, a denied range never is. None of them opens what is
// refused whatever the settings say.
func TestRulesHonourTheAllowLists(t *testing.T) {
	t.Parallel()
	r := Rules{
		Deny:         prefixes("169.254.169.254/32", "10.9.0.0/16"),
		Allow:        prefixes("203.0.113.0/24", "127.0.0.0/8", "172.17.0.0/16", "169.254.0.0/16", "198.51.100.0/24"),
		AllowCluster: prefixes("10.0.0.0/8", "224.0.0.0/4"),
	}
	for addr, want := range map[string]string{
		"203.0.113.10":    "",
		"8.8.8.8":         "an address outside the allowed ranges",
		"10.1.2.3":        "",
		"10.9.2.3":        "a denied address",
		"192.168.1.1":     "an address outside the allowed ranges",
		"127.0.0.1":       "a loopback address",
		"172.17.0.1":      "an address of the platform's own networks",
		"169.254.169.254": "a link-local address",
		"169.254.1.1":     "a link-local address",
		"198.51.100.1":    "an address of the gateway itself",
		"224.0.0.1":       "a multicast address",
		"10.213.0.2":      "an address of the platform's own networks",
	} {
		if got := r.refuses(netip.MustParseAddr(addr), testNets, testOwn); got != want {
			t.Errorf("%s: %q, want %q", addr, got, want)
		}
	}
}

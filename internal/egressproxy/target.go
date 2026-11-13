package egressproxy

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// target is where a request wants to go: an address written out, or a
// name to resolve.
type target struct {
	// name is the host as a lower-case name without a trailing dot; empty
	// when addr is set.
	name string
	addr netip.Addr
	port uint16
}

// host is the target's host, for the log.
func (t target) host() string {
	if t.name != "" {
		return t.name
	}
	return t.addr.String()
}

var errBadTarget = errors.New("the target is not a host and a port")

// parseTarget reads "host:port". It refuses what could be read in two
// ways:
//
//   - no port, port 0 or a port that is not a decimal number below 65536
//     (RFC 9110 section 9.3.6: a CONNECT with an empty or invalid port
//     MUST be rejected);
//   - an IPv6 address with a zone;
//   - a host that ends in a number but is not an IPv4 address in its one
//     usual form, such as 2130706433, 0177.0.0.1, 0x7f.0.0.1 or 127.1:
//     some resolvers read these as addresses (inet_aton) and this proxy
//     would not, so they are never passed on to one;
//   - a name with a character outside letters, digits, "-", "_" and ".".
//
// One trailing dot is dropped: "example.com." is example.com, and
// "127.0.0.1." is the address.
func parseTarget(hostport string) (target, error) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return target{}, errBadTarget
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 || port[0] == '+' {
		return target{}, errBadTarget
	}
	t := target{port: uint16(n)}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || strings.Contains(host, "%") {
		return target{}, errBadTarget
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		t.addr = addr
		return t, nil
	}
	if len(host) > 253 {
		return target{}, errBadTarget
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.Trim(label, "abcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			return target{}, errBadTarget
		}
	}
	if last := labels[len(labels)-1]; strings.Trim(last, "0123456789") == "" || strings.HasPrefix(last, "0x") {
		return target{}, errBadTarget
	}
	t.name = host
	return t, nil
}

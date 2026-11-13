package egressproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"
)

// Limits of one connection to the outside.
const (
	// dialTimeout is how long one address has to answer, and
	// connectTimeout how long resolving and connecting may take in all.
	dialTimeout    = 10 * time.Second
	connectTimeout = 30 * time.Second
	// maxTries is how many allowed addresses of a name are tried.
	maxTries = 4
)

// DeniedError is why a target may not be reached. Its text names the
// kind of address, never the address of anything inside.
type DeniedError struct{ Why string }

func (e *DeniedError) Error() string { return "refused: the target is " + e.Why }

// resolver is the part of net.Resolver the proxy uses.
type resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// dialFunc connects to addr, an address written out, after asking check
// about the address the socket is about to connect to.
type dialFunc func(ctx context.Context, addr netip.AddrPort, check func(netip.Addr) error) (net.Conn, error)

// dialChecked is the dialFunc of a real proxy. The address is a literal,
// so nothing is resolved here; Control runs on the socket just before it
// connects and sees the address the kernel is given, which is checked
// once more. An address that is not allowed never gets a packet.
func dialChecked(ctx context.Context, addr netip.AddrPort, check func(netip.Addr) error) (net.Conn, error) {
	d := net.Dialer{
		Timeout: dialTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			to, err := netip.ParseAddrPort(address)
			if err != nil {
				return &DeniedError{Why: "not an address"}
			}
			return check(to.Addr())
		},
	}
	return d.DialContext(ctx, "tcp", addr.String())
}

// dial connects to t and returns the connection and the address it is
// to. It resolves a name once, here, and connects only to addresses it
// has checked itself.
//
// Of a name with several addresses, the ones that are refused are left
// out and the others are tried in order. Refusing the whole name would
// add nothing: no refused address is ever connected to either way, and a
// name served through DNS64 (which adds a NAT64 address, a refused range,
// to every answer) would never work.
func (p *Proxy) dial(ctx context.Context, t target) (net.Conn, netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	// The networks as they are now: an app started a second ago is
	// already off limits.
	nets, err := p.networks(ctx)
	if err != nil {
		return nil, netip.Addr{}, fmt.Errorf("the platform's networks: %w", err)
	}
	own := p.own()
	check := func(a netip.Addr) error {
		if why := p.rules.refuses(a, nets, own); why != "" {
			return &DeniedError{Why: why}
		}
		return nil
	}
	addrs := []netip.Addr{t.addr}
	if t.name != "" {
		if addrs, err = p.resolver.LookupNetIP(ctx, "ip", t.name); err != nil {
			return nil, netip.Addr{}, fmt.Errorf("the name does not resolve: %w", errResolve)
		}
	}
	var failed error = &DeniedError{Why: "a name without an address"}
	tries := 0
	for _, a := range addrs {
		a = a.Unmap()
		if err := check(a); err != nil {
			if tries == 0 {
				failed = err
			}
			continue
		}
		if tries++; tries > maxTries {
			break
		}
		conn, err := p.dialTo(ctx, netip.AddrPortFrom(a, t.port), check)
		if err == nil {
			return conn, a, nil
		}
		failed = err
	}
	return nil, netip.Addr{}, failed
}

// errResolve stands for a failed lookup: the resolver's own error can
// name servers inside.
var errResolve = errors.New("no answer")

package egress

import (
	"fmt"
	"net/netip"
	"slices"
)

// Lists are the operator's egress settings, read: --egress-deny-cidrs,
// --egress-allow-cidrs and --egress-allow-cluster-cidrs.
type Lists struct {
	// Deny are address ranges no app may reach.
	Deny []netip.Prefix
	// Allow, when set, are the only address ranges apps may reach (less
	// Deny), besides AllowCluster. Empty: the public internet.
	Allow []netip.Prefix
	// AllowCluster are address ranges apps may reach although they are
	// private (less Deny).
	AllowCluster []netip.Prefix
}

// Parse reads deny, allow and allowCluster CIDRs. It refuses what a
// NetworkPolicy cannot say: denying a whole address family (allow a list
// instead), or allowing a range that a denied range covers.
func Parse(deny, allow, allowCluster []string) (Lists, error) {
	var l Lists
	var err error
	if l.Deny, err = parsePrefixes(deny); err != nil {
		return Lists{}, err
	}
	if l.Allow, err = parsePrefixes(allow); err != nil {
		return Lists{}, err
	}
	if l.AllowCluster, err = parsePrefixes(allowCluster); err != nil {
		return Lists{}, err
	}
	for _, d := range l.Deny {
		if d.Bits() == 0 {
			return Lists{}, fmt.Errorf("egress deny %s covers every address; allow a list instead", d)
		}
		for _, a := range slices.Concat(l.Allow, l.AllowCluster) {
			if d.Bits() <= a.Bits() && d.Contains(a.Addr()) {
				return Lists{}, fmt.Errorf("egress allow %s is inside denied %s", a, d)
			}
		}
	}
	return l, nil
}

func parsePrefixes(cidrs []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, fmt.Errorf("egress CIDR %q: %w", c, err)
		}
		if p != p.Masked() {
			return nil, fmt.Errorf("egress CIDR %q has host bits set; did you mean %s?", c, p.Masked())
		}
		out = append(out, p)
	}
	return out, nil
}

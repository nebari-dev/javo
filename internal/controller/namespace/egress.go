package namespace

import (
	"fmt"
	"maps"
	"net/netip"
	"os"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/nebari-dev/javo/internal/model"
)

const policyLimitEgress = "limit-egress"

// Egress says where app and job pods may connect to.
type Egress struct {
	// Deny are address ranges no pod may reach (the cloud metadata
	// addresses, which hand out the node's cloud credentials).
	Deny []netip.Prefix
	// Allow, when set, are the only address ranges pods may reach (less
	// Deny), besides DNS, the gateway, the pod's own namespace and the
	// two below. Empty: the public internet, every address but Deny and
	// the private ranges the cluster lives on.
	Allow []netip.Prefix
	// AllowCluster are address ranges pods may reach although they are
	// private (less Deny): a service on the nodes or the private network.
	AllowCluster []netip.Prefix
	// AllowNamespaces are namespaces whose pods pods may reach.
	AllowNamespaces []string
	// NameServers are the DNS servers pods are told to use (the node's
	// --cluster-dns), reachable on port 53 besides the kube-dns pods.
	NameServers []netip.Addr
}

// ParseEgress reads deny, allow and allowCluster CIDRs. It refuses what a
// NetworkPolicy cannot say: denying a whole address family (allow a list
// instead), or allowing a range that a denied range covers.
func ParseEgress(deny, allow, allowCluster []string) (Egress, error) {
	var e Egress
	var err error
	if e.Deny, err = parsePrefixes(deny); err != nil {
		return Egress{}, err
	}
	if e.Allow, err = parsePrefixes(allow); err != nil {
		return Egress{}, err
	}
	if e.AllowCluster, err = parsePrefixes(allowCluster); err != nil {
		return Egress{}, err
	}
	for _, d := range e.Deny {
		if d.Bits() == 0 {
			return Egress{}, fmt.Errorf("egress deny %s covers every address; allow a list instead", d)
		}
		for _, a := range slices.Concat(e.Allow, e.AllowCluster) {
			if d.Bits() <= a.Bits() && d.Contains(a.Addr()) {
				return Egress{}, fmt.Errorf("egress allow %s is inside denied %s", a, d)
			}
		}
	}
	return e, nil
}

// ReadNameServers returns the nameserver addresses of a resolv.conf.
func ReadNameServers(path string) ([]netip.Addr, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "nameserver" {
			continue
		}
		a, err := netip.ParseAddr(f[1])
		if err != nil {
			return nil, fmt.Errorf("%s: nameserver %q: %w", path, f[1], err)
		}
		out = append(out, a)
	}
	return out, nil
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

// privateRanges are the addresses the cluster lives on (private, carrier-grade
// NAT, link-local, loopback; IPv6 unique-local and link-local): by default
// pods reach every address but these. Nodes, the API server
// and storage servers sit here; pods in other namespaces are kept away by
// leaving them out of the policy, since some network plugins (Cilium) never
// match pods by ipBlock.
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

// limitEgress lets every pod but the file pods (deny-egress-files keeps those
// off the network; policies add up, so this one must not select them) reach
// what Egress allows.
func (r *Reconciler) limitEgress(ns string) *networkingv1.NetworkPolicy {
	var blocks []networkingv1.NetworkPolicyPeer
	if len(r.Egress.Allow) == 0 {
		// The public internet.
		deny := append(slices.Clone(privateRanges), uncovered(r.Egress.Deny, privateRanges)...)
		blocks = []networkingv1.NetworkPolicyPeer{
			ipBlock(netip.MustParsePrefix("0.0.0.0/0"), deny),
			ipBlock(netip.MustParsePrefix("::/0"), deny),
		}
	} else {
		for _, a := range r.Egress.Allow {
			blocks = append(blocks, ipBlock(a, r.Egress.Deny))
		}
	}
	for _, a := range r.Egress.AllowCluster {
		blocks = append(blocks, ipBlock(a, r.Egress.Deny))
	}
	udp, tcp, dns := corev1.ProtocolUDP, corev1.ProtocolTCP, intstr.FromInt32(53)
	// DNS only from the cluster's servers: port 53 open to any host would
	// carry anything. CoreDNS and kube-dns pods carry k8s-app=kube-dns on
	// kubeadm, k3s, EKS, GKE and AKS. The name servers pods are told to use
	// are added by address for a node-local cache on the host network.
	dnsPeers := []networkingv1.NetworkPolicyPeer{{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}},
		PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
	}}
	for _, a := range r.Egress.NameServers {
		dnsPeers = append(dnsPeers, ipBlock(netip.PrefixFrom(a.WithZone(""), a.BitLen()), nil))
	}
	rules := []networkingv1.NetworkPolicyEgressRule{
		{Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &dns}, {Protocol: &tcp, Port: &dns}}, To: dnsPeers},
		{To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": r.Gateway.Namespace}},
			PodSelector:       &metav1.LabelSelector{MatchLabels: maps.Clone(r.Gateway.Labels)},
		}}},
		// The namespace's own pods: their ingress policy decides, so the
		// NetworkPolicy probe still tests it.
		{To: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}},
		{To: blocks},
	}
	if len(r.Egress.AllowNamespaces) > 0 {
		rules = append(rules, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpIn, Values: slices.Clone(r.Egress.AllowNamespaces),
			}}},
		}}})
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: policyLimitEgress, Labels: policyLabels()},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: model.LabelRole, Operator: metav1.LabelSelectorOpNotIn, Values: []string{roleFiles},
			}}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      rules,
		},
	}
}

// uncovered are the prefixes no range in by contains.
func uncovered(prefixes, by []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range prefixes {
		if !slices.ContainsFunc(by, func(b netip.Prefix) bool { return b.Bits() <= p.Bits() && b.Contains(p.Addr()) }) {
			out = append(out, p)
		}
	}
	return out
}

// ipBlock is cidr less the denied ranges strictly inside it (a policy refuses
// others in except).
func ipBlock(cidr netip.Prefix, deny []netip.Prefix) networkingv1.NetworkPolicyPeer {
	b := &networkingv1.IPBlock{CIDR: cidr.String()}
	for _, d := range deny {
		if d.Bits() > cidr.Bits() && cidr.Contains(d.Addr()) {
			b.Except = append(b.Except, d.String())
		}
	}
	return networkingv1.NetworkPolicyPeer{IPBlock: b}
}

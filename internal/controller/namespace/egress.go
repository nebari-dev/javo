package namespace

import (
	"context"
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

	"github.com/nebari-dev/javo/internal/egress"
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
	l, err := egress.Parse(deny, allow, allowCluster)
	return Egress{Deny: l.Deny, Allow: l.Allow, AllowCluster: l.AllowCluster}, err
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

// privateRanges and reservedRanges are the addresses pods do not reach by
// default: the ones the cluster lives on, and the ones that are no public
// host. Nodes, the API server and storage servers sit on the first; pods in
// other namespaces are kept away by leaving them out of the policy, since
// some network plugins (Cilium) never match pods by ipBlock. The forward
// proxy of Docker apps refuses the same lists (internal/egress).
var (
	privateRanges  = egress.Private()
	reservedRanges = egress.Reserved()
)

// limitEgress lets every pod but the file pods (deny-egress-files keeps those
// off the network; policies add up, so this one must not select them) reach
// what Egress allows. nodes are the cluster's own machines (their addresses
// and pod ranges), denied in the public internet and in the allow list alike.
func (r *Reconciler) limitEgress(ns string, nodes []netip.Prefix) *networkingv1.NetworkPolicy {
	var blocks []networkingv1.NetworkPolicyPeer
	if len(r.Egress.Allow) == 0 {
		// The public internet.
		deny := slices.Concat(privateRanges, reservedRanges)
		deny = append(deny, uncovered(r.Egress.Deny, deny)...)
		deny = append(deny, uncovered(nodes, deny)...)
		blocks = []networkingv1.NetworkPolicyPeer{
			ipBlock(netip.MustParsePrefix("0.0.0.0/0"), deny),
			ipBlock(netip.MustParsePrefix("::/0"), deny),
		}
	} else {
		deny := append(slices.Clone(r.Egress.Deny), uncovered(nodes, r.Egress.Deny)...)
		for _, a := range r.Egress.Allow {
			blocks = append(blocks, ipBlock(a, deny))
		}
	}
	for _, a := range r.Egress.AllowCluster {
		blocks = append(blocks, ipBlock(a, r.Egress.Deny))
	}
	udp, tcp, dns, gwPort := corev1.ProtocolUDP, corev1.ProtocolTCP, intstr.FromInt32(53), intstr.FromInt32(r.Gateway.Port)
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
		// The gateway's HTTP port only: with SQLite the same pod serves the
		// controller's metrics and health ports, which are not for apps.
		{Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &gwPort}}, To: []networkingv1.NetworkPolicyPeer{{
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

// nodeRanges are the cluster's own machines outside the private ranges, which
// pods must not reach: every node's internal and external addresses and its
// pod ranges. Sorted, so an unchanged cluster gives an unchanged policy.
func (r *Reconciler) nodeRanges(ctx context.Context) ([]netip.Prefix, error) {
	var nodes corev1.NodeList
	if err := r.Client.List(ctx, &nodes); err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	var out []netip.Prefix
	for _, n := range nodes.Items {
		for _, a := range n.Status.Addresses {
			if a.Type != corev1.NodeInternalIP && a.Type != corev1.NodeExternalIP {
				continue
			}
			if ip, err := netip.ParseAddr(a.Address); err == nil {
				ip = ip.Unmap().WithZone("")
				out = append(out, netip.PrefixFrom(ip, ip.BitLen()))
			}
		}
		for _, c := range n.Spec.PodCIDRs {
			if p, err := netip.ParsePrefix(c); err == nil {
				out = append(out, p.Masked())
			}
		}
	}
	out = uncovered(out, slices.Concat(privateRanges, reservedRanges))
	slices.SortFunc(out, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	return slices.Compact(out), nil
}

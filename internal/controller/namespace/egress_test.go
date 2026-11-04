package namespace

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/model"
)

var metadataCIDRs = []string{"169.254.169.254/32", "fd00:ec2::254/128"}

func mustEgress(t *testing.T, deny, allow []string) Egress {
	t.Helper()
	e, err := ParseEgress(deny, allow, nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func egressPolicy(t *testing.T, ns string) *networkingv1.NetworkPolicy {
	t.Helper()
	pol := &networkingv1.NetworkPolicy{}
	if err := k8s.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "limit-egress"}, pol); err != nil {
		t.Fatal(err)
	}
	return pol
}

// ipBlocks returns every ipBlock peer of the policy's rules with no port
// limit (the DNS rule's are not).
func ipBlocks(pol *networkingv1.NetworkPolicy) []networkingv1.IPBlock {
	var out []networkingv1.IPBlock
	for _, r := range pol.Spec.Egress {
		if len(r.Ports) > 0 {
			continue
		}
		for _, p := range r.To {
			if p.IPBlock != nil {
				out = append(out, *p.IPBlock)
			}
		}
	}
	return out
}

// hasPeer is true when a rule with no port limit allows peer.
func hasPeer(pol *networkingv1.NetworkPolicy, peer networkingv1.NetworkPolicyPeer) bool {
	for _, r := range pol.Spec.Egress {
		if len(r.Ports) > 0 {
			continue
		}
		for _, p := range r.To {
			if p.String() == peer.String() {
				return true
			}
		}
	}
	return false
}

// Every pod but the file pods (which have no egress at all) is covered.
func checkSelectsAllButFilePods(t *testing.T, pol *networkingv1.NetworkPolicy) {
	t.Helper()
	sel, err := metav1.LabelSelectorAsSelector(&pol.Spec.PodSelector)
	if err != nil {
		t.Fatal(err)
	}
	if !sel.Matches(labels.Set{"app": "x"}) || sel.Matches(labels.Set{model.LabelRole: "files"}) {
		t.Fatalf("pod selector %v must select app pods and not file pods", pol.Spec.PodSelector)
	}
	if !slices.Equal(pol.Spec.PolicyTypes, []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}) {
		t.Fatalf("policy types = %v", pol.Spec.PolicyTypes)
	}
}

// By default pods reach the public internet but not the cluster: no other
// namespace's pods (the database, the identity provider, the controller's
// metrics), no private or link-local address (nodes, the API server, storage
// servers) and no denied address (a public one like Azure's WireServer stays
// denied; the metadata addresses are already inside the private ranges).
func TestNamespaceEgressReachesTheInternetButNotTheCluster(t *testing.T) {
	gina := newPerson(t, "gina")
	r := perUser()
	r.Egress = mustEgress(t, append(slices.Clone(metadataCIDRs), "168.63.129.16/32"), nil)
	reconcilePerson(t, r, gina)

	pol := egressPolicy(t, "javo-"+gina.Name)
	checkSelectsAllButFilePods(t, pol)
	want := []networkingv1.IPBlock{
		{CIDR: "0.0.0.0/0", Except: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "127.0.0.0/8",
			"0.0.0.0/8", "192.0.0.0/24", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4", "168.63.129.16/32"}},
		{CIDR: "::/0", Except: []string{"fc00::/7", "fe80::/10", "::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "ff00::/8"}},
	}
	if got := ipBlocks(pol); !slices.EqualFunc(got, want, func(a, b networkingv1.IPBlock) bool { return a.String() == b.String() }) {
		t.Errorf("ip blocks = %v, want %v", got, want)
	}
	if hasPeer(pol, networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{}}) {
		t.Errorf("egress %v allows every pod in the cluster", pol.Spec.Egress)
	}
	checkKeepsGatewayAndOwnNamespace(t, pol)
}

// Admins can open chosen in-cluster services: the pods of named namespaces
// (matched by namespace, which every network plugin supports) and private
// ranges (less the denied addresses inside them).
func TestEgressOpensChosenClusterNamespacesAndRanges(t *testing.T) {
	lee := newPerson(t, "lee")
	r := perUser()
	e, err := ParseEgress(append(slices.Clone(metadataCIDRs), "10.5.0.1/32"), nil, []string{"10.5.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	r.Egress = e
	r.Egress.AllowNamespaces = []string{"monitoring", "nebi"}
	reconcilePerson(t, r, lee)

	pol := egressPolicy(t, "javo-"+lee.Name)
	if !slices.ContainsFunc(ipBlocks(pol), func(b networkingv1.IPBlock) bool {
		return b.String() == (&networkingv1.IPBlock{CIDR: "10.5.0.0/16", Except: []string{"10.5.0.1/32"}}).String()
	}) {
		t.Errorf("ip blocks %v do not open 10.5.0.0/16 less 10.5.0.1/32", ipBlocks(pol))
	}
	ns := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
		Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpIn, Values: []string{"monitoring", "nebi"},
	}}}}
	if !hasPeer(pol, ns) {
		t.Errorf("egress %v does not open the monitoring and nebi namespaces", pol.Spec.Egress)
	}
}

// checkKeepsGatewayAndOwnNamespace checks the in-cluster peers every egress
// policy allows: the gateway pods (some network plugins, Cilium, never match
// pods by ipBlock, so they are named by namespace and labels) and the
// namespace's own pods (the NetworkPolicy probe's dialer must be stopped by
// the listener's ingress policy, not by its own egress).
func checkKeepsGatewayAndOwnNamespace(t *testing.T, pol *networkingv1.NetworkPolicy) {
	t.Helper()
	gw := networkingv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": gateway.Namespace}},
		PodSelector:       &metav1.LabelSelector{MatchLabels: gateway.Labels},
	}
	if hasPeer(pol, gw) {
		t.Errorf("egress %v allows every port of the gateway pods", pol.Spec.Egress)
	}
	if ports := gatewayPorts(pol, gw); !slices.Equal(ports, []string{"TCP/8080"}) {
		t.Errorf("gateway pods reachable on %v, want only its HTTP port TCP/8080", ports)
	}
	if !hasPeer(pol, networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}}) {
		t.Errorf("egress %v does not allow the namespace's own pods", pol.Spec.Egress)
	}
}

// With an allow list, pods reach only those addresses (less the denied ones
// inside them), plus DNS, the gateway, and their own namespace (the
// NetworkPolicy probe's dialer must be stopped by the listener's ingress
// policy, not by its own egress).
func TestEgressAllowListKeepsDNSAndTheGateway(t *testing.T) {
	hana := newPerson(t, "hana")
	r := perUser()
	r.Egress = mustEgress(t, append(slices.Clone(metadataCIDRs), "10.1.2.3/32"), []string{"10.0.0.0/8", "192.0.2.0/24"})
	reconcilePerson(t, r, hana)

	pol := egressPolicy(t, "javo-"+hana.Name)
	checkSelectsAllButFilePods(t, pol)
	want := []networkingv1.IPBlock{
		{CIDR: "10.0.0.0/8", Except: []string{"10.1.2.3/32"}},
		{CIDR: "192.0.2.0/24"},
	}
	if got := ipBlocks(pol); !slices.EqualFunc(got, want, func(a, b networkingv1.IPBlock) bool { return a.String() == b.String() }) {
		t.Errorf("ip blocks = %v, want %v", got, want)
	}
	checkKeepsGatewayAndOwnNamespace(t, pol)
	if hasPeer(pol, networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{}}) {
		t.Errorf("egress %v allows every pod in the cluster despite the allow list", pol.Spec.Egress)
	}
}

// Port 53 is open only to the cluster's DNS: the kube-dns (CoreDNS) pods in
// kube-system, and the name servers pods are told to use (for a node-local
// DNS cache on the host network, which no pod selector matches). Port 53
// open to any host would let a pod tunnel anything over it.
func TestEgressDNSGoesOnlyToTheClusterDNS(t *testing.T) {
	for _, allow := range [][]string{nil, {"192.0.2.0/24"}} {
		kim := newPerson(t, "kim"+string(rune('a'+len(allow))))
		r := perUser()
		r.Egress = mustEgress(t, metadataCIDRs, allow)
		r.Egress.NameServers = []netip.Addr{netip.MustParseAddr("169.254.20.10"), netip.MustParseAddr("fd00::a")}
		reconcilePerson(t, r, kim)

		pol := egressPolicy(t, "javo-"+kim.Name)
		want := []string{
			(&networkingv1.NetworkPolicyPeer{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}},
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
			}).String(),
			(&networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: "169.254.20.10/32"}}).String(),
			(&networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: "fd00::a/128"}}).String(),
		}
		found := false
		for _, rule := range pol.Spec.Egress {
			if len(rule.Ports) == 0 || rule.Ports[0].Port.IntValue() != 53 {
				continue
			}
			var ports, to []string
			for _, p := range rule.Ports {
				ports = append(ports, string(*p.Protocol)+"/"+p.Port.String())
			}
			for _, p := range rule.To {
				to = append(to, p.String())
			}
			slices.Sort(ports)
			if !slices.Equal(ports, []string{"TCP/53", "UDP/53"}) || !slices.Equal(to, want) {
				t.Errorf("allow %v: rule %v, want port 53 to %v only", allow, rule, want)
			}
			found = true
		}
		if !found {
			t.Errorf("allow %v: egress %v does not allow DNS", allow, pol.Spec.Egress)
		}
	}
}

// The policy as the API server stores it equals the one built, so a
// reconcile with nothing changed writes nothing.
func TestUnchangedEgressPolicyIsNotRewritten(t *testing.T) {
	for _, allow := range [][]string{nil, {"10.0.0.0/8"}} {
		ivy := newPerson(t, "ivy"+string(rune('a'+len(allow))))
		r := perUser()
		r.Egress = mustEgress(t, metadataCIDRs, allow)
		r.Egress.AllowCluster = []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")}
		r.Egress.AllowNamespaces = []string{"monitoring"}
		r.Egress.NameServers = []netip.Addr{netip.MustParseAddr("10.43.0.10")}
		reconcilePerson(t, r, ivy)
		before := egressPolicy(t, "javo-"+ivy.Name).ResourceVersion
		reconcilePerson(t, r, ivy)
		if after := egressPolicy(t, "javo-"+ivy.Name).ResourceVersion; after != before {
			t.Errorf("allow %v: policy rewritten (%s -> %s)", allow, before, after)
		}
	}
}

// The egress policy is repaired on its own events like the other policies.
func TestEditedEgressPolicyIsRepaired(t *testing.T) {
	jack := newPerson(t, "jack")
	r := perUser()
	r.Egress = mustEgress(t, metadataCIDRs, nil)
	reconcilePerson(t, r, jack)
	ns := "javo-" + jack.Name
	pol := egressPolicy(t, ns)
	pol.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{{}}
	if err := k8s.Update(context.Background(), pol); err != nil {
		t.Fatal(err)
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "limit-egress"}}
	if _, err := r.reconcilePolicy(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := ipBlocks(egressPolicy(t, ns)); len(got) != 2 {
		t.Fatalf("after repair ip blocks = %v", got)
	}
}

func TestParseEgressRejectsWhatAPolicyCannotSay(t *testing.T) {
	for _, c := range []struct {
		name        string
		deny, allow []string
	}{
		{"not a CIDR", []string{"169.254.169.254"}, nil},
		{"deny everything", []string{"0.0.0.0/0"}, nil},
		{"allowed range inside a denied one", []string{"10.0.0.0/8"}, []string{"10.1.0.0/16"}},
		{"allowed range equal to a denied one", []string{"10.0.0.0/8"}, []string{"10.0.0.0/8"}},
		{"host bits set", nil, []string{"10.0.0.1/8"}},
	} {
		if _, err := ParseEgress(c.deny, c.allow, nil); err == nil {
			t.Errorf("%s: no error", c.name)
		}
	}
}

// The name servers come from a resolv.conf (the controller's own: kubelet
// gives every pod with the default DNS policy the same --cluster-dns).
func TestReadNameServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolv.conf")
	conf := "search javo-system.svc.cluster.local svc.cluster.local\nnameserver 10.43.0.10\n# nameserver 192.0.2.1\nnameserver fd00::a\noptions ndots:5\n"
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadNameServers(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := []netip.Addr{netip.MustParseAddr("10.43.0.10"), netip.MustParseAddr("fd00::a")}; !slices.Equal(got, want) {
		t.Errorf("name servers = %v, want %v", got, want)
	}
}

// gatewayPorts are the ports of the rules that allow peer.
func gatewayPorts(pol *networkingv1.NetworkPolicy, peer networkingv1.NetworkPolicyPeer) []string {
	var out []string
	for _, r := range pol.Spec.Egress {
		if !slices.ContainsFunc(r.To, func(p networkingv1.NetworkPolicyPeer) bool { return p.String() == peer.String() }) {
			continue
		}
		for _, p := range r.Ports {
			out = append(out, string(*p.Protocol)+"/"+p.Port.String())
		}
	}
	return out
}

// makeNode creates a node with addresses and pod ranges, deleted when the
// test ends (nodes are cluster-wide, shared with the other tests).
func makeNode(t *testing.T, name string, podCIDRs []string, addrs ...corev1.NodeAddress) {
	t.Helper()
	ctx := context.Background()
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: corev1.NodeSpec{PodCIDRs: podCIDRs}}
	if len(podCIDRs) > 0 {
		n.Spec.PodCIDR = podCIDRs[0]
	}
	if err := k8s.Create(ctx, n); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k8s.Delete(context.Background(), n) })
	n.Status.Addresses = addrs
	if err := k8s.Status().Update(ctx, n); err != nil {
		t.Fatal(err)
	}
}

func exceptOf(pol *networkingv1.NetworkPolicy, cidr string) []string {
	for _, b := range ipBlocks(pol) {
		if b.CIDR == cidr {
			return b.Except
		}
	}
	return nil
}

// The nodes' own addresses can be public (a single machine, bare metal, many
// small clouds). Pods must not reach them: the packets never leave the host,
// so no cloud firewall stands between a pod and the node's sshd, kubelet or
// NodePorts. A node's pod range outside the private ones is kept off too.
// Both hold for the public internet and for an allow list; a node added
// later is denied once the nodes are reconciled.
func TestEgressKeepsPodsOffTheNodesOwnAddresses(t *testing.T) {
	makeNode(t, "node-a", []string{"198.51.100.0/24"},
		corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "203.0.113.7"},
		corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.0.5"},
		corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "2001:db8::7"},
		corev1.NodeAddress{Type: corev1.NodeHostName, Address: "node-a"},
	)
	for _, allow := range [][]string{nil, {"203.0.113.0/24"}} {
		mia := newPerson(t, "mia"+string(rune('a'+len(allow))))
		r := perUser()
		r.Egress = mustEgress(t, metadataCIDRs, allow)
		reconcilePerson(t, r, mia)
		ns := "javo-" + mia.Name
		pol := egressPolicy(t, ns)
		if allow == nil {
			v4, v6 := exceptOf(pol, "0.0.0.0/0"), exceptOf(pol, "::/0")
			for _, want := range []string{"203.0.113.7/32", "198.51.100.0/24"} {
				if !slices.Contains(v4, want) {
					t.Errorf("IPv4 except %v lacks %s", v4, want)
				}
			}
			if slices.Contains(v4, "10.0.0.5/32") {
				t.Errorf("IPv4 except %v repeats an address the private ranges cover", v4)
			}
			if !slices.Contains(v6, "2001:db8::7/128") {
				t.Errorf("IPv6 except %v lacks the node's IPv6 address", v6)
			}
		} else if got := exceptOf(pol, "203.0.113.0/24"); !slices.Contains(got, "203.0.113.7/32") {
			t.Errorf("allow list except = %v, want the node's address", got)
		}

		makeNode(t, "node-b"+string(rune('a'+len(allow))), nil, corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "203.0.113.9"})
		if _, err := r.reconcileNodes(context.Background(), reconcile.Request{}); err != nil {
			t.Fatal(err)
		}
		pol = egressPolicy(t, ns)
		cidr := "0.0.0.0/0"
		if allow != nil {
			cidr = allow[0]
		}
		if got := exceptOf(pol, cidr); !slices.Contains(got, "203.0.113.9/32") {
			t.Errorf("allow %v: after a node joined, except = %v, want its address", allow, got)
		}
	}
}

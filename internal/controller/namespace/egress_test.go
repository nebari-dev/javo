package namespace

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"

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
		{CIDR: "0.0.0.0/0", Except: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "127.0.0.0/8", "168.63.129.16/32"}},
		{CIDR: "::/0", Except: []string{"fc00::/7", "fe80::/10"}},
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
	if !hasPeer(pol, gw) {
		t.Errorf("egress %v does not allow the gateway pods", pol.Spec.Egress)
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
			if len(rule.Ports) == 0 {
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

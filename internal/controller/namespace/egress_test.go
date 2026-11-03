package namespace

import (
	"context"
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
	e, err := ParseEgress(deny, allow)
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

// ipBlocks returns every ipBlock peer of the policy's rules.
func ipBlocks(pol *networkingv1.NetworkPolicy) []networkingv1.IPBlock {
	var out []networkingv1.IPBlock
	for _, r := range pol.Spec.Egress {
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

// By default pods may reach anything (other pods, the internet) except the
// cloud metadata addresses, which hand out the node's cloud credentials.
func TestNamespaceDeniesEgressToCloudMetadataOnly(t *testing.T) {
	gina := newPerson(t, "gina")
	r := perUser()
	r.Egress = mustEgress(t, metadataCIDRs, nil)
	reconcilePerson(t, r, gina)

	pol := egressPolicy(t, "javo-"+gina.Name)
	checkSelectsAllButFilePods(t, pol)
	want := []networkingv1.IPBlock{
		{CIDR: "0.0.0.0/0", Except: []string{"169.254.169.254/32"}},
		{CIDR: "::/0", Except: []string{"fd00:ec2::254/128"}},
	}
	if got := ipBlocks(pol); !slices.EqualFunc(got, want, func(a, b networkingv1.IPBlock) bool { return a.String() == b.String() }) {
		t.Errorf("ip blocks = %v, want %v", got, want)
	}
	// Some network plugins (Cilium) never match pods by ipBlock: pods are
	// allowed by namespace so in-cluster traffic, the gateway included,
	// keeps working there.
	if !hasPeer(pol, networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{}}) {
		t.Errorf("egress %v does not allow every pod in the cluster", pol.Spec.Egress)
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
	if hasPeer(pol, networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{}}) {
		t.Errorf("egress %v allows every pod in the cluster despite the allow list", pol.Spec.Egress)
	}
	dns := false
	for _, rule := range pol.Spec.Egress {
		var ports []string
		for _, p := range rule.Ports {
			ports = append(ports, string(*p.Protocol)+"/"+p.Port.String())
		}
		slices.Sort(ports)
		if len(rule.To) == 0 && slices.Equal(ports, []string{"TCP/53", "UDP/53"}) {
			dns = true
		}
	}
	if !dns {
		t.Errorf("egress %v does not allow DNS (port 53, UDP and TCP, anywhere)", pol.Spec.Egress)
	}
}

// The policy as the API server stores it equals the one built, so a
// reconcile with nothing changed writes nothing.
func TestUnchangedEgressPolicyIsNotRewritten(t *testing.T) {
	for _, allow := range [][]string{nil, {"10.0.0.0/8"}} {
		ivy := newPerson(t, "ivy"+string(rune('a'+len(allow))))
		r := perUser()
		r.Egress = mustEgress(t, metadataCIDRs, allow)
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
		if _, err := ParseEgress(c.deny, c.allow); err == nil {
			t.Errorf("%s: no error", c.name)
		}
	}
}

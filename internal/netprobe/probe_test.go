package netprobe

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
)

const (
	appsNS  = "javo-apps"
	probeNS = "javo-system"
	podIP   = "10.1.2.3"
)

// fakeCluster plays the kubelet: a created listener pod gets an IP and runs;
// a created dialer pod finishes at once with the outcome dial returns, or
// never finishes when dial is nil.
func fakeCluster(t *testing.T, dial func() *Outcome, created ...*[]corev1.Pod) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := networkingv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(gatewayOnly()).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if p, ok := obj.(*corev1.Pod); ok {
				for _, seen := range created {
					*seen = append(*seen, *p.DeepCopy())
				}
				switch p.Labels[roleLabel] {
				case roleListener:
					p.Status = readyListener()
				case roleDialer:
					if o := dial(); o != nil {
						msg, _ := json.Marshal(o)
						p.Status = corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{
							State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: string(msg)}},
						}}}
					}
				}
			}
			return c.Create(ctx, obj, opts...)
		},
	}).Build()
}

// gatewayOnly is the policy the namespace reconciler keeps in an apps
// namespace; the probe tests whether it is enforced.
func gatewayOnly() *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: appsNS, Name: model.PolicyAllowGateway}}
}

func readyListener() corev1.PodStatus {
	return corev1.PodStatus{Phase: corev1.PodRunning, PodIP: podIP,
		Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		ContainerStatuses: []corev1.ContainerStatus{{Name: "probe", Ready: true}}}
}

func prober(c client.Client) *Prober {
	return &Prober{Client: c, AppsNamespace: appsNS, Image: podspec.Image{Ref: "example.org/javo:1"},
		ControlURL: "http://javo-gateway.javo-system.svc/healthz", Timeout: 200 * time.Millisecond, poll: time.Millisecond}
}

func TestProbeStates(t *testing.T) {
	cases := []struct {
		name     string
		dial     func() *Outcome
		want     State
		evidence string
	}{
		{"listener reachable means NotEnforced", func() *Outcome { return &Outcome{Control: true, Target: true} }, NotEnforced, podIP},
		{"control ok and listener unreachable means Enforced", func() *Outcome { return &Outcome{Control: true} }, Enforced, podIP},
		{"control unreachable proves nothing", func() *Outcome { return &Outcome{Target: false} }, Unknown, "gateway"},
		{"dialer that never finishes is Unknown", func() *Outcome { return nil }, Unknown, "did not finish"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := prober(fakeCluster(t, tc.dial)).Probe(context.Background())
			if got.State != tc.want {
				t.Fatalf("state %q, want %q (evidence %v)", got.State, tc.want, got.Evidence)
			}
			if !strings.Contains(strings.Join(got.Evidence, "\n"), tc.evidence) {
				t.Fatalf("evidence %v does not mention %q", got.Evidence, tc.evidence)
			}
			if got.CheckedAt.IsZero() {
				t.Fatal("CheckedAt not set")
			}
		})
	}
}

func TestProbeDeletesItsPods(t *testing.T) {
	c := fakeCluster(t, func() *Outcome { return &Outcome{Control: true} })
	prober(c).Probe(context.Background())
	var pods corev1.PodList
	if err := c.List(context.Background(), &pods); err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("%d probe pods left behind", len(pods.Items))
	}
}

func TestProbePodsAreRestricted(t *testing.T) {
	var seen []corev1.Pod
	prober(fakeCluster(t, func() *Outcome { return &Outcome{Control: true} }, &seen)).Probe(context.Background())
	if len(seen) != 2 {
		t.Fatalf("%d pods created, want a listener and a dialer", len(seen))
	}
	for _, p := range seen {
		sc := p.Spec.SecurityContext
		cs := p.Spec.Containers[0].SecurityContext
		if sc == nil || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot || sc.SeccompProfile == nil ||
			cs == nil || cs.AllowPrivilegeEscalation == nil || *cs.AllowPrivilegeEscalation || cs.Capabilities == nil {
			t.Errorf("pod %s is not restricted: %+v / %+v", p.Name, sc, cs)
		}
		if p.Spec.RestartPolicy == corev1.RestartPolicyAlways {
			t.Errorf("pod %s restarts", p.Name)
		}
	}
	// The release namespace holds the gateway's Secrets, so no probe pod
	// may be created there.
	if seen[0].Namespace != appsNS || seen[1].Namespace != appsNS {
		t.Errorf("both pods belong in %s, got %s and %s", appsNS, seen[0].Namespace, seen[1].Namespace)
	}
}

func TestProbeSweepsPodsLeftByACrashedProbe(t *testing.T) {
	c := fakeCluster(t, func() *Outcome { return &Outcome{Control: true} })
	stale := &corev1.Pod{}
	stale.Name, stale.Namespace = "javo-netprobe-old", appsNS
	stale.Labels = map[string]string{roleLabel: roleListener}
	if err := c.Create(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	prober(c).Probe(context.Background())
	var pods corev1.PodList
	_ = c.List(context.Background(), &pods)
	if len(pods.Items) != 0 {
		t.Fatalf("stale probe pod survived: %d pods", len(pods.Items))
	}
}

// A listener that is gone, not Ready or restarted by the time the dialer has
// finished may simply not have been serving, so its unreachability proves
// nothing about the policy.
func TestProbeIsUnknownWhenTheListenerWasNotServing(t *testing.T) {
	cases := map[string]func(c client.Client, p *corev1.Pod){
		"gone": func(c client.Client, p *corev1.Pod) { _ = c.Delete(context.Background(), p) },
		"not ready": func(c client.Client, p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
			_ = c.Status().Update(context.Background(), p)
		},
		"restarted": func(c client.Client, p *corev1.Pod) {
			p.Status.ContainerStatuses[0].RestartCount = 1
			_ = c.Status().Update(context.Background(), p)
		},
		"failed": func(c client.Client, p *corev1.Pod) {
			p.Status.Phase = corev1.PodFailed
			_ = c.Status().Update(context.Background(), p)
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			var c client.Client
			c = fakeCluster(t, func() *Outcome {
				var pods corev1.PodList
				_ = c.List(context.Background(), &pods, client.MatchingLabels{roleLabel: roleListener})
				spoil(c, &pods.Items[0])
				return &Outcome{Control: true}
			})
			got := prober(c).Probe(context.Background())
			if got.State != Unknown {
				t.Fatalf("state %q, want Unknown (evidence %v)", got.State, got.Evidence)
			}
		})
	}
}

func TestListenerHasAReadinessProbe(t *testing.T) {
	var seen []corev1.Pod
	prober(fakeCluster(t, func() *Outcome { return &Outcome{Control: true} }, &seen)).Probe(context.Background())
	rp := seen[0].Spec.Containers[0].ReadinessProbe
	if rp == nil || rp.HTTPGet == nil || rp.HTTPGet.Port.IntValue() != listenPort {
		t.Fatalf("listener readiness probe %+v, want httpGet on %d", rp, listenPort)
	}
}

func TestProbeWaitsForTheListenerToBeReady(t *testing.T) {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = networkingv1.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gatewayOnly()).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if p, ok := obj.(*corev1.Pod); ok && p.Labels[roleLabel] == roleListener {
				p.Status = corev1.PodStatus{Phase: corev1.PodRunning, PodIP: podIP} // never Ready
			}
			return c.Create(ctx, obj, opts...)
		},
	}).Build()
	got := prober(c).Probe(context.Background())
	if got.State != Unknown || !strings.Contains(strings.Join(got.Evidence, "\n"), "listener") {
		t.Fatalf("got %q %v, want Unknown naming the listener", got.State, got.Evidence)
	}
}

// Until the policy the probe tests exists (a namespace made a moment ago,
// as the probe's own namespace is on a fresh per-user install), a pod
// reaching the listener proves nothing: the probe answers Unknown and starts
// no pods, rather than a NotEnforced that would hold for hours.
func TestProbeWithoutThePolicyIsUnknownAndStartsNothing(t *testing.T) {
	var created []corev1.Pod
	c := fakeCluster(t, func() *Outcome { return &Outcome{Control: true, Target: true} }, &created)
	if err := c.Delete(context.Background(), gatewayOnly()); err != nil {
		t.Fatal(err)
	}
	got := prober(c).Probe(context.Background())
	if got.State != Unknown || !strings.Contains(strings.Join(got.Evidence, "\n"), model.PolicyAllowGateway) {
		t.Fatalf("got %+v, want Unknown naming %s", got, model.PolicyAllowGateway)
	}
	if len(created) != 0 {
		t.Fatalf("created %d pods without the policy", len(created))
	}
}

// Both probe pods run the platform image, so a private one is pulled with
// its Secrets.
func TestProbePodsPullAPrivateImageWithItsSecrets(t *testing.T) {
	var seen []corev1.Pod
	p := prober(fakeCluster(t, func() *Outcome { return &Outcome{Control: true} }, &seen))
	p.Image.PullSecrets = []string{"ghcr"}
	p.Probe(context.Background())
	if len(seen) != 2 {
		t.Fatalf("%d pods created, want a listener and a dialer", len(seen))
	}
	for _, pod := range seen {
		if got := pod.Spec.ImagePullSecrets; len(got) != 1 || got[0].Name != "ghcr" {
			t.Errorf("pod %s: imagePullSecrets = %v, want [ghcr]", pod.Name, got)
		}
	}
}

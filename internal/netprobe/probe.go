// Package netprobe finds out whether the cluster enforces NetworkPolicy.
// Apps whose template has no login of their own are safe only when it does,
// so the controller probes at start and every few hours, stores the answer
// (Store), and the controller and gateway read it.
//
// The probe starts a listener pod and a dialer pod in the apps namespace,
// where the allow-gateway-only policy covers the listener and the dialer has
// no gateway labels. (The release namespace holds the gateway's Secrets, so
// the controller may not create pods there.) It reports Enforced only when the
// dialer reaches the gateway (the control) and does not reach the listener.
package netprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
)

// State is what the probe concluded.
type State string

const (
	// Enforced: the dialer reached the gateway but not the listener.
	Enforced State = "Enforced"
	// NotEnforced: the dialer reached the listener.
	NotEnforced State = "NotEnforced"
	// Unknown: the probe could not tell, or has not run. It counts as not
	// enforced wherever the answer gates something.
	Unknown State = "Unknown"
)

// Result is one probe's answer with the lines behind it.
type Result struct {
	State     State     `json:"state"`
	CheckedAt time.Time `json:"checkedAt"`
	Evidence  []string  `json:"evidence,omitempty"`
}

const (
	roleLabel    = model.LabelRole
	roleListener = "netprobe-listener"
	roleDialer   = "netprobe-dialer"

	listenPort = 8080
	// dialTimeout is how long the dialer waits for each of its two requests.
	dialTimeout = 3 * time.Second
)

// Prober runs the probe. Client must read the API server directly (the
// probe pods are in two namespaces and are not cached).
type Prober struct {
	Client client.Client
	// AppsNamespace holds both probe pods.
	AppsNamespace string
	// Image is the javo image both pods run.
	Image podspec.Image
	// ControlURL is a URL the dialer must be able to reach, the gateway's
	// health endpoint. It proves a failure to reach the listener is the
	// policy and not a broken dialer.
	ControlURL string
	// Timeout bounds the whole probe.
	Timeout time.Duration

	poll time.Duration // pod poll interval; zero means one second
}

// Probe runs the probe and returns its result. It never fails: whatever
// goes wrong is an Unknown result with the reason as evidence. Its pods are
// deleted before it returns.
func (p *Prober) Probe(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	res := Result{State: Unknown, CheckedAt: time.Now().UTC()}
	unknown := func(format string, a ...any) Result {
		res.Evidence = append(res.Evidence, fmt.Sprintf(format, a...))
		return res
	}

	// Until the policy exists (its namespace was made a moment ago), a
	// listener anyone can reach would wrongly read as NotEnforced.
	if err := p.waitPolicy(ctx); err != nil {
		return unknown("no %s NetworkPolicy in %s yet: %v", model.PolicyAllowGateway, p.AppsNamespace, err)
	}
	p.sweep()
	suffix := rand.String(6)
	listener := p.listenerPod(suffix)
	dialer := p.dialerPod(suffix)
	defer p.remove(listener, dialer)

	if err := p.Client.Create(ctx, listener); err != nil {
		return unknown("create listener pod: %v", err)
	}
	ip, err := p.waitIP(ctx, listener)
	if err != nil {
		return unknown("listener pod: %v", err)
	}
	dialer.Spec.Containers[0].Args = []string{"netprobe", "dial",
		"--control=" + p.ControlURL, "--target=" + fmt.Sprintf("http://%s:%d/", ip, listenPort), "--timeout=" + dialTimeout.String()}
	if err := p.Client.Create(ctx, dialer); err != nil {
		return unknown("create dialer pod: %v", err)
	}
	out, err := p.waitOutcome(ctx, dialer)
	if err != nil {
		return unknown("dialer pod did not finish: %v", err)
	}

	switch {
	case !out.Control:
		return unknown("the dialer in %s could not reach the gateway at %s, so its failure to reach the listener proves nothing", p.AppsNamespace, p.ControlURL)
	case out.Target:
		res.State = NotEnforced
		res.Evidence = []string{fmt.Sprintf("a pod without the gateway's labels reached the listener %s:%d in %s", ip, listenPort, p.AppsNamespace)}
	default:
		// Not reaching the listener proves enforcement only if the listener
		// was serving the whole time.
		if err := p.listenerServing(ctx, listener); err != nil {
			return unknown("listener pod was not serving when the dialer ran, so its unreachability proves nothing: %v", err)
		}
		res.State = Enforced
		res.Evidence = []string{fmt.Sprintf("a pod without the gateway's labels reached the gateway but not the listener %s:%d in %s", ip, listenPort, p.AppsNamespace)}
	}
	return res
}

// waitPolicy waits until the gateway-only policy is in the apps namespace.
func (p *Prober) waitPolicy(ctx context.Context) error {
	for {
		err := p.Client.Get(ctx, client.ObjectKey{Namespace: p.AppsNamespace, Name: model.PolicyAllowGateway}, &networkingv1.NetworkPolicy{})
		if err == nil || !apierrors.IsNotFound(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(p.pollEvery()):
		}
	}
}

func (p *Prober) pollEvery() time.Duration {
	if p.poll > 0 {
		return p.poll
	}
	return time.Second
}

// listenerServing re-reads the listener and returns an error unless it is
// still Running and Ready and has never restarted.
func (p *Prober) listenerServing(ctx context.Context, pod *corev1.Pod) error {
	var got corev1.Pod
	if err := p.Client.Get(ctx, client.ObjectKeyFromObject(pod), &got); err != nil {
		return err
	}
	if got.Status.Phase != corev1.PodRunning || !podReady(&got) {
		return fmt.Errorf("phase %s, ready %t", got.Status.Phase, podReady(&got))
	}
	for _, cs := range got.Status.ContainerStatuses {
		if cs.RestartCount != 0 {
			return fmt.Errorf("container restarted %d times", cs.RestartCount)
		}
	}
	return nil
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// waitIP waits until the listener runs, is Ready and has an IP.
func (p *Prober) waitIP(ctx context.Context, pod *corev1.Pod) (string, error) {
	var ip string
	err := p.until(ctx, pod, func(got *corev1.Pod) bool {
		ip = got.Status.PodIP
		return got.Status.Phase == corev1.PodRunning && ip != "" && podReady(got)
	})
	return ip, err
}

// waitOutcome waits until the dialer has terminated and reads its message.
func (p *Prober) waitOutcome(ctx context.Context, pod *corev1.Pod) (Outcome, error) {
	var out Outcome
	var parseErr error
	err := p.until(ctx, pod, func(got *corev1.Pod) bool {
		for _, cs := range got.Status.ContainerStatuses {
			if t := cs.State.Terminated; t != nil {
				parseErr = json.Unmarshal([]byte(t.Message), &out)
				return true
			}
		}
		return false
	})
	if err == nil && parseErr != nil {
		err = fmt.Errorf("termination message: %w", parseErr)
	}
	return out, err
}

func (p *Prober) until(ctx context.Context, pod *corev1.Pod, done func(*corev1.Pod) bool) error {
	t := time.NewTicker(p.pollEvery())
	defer t.Stop()
	for {
		var got corev1.Pod
		if err := p.Client.Get(ctx, client.ObjectKeyFromObject(pod), &got); err == nil && done(&got) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s", p.Timeout)
		case <-t.C:
		}
	}
}

// remove deletes pods on a fresh context so a cancelled probe still cleans up.
func (p *Prober) remove(pods ...*corev1.Pod) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, pod := range pods {
		_ = client.IgnoreNotFound(p.Client.Delete(ctx, pod))
	}
}

// sweep deletes probe pods an earlier probe left behind (the controller
// was stopped part-way).
func (p *Prober) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, ns := range []string{p.AppsNamespace} {
		var list corev1.PodList
		if err := p.Client.List(ctx, &list, client.InNamespace(ns)); err != nil {
			continue
		}
		for i := range list.Items {
			if r := list.Items[i].Labels[roleLabel]; r == roleListener || r == roleDialer {
				_ = client.IgnoreNotFound(p.Client.Delete(ctx, &list.Items[i]))
			}
		}
	}
}

func (p *Prober) pod(ns, name, role string, args []string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{roleLabel: role}},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			AutomountServiceAccountToken: ptr.To(false),
			ActiveDeadlineSeconds:        ptr.To(int64(p.Timeout.Seconds()) + 60),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   ptr.To(true),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:  "probe",
				Image: p.Image.Ref,
				Args:  args,
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false),
					ReadOnlyRootFilesystem:   ptr.To(true),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
	p.Image.Pull(&pod.Spec)
	return pod
}

func (p *Prober) listenerPod(suffix string) *corev1.Pod {
	pod := p.pod(p.AppsNamespace, "javo-netprobe-listen-"+suffix, roleListener,
		[]string{"netprobe", "listen", fmt.Sprintf("--addr=:%d", listenPort)})
	pod.Spec.Containers[0].Ports = []corev1.ContainerPort{{ContainerPort: listenPort}}
	pod.Spec.Containers[0].ReadinessProbe = &corev1.Probe{
		ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt32(listenPort)}},
		PeriodSeconds: 1,
	}
	return pod
}

// dialerPod returns the dialer without its target; Probe fills the args in
// once the listener has an address.
func (p *Prober) dialerPod(suffix string) *corev1.Pod {
	return p.pod(p.AppsNamespace, "javo-netprobe-dial-"+suffix, roleDialer, nil)
}

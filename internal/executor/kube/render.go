package kube

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/workload"
)

// Render is the Pod that runs s, kept from the node as far as iso says. For
// a run (s.Batch) it is the pod of each attempt: the caller names and labels
// it and wraps it in the Job that retries and times it out.
// Every pod gets the same hardening, whatever s asks for: no service
// account token, no root, no capabilities, no privilege escalation and the
// runtime's seccomp profile.
func Render(s workload.Spec, iso podspec.Isolation) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: s.Ref.Name, Namespace: s.Ref.Namespace,
			Labels: map[string]string{model.LabelApp: s.Ref.ID, model.LabelOwner: s.Ref.Owner},
		},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: ptr.To(false),
			EnableServiceLinks:           ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   ptr.To(true),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Affinity: affinity(s.Ref.Owner, s.CoLocate),
		},
	}
	spec := &pod.Spec
	runAs(spec, s.User)
	for _, v := range s.Volumes {
		spec.Volumes = append(spec.Volumes, volume(v))
	}
	for _, name := range s.PullSecrets {
		spec.ImagePullSecrets = append(spec.ImagePullSecrets, corev1.LocalObjectReference{Name: name})
	}
	for _, u := range s.Steps {
		spec.InitContainers = append(spec.InitContainers, step(u))
	}

	app := container(s.Main)
	app.Resources = s.Resources
	limitDisk(&app, s.DiskMax)
	if s.Port > 0 {
		app.Ports = []corev1.ContainerPort{{ContainerPort: s.Port}}
	}
	if c := s.Main.Ready; c != nil {
		port := intstr.FromInt32(s.Port)
		app.ReadinessProbe = &corev1.Probe{PeriodSeconds: int32(c.Every / time.Second)}
		if c.TCP {
			app.ReadinessProbe.TCPSocket = &corev1.TCPSocketAction{Port: port}
		} else {
			app.ReadinessProbe.HTTPGet = &corev1.HTTPGetAction{Path: c.HTTPPath, Port: port}
		}
	}
	spec.Containers = []corev1.Container{app}
	s.Size.Apply(spec, &spec.Containers[0], s.Resources)

	evictable(pod)
	switch {
	case s.Batch != nil:
		spec.RestartPolicy = corev1.RestartPolicyNever
	case s.Fingerprint != (workload.Fingerprint{}):
		// One that is never built again (a file pod) has none to record.
		Mark(pod, s.Fingerprint)
	}
	iso.Apply(spec)
	return pod
}

// container is the parts of a container every unit has.
func container(u workload.Unit) corev1.Container {
	c := corev1.Container{
		Name: u.Name, Image: u.Image, Command: u.Command, Args: u.Args, WorkingDir: u.Dir,
		SecurityContext: podspec.RestrictedContext(),
	}
	if u.PullAlways {
		c.ImagePullPolicy = corev1.PullAlways
	}
	if u.ReadOnlyRoot {
		c.SecurityContext = podspec.ReadOnlyRootContext()
	}
	for _, e := range u.Env {
		c.Env = append(c.Env, env(e))
	}
	for _, m := range u.Mounts {
		c.VolumeMounts = append(c.VolumeMounts, mount(m))
	}
	return c
}

// step is the init container of a step. One that stays is a native
// sidecar: the kubelet starts the next container once its check passes.
func step(u workload.Unit) corev1.Container {
	c := container(u)
	// Errors go to stderr; without this the kubelet only reads the (empty)
	// termination file and the reason has no text.
	c.TerminationMessagePolicy = corev1.TerminationMessageFallbackToLogsOnError
	if u.Stays {
		c.RestartPolicy = ptr.To(corev1.ContainerRestartPolicyAlways)
		if u.Ready != nil {
			c.StartupProbe = &corev1.Probe{
				ProbeHandler:     corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: u.Ready.Exec}},
				PeriodSeconds:    1,
				FailureThreshold: u.Ready.Tries,
			}
		}
	}
	return c
}

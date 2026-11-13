package kube

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/workload"
)

func someSpec() workload.Spec {
	return workload.Spec{
		Ref:  sales,
		User: workload.User{UID: 100123, GID: 100},
		Port: 8080,
		Main: workload.Unit{Name: workload.MainUnit, Image: "example.com/app:1", Ready: &workload.Check{HTTPPath: "/healthz"},
			Env: []workload.Env{{Name: "JAVO_TOKEN", From: &workload.SecretRef{Bundle: "alice-sales.app-api", Key: "token"}}}},
		Steps: []workload.Unit{
			{Name: "copy", Image: "example.com/javo:1", ReadOnlyRoot: true},
			{Name: "helper", Image: "example.com/javo:1", Stays: true, Ready: &workload.Check{Exec: []string{"true"}, Tries: 30}},
		},
		Volumes: []workload.Volume{{Name: "token", Secret: &workload.SecretFiles{Bundle: "alice-sales.app-nebi", Keys: []string{"token"}, Optional: true, WorldReadable: true}}},
	}
}

// A workload cannot ask for less: whatever it holds, its pod has no
// service account token, runs as the person's uid and never as root, and
// every container drops all capabilities and cannot gain privileges.
func TestEveryPodIsHardened(t *testing.T) {
	pod := Render(someSpec(), podspec.Isolation{})
	spec := pod.Spec
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Error("the pod mounts a service account token")
	}
	// Service links put every Service of the namespace (other people's app
	// names and addresses, in a shared namespace) into the environment.
	if spec.EnableServiceLinks == nil || *spec.EnableServiceLinks {
		t.Error("service-link variables are on")
	}
	sc := spec.SecurityContext
	// No pod sets fsGroup: on drivers with fsGroupPolicy File it would
	// re-group every file of a shared folder the pod mounts.
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot || *sc.RunAsUser != 100123 || *sc.RunAsGroup != 100 || sc.FSGroup != nil {
		t.Errorf("pod security context = %+v", sc)
	}
	if sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("seccomp = %+v", sc.SeccompProfile)
	}
	if spec.HostNetwork || spec.HostPID || spec.HostIPC {
		t.Error("the pod shares the node's namespaces")
	}
	all := append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...)
	if len(all) != 3 {
		t.Fatalf("%d containers", len(all))
	}
	for _, c := range all {
		s := c.SecurityContext
		if s == nil || s.AllowPrivilegeEscalation == nil || *s.AllowPrivilegeEscalation ||
			s.Capabilities == nil || len(s.Capabilities.Drop) != 1 || s.Capabilities.Drop[0] != "ALL" || len(s.Capabilities.Add) != 0 ||
			(s.Privileged != nil && *s.Privileged) {
			t.Errorf("%s security context = %+v", c.Name, s)
		}
	}
	if ro := spec.InitContainers[0].SecurityContext.ReadOnlyRootFilesystem; ro == nil || !*ro {
		t.Error("a step that runs only the platform's code has a writable root")
	}
}

func TestAStepThatStaysIsASidecarTheAppWaitsFor(t *testing.T) {
	helper := Render(someSpec(), podspec.Isolation{}).Spec.InitContainers[1]
	if helper.RestartPolicy == nil || *helper.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Fatalf("restart policy = %v", helper.RestartPolicy)
	}
	if p := helper.StartupProbe; p == nil || p.Exec == nil || p.FailureThreshold != 30 || p.PeriodSeconds != 1 {
		t.Fatalf("startup probe = %+v", p)
	}
}

// A value of a bundle is read from the app's Secret by reference: it is
// never in the pod.
func TestSecretValuesAreReferencesToTheAppsSecrets(t *testing.T) {
	pod := Render(someSpec(), podspec.Isolation{})
	e := pod.Spec.Containers[0].Env[0]
	if e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef.Name != "alice-sales.app-api" || e.ValueFrom.SecretKeyRef.Key != "token" {
		t.Errorf("env = %+v", e)
	}
	v := pod.Spec.Volumes[0].Secret
	if v == nil || v.SecretName != "alice-sales.app-nebi" || v.Optional == nil || !*v.Optional || *v.DefaultMode != 0o444 ||
		len(v.Items) != 1 || v.Items[0].Key != "token" || v.Items[0].Path != "token" {
		t.Errorf("volume = %+v", v)
	}
}

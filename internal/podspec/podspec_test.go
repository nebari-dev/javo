package podspec

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// An admin may run people's pods in their own user namespace (a container
// escape then lands as an unprivileged uid on the node) and under a
// sandboxed runtime class. The zero value leaves the pod as it is.
func TestIsolationRunsPodsInAUserNamespaceAndARuntimeClass(t *testing.T) {
	spec := corev1.PodSpec{}
	Isolation{}.Apply(&spec)
	if spec.HostUsers != nil || spec.RuntimeClassName != nil {
		t.Fatalf("zero isolation changed the pod: hostUsers %v, runtimeClass %v", spec.HostUsers, spec.RuntimeClassName)
	}
	Isolation{UserNamespace: true, RuntimeClassName: "gvisor"}.Apply(&spec)
	if spec.HostUsers == nil || *spec.HostUsers || spec.RuntimeClassName == nil || *spec.RuntimeClassName != "gvisor" {
		t.Fatalf("hostUsers %v, runtimeClass %v", spec.HostUsers, spec.RuntimeClassName)
	}
}

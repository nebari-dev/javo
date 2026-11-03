package gatewaytest

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/nebari-dev/javo/internal/podspec"
)

// PodWith is alice's app pod with these init containers before the app's.
func PodWith(short string, inits ...string) *corev1.Pod {
	p := LivePod("alice-aaaaaaaa", short, types.UID("uid-"+short))
	for _, n := range inits {
		p.Spec.InitContainers = append(p.Spec.InitContainers, corev1.Container{Name: n})
	}
	p.Spec.Containers = []corev1.Container{{Name: podspec.ContainerName}}
	return p
}

// EnvInits are the init containers of an app from Git with an environment,
// in the order the controller puts them.
var EnvInits = []string{"clone", "source-check", "javo-files", "pod-user", "sample-dir", "sample-copy", "env-tools", "env-install"}

// LivePod is owner's app pod, as the controller creates it.
func LivePod(owner, short string, uid types.UID) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: App(owner, short).ObjectName(), Namespace: Namespace, UID: uid}}
}

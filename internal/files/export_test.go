package files

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/tink-crypto/tink-go/v2/tink"
)

// SealLogin gives pod the sealed rclone password pass, as create does.
func SealLogin(seal tink.AEAD, pod *corev1.Pod, pass string) {
	if _, err := sealLogin(seal, pod, pass); err != nil {
		panic(err)
	}
}

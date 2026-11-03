package hubimport

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func claim(name string, annotations map[string]string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations}}
}

// The JupyterHub username of a home claim is KubeSpawner's annotation when
// the claim has it, else the claim name with KubeSpawner's escaping undone.
// Claims that are not homes have none.
func TestUsernameOfAClaim(t *testing.T) {
	for _, tc := range []struct {
		claim *corev1.PersistentVolumeClaim
		want  string
		ok    bool
	}{
		{claim("claim-alice", nil), "alice", true},
		{claim("claim-alice-40example-2ecom", nil), "alice@example.com", true},
		{claim("claim--41lice", nil), "Alice", true},
		{claim("claim-bob-2dsmith", nil), "bob-smith", true},
		{claim("claim-x", map[string]string{"hub.jupyter.org/username": "Bob.Smith@example.org"}), "Bob.Smith@example.org", true},
		{claim("hub-db-dir", nil), "", false},
		{claim("claim-", nil), "", false},
		{claim("claim-bad-zz", nil), "", false},
	} {
		got, ok := Username(tc.claim)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: %q, %v; want %q, %v", tc.claim.Name, got, ok, tc.want, tc.ok)
		}
	}
}

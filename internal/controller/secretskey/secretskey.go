// Package secretskey reads the key-encryption key of people's secrets from
// its Kubernetes Secret, and makes that Secret on the first start.
//
// The chart cannot make the key itself: a render without the cluster (helm
// template, Argo CD) cannot look up the live Secret, so each render would
// carry a new random key. The controller makes it instead, outside the
// release, and never changes or deletes it afterwards: a new key would leave
// every stored secret unreadable.
package secretskey

import (
	"context"
	"crypto/rand"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/secrets"
)

// The controller's Role in the release namespace grants these; the chart
// grants create only when it is the controller that makes the Secret.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get,resourceNames=javo-secrets-key
// +kubebuilder:rbac:groups="",resources=secrets,verbs=create

// Key is the Secret's data key holding the 32 key bytes.
const Key = "kek"

// Secret is the Secret holding the key.
type Secret struct {
	Client    client.Client
	Namespace string
	Name      string
	// Create makes the Secret, with a new random key, when it does not exist.
	// Off, a missing Secret is an error (one made by the admin, such as a
	// restored key, must never be replaced by a new one).
	Create bool
}

// Load returns the key in the Secret, making the Secret first when Create
// is set and it does not exist. An existing Secret is never changed. Its
// errors never contain key material.
func (s Secret) Load(ctx context.Context) (secrets.KEK, error) {
	sec, err := s.get(ctx)
	if apierrors.IsNotFound(err) && s.Create {
		sec, err = s.create(ctx)
	}
	if err != nil {
		return nil, err
	}
	kek, err := secrets.KEKFromBytes(sec.Data[Key])
	if err != nil {
		return nil, fmt.Errorf("the secrets key in Secret %s/%s, key %s: %w", s.Namespace, s.Name, Key, err)
	}
	return kek, nil
}

func (s Secret) get(ctx context.Context) (*corev1.Secret, error) {
	var sec corev1.Secret
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Name}, &sec); err != nil {
		return nil, fmt.Errorf("read the secrets key Secret %s/%s: %w", s.Namespace, s.Name, err)
	}
	return &sec, nil
}

// create makes the Secret with a new random key. When another replica made
// it first, its key is the one used.
func (s Secret) create(ctx context.Context) (*corev1.Secret, error) {
	b := make([]byte, secrets.KEKSize)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	immutable := true
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: s.Namespace, Name: s.Name,
			// No app.kubernetes.io/instance: Argo CD would count the Secret
			// as part of the Application and want to prune it.
			Labels: map[string]string{
				"app.kubernetes.io/name":       "javo",
				"app.kubernetes.io/component":  "secrets-key",
				"app.kubernetes.io/managed-by": "javo",
			},
			Annotations: map[string]string{
				"helm.sh/resource-policy":         "keep",
				"argocd.argoproj.io/sync-options": "Prune=false,Delete=false",
			},
		},
		Type:      corev1.SecretTypeOpaque,
		Immutable: &immutable,
		Data:      map[string][]byte{Key: b},
	}
	err := s.Client.Create(ctx, sec)
	if apierrors.IsAlreadyExists(err) {
		return s.get(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("create the secrets key Secret %s/%s: %w", s.Namespace, s.Name, err)
	}
	return sec, nil
}

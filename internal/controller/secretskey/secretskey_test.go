package secretskey

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/nebari-dev/javo/internal/secrets"
)

const ns, name = "javo-system", "javo-secrets-key"

func getSecret(t *testing.T, c client.Client) corev1.Secret {
	t.Helper()
	var s corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// opensTheSameKeyset fails unless a and b are the same key: a keyset wrapped
// by a opens only with a.
func opensTheSameKeyset(t *testing.T, a, b secrets.KEK) {
	t.Helper()
	ks, err := secrets.NewKeyset(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.NewOpener(ks, b); err != nil {
		t.Fatalf("not the same key: %v", err)
	}
}

// A first start makes the key: 32 random bytes in an immutable Secret that
// neither Helm nor Argo CD deletes; later starts get the same key.
func TestLoadCreatesTheKeyWhenMissing(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	src := Secret{Client: c, Namespace: ns, Name: name, Create: true}
	first, err := src.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := getSecret(t, c)
	if len(s.Data[Key]) != secrets.KEKSize {
		t.Fatalf("kek is %d bytes, want %d", len(s.Data[Key]), secrets.KEKSize)
	}
	if s.Immutable == nil || !*s.Immutable {
		t.Error("the Secret is not immutable")
	}
	if s.Annotations["helm.sh/resource-policy"] != "keep" || s.Annotations["argocd.argoproj.io/sync-options"] != "Prune=false,Delete=false" {
		t.Errorf("annotations %v", s.Annotations)
	}
	if s.Labels["app.kubernetes.io/name"] != "javo" || s.Labels["app.kubernetes.io/instance"] != "" {
		t.Errorf("labels %v", s.Labels)
	}
	if len(s.OwnerReferences) != 0 {
		t.Errorf("owner references %v", s.OwnerReferences)
	}
	again, err := src.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	opensTheSameKeyset(t, first, again)
	if now := getSecret(t, c); !bytes.Equal(now.Data[Key], s.Data[Key]) {
		t.Fatal("a second start changed the key")
	}
}

// keySecret is a Secret holding a fresh key, as an earlier chart or an admin
// made it, and that key.
func keySecret(t *testing.T, secretName string) (*corev1.Secret, secrets.KEK) {
	t.Helper()
	b := make([]byte, secrets.KEKSize)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	kek, err := secrets.KEKFromBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: secretName, Labels: map[string]string{"app.kubernetes.io/managed-by": "Helm"}},
		Data:       map[string][]byte{Key: b},
	}, kek
}

// An install whose chart made the Secret keeps its key: it is used, and the
// Secret is left exactly as it is.
func TestLoadUsesAnExistingKey(t *testing.T) {
	existing, want := keySecret(t, name)
	c := fake.NewClientBuilder().WithObjects(existing.DeepCopy()).Build()
	got, err := Secret{Client: c, Namespace: ns, Name: name, Create: true}.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	opensTheSameKeyset(t, want, got)
	s := getSecret(t, c)
	if !bytes.Equal(s.Data[Key], existing.Data[Key]) || len(s.Labels) != 1 || len(s.Annotations) != 0 {
		t.Fatalf("the existing Secret changed: %+v", s.ObjectMeta)
	}
}

// Two replicas starting at once both find no Secret; the one whose create
// loses uses the key the other made.
func TestLoadUsesTheKeyAnotherReplicaMadeFirst(t *testing.T) {
	other, want := keySecret(t, name)
	c := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if err := c.Create(ctx, other.DeepCopy()); err != nil {
				return err
			}
			return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, obj.GetName())
		},
	}).Build()
	got, err := Secret{Client: c, Namespace: ns, Name: name, Create: true}.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	opensTheSameKeyset(t, want, got)
}

// secrets.existingKeySecret names a Secret the admin made (a restored key):
// it is only read, and a missing one is an error, never a new key.
func TestLoadWithoutCreateOnlyReads(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	src := Secret{Client: c, Namespace: ns, Name: "my-kek"}
	if _, err := src.Load(context.Background()); err == nil {
		t.Fatal("a missing Secret gave a key")
	}
	var list corev1.SecretList
	if err := c.List(context.Background(), &list); err != nil || len(list.Items) != 0 {
		t.Fatalf("Secrets made: %v, %v", list.Items, err)
	}
	existing, want := keySecret(t, "my-kek")
	if err := c.Create(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	got, err := src.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	opensTheSameKeyset(t, want, got)
}

// A Secret whose key is not 32 bytes is refused, and never replaced.
func TestLoadRefusesABadKey(t *testing.T) {
	bad := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Data: map[string][]byte{Key: []byte("short")}}
	c := fake.NewClientBuilder().WithObjects(bad).Build()
	if _, err := (Secret{Client: c, Namespace: ns, Name: name, Create: true}).Load(context.Background()); err == nil {
		t.Fatal("a 5-byte key was accepted")
	}
	if s := getSecret(t, c); string(s.Data[Key]) != "short" {
		t.Fatal("the bad Secret was replaced")
	}
}

// Other keys (the gateway's sessions key, the secrets' fingerprint key) are
// made and kept the same way, under their own data key and component.
func TestBytesMakesAndKeepsAnyNamedKey(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	src := Secret{Client: c, Namespace: ns, Name: "javo-sessions-key", Key: "key", Component: "sessions-key", Create: true}
	first, err := src.Bytes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var s corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "javo-sessions-key"}, &s); err != nil {
		t.Fatal(err)
	}
	if len(first) != secrets.KEKSize || !bytes.Equal(s.Data["key"], first) || s.Labels["app.kubernetes.io/component"] != "sessions-key" {
		t.Fatalf("secret %v labels %v", len(s.Data["key"]), s.Labels)
	}
	if s.Immutable == nil || !*s.Immutable || s.Annotations["helm.sh/resource-policy"] != "keep" {
		t.Fatalf("immutable %v annotations %v", s.Immutable, s.Annotations)
	}
	again, err := src.Bytes(context.Background())
	if err != nil || !bytes.Equal(again, first) {
		t.Fatalf("second start: %v", err)
	}
	src.Create = false
	src.Name = "javo-missing"
	if _, err := src.Bytes(context.Background()); !apierrors.IsNotFound(err) {
		t.Fatalf("missing Secret without create: %v", err)
	}
}

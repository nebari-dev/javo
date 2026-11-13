package kube

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/workload"
)

var sales = workload.Ref{Namespace: "javo", Name: "alice-sales", ID: "app-1", Owner: "alice"}

func secretsOn(objs ...client.Object) (Secrets, client.Client) {
	c := fake.NewClientBuilder().WithObjects(objs...).Build()
	return Secrets{Reader: c, Client: c}, c
}

func stored(t *testing.T, c client.Client, name string) *corev1.Secret {
	t.Helper()
	sec := &corev1.Secret{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "javo", Name: name}, sec); err != nil {
		t.Fatal(err)
	}
	return sec
}

func TestABundleIsKeptInTheAppsOwnSecret(t *testing.T) {
	s, c := secretsOn()
	ctx := context.Background()
	want := executor.Bundle{Data: map[string][]byte{"token": []byte("v1")}, Meta: map[string]string{model.AnnotationGitToken: "3"}}
	if err := s.Put(ctx, sales, "git", want); err != nil {
		t.Fatal(err)
	}
	sec := stored(t, c, "alice-sales.app-git")
	if sec.Labels[model.LabelApp] != "app-1" || sec.Labels[model.LabelOwner] != "alice" {
		t.Errorf("labels = %v", sec.Labels)
	}
	got, err := s.Get(ctx, sales, "git")
	if err != nil || got == nil {
		t.Fatalf("Get = %v, %v", got, err)
	}
	if string(got.Data["token"]) != "v1" || got.Meta[model.AnnotationGitToken] != "3" {
		t.Errorf("read back %q, meta %v", got.Data["token"], got.Meta)
	}

	// Replaced in place: the same Secret, the new values.
	want.Data["token"], want.Meta[model.AnnotationGitToken] = []byte("v2"), "4"
	if err := s.Put(ctx, sales, "git", want); err != nil {
		t.Fatal(err)
	}
	if again := stored(t, c, "alice-sales.app-git"); again.UID != sec.UID || string(again.Data["token"]) != "v2" || again.Annotations[model.AnnotationGitToken] != "4" {
		t.Errorf("after the second Put: %q, %v", again.Data["token"], again.Annotations)
	}
}

func TestAnAppWithoutABundleHasNone(t *testing.T) {
	s, _ := secretsOn()
	if got, err := s.Get(context.Background(), sales, "api"); got != nil || err != nil {
		t.Fatalf("Get = %v, %v", got, err)
	}
	if err := s.Delete(context.Background(), sales, "api"); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteRemovesTheBundle(t *testing.T) {
	s, _ := secretsOn()
	ctx := context.Background()
	if err := s.Put(ctx, sales, "api", executor.Bundle{Data: map[string][]byte{"token": []byte("v")}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, sales, "api"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, sales, "api"); got != nil || err != nil {
		t.Fatalf("after Delete: %v, %v", got, err)
	}
}

// A Secret at the app's name that carries another app's id, or none, is
// never read, replaced or deleted for the app.
func TestASecretOfAnotherAppIsLeftAlone(t *testing.T) {
	theirs := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "javo", Name: "alice-sales.app-api", Labels: map[string]string{model.LabelApp: "app-2"}},
		Data:       map[string][]byte{"token": []byte("theirs")},
	}
	s, c := secretsOn(theirs)
	ctx := context.Background()
	if got, err := s.Get(ctx, sales, "api"); got != nil || !errors.Is(err, executor.ErrForeign) {
		t.Errorf("Get = %v, %v", got, err)
	}
	if err := s.Put(ctx, sales, "api", executor.Bundle{Data: map[string][]byte{"token": []byte("mine")}}); !errors.Is(err, executor.ErrForeign) {
		t.Errorf("Put = %v", err)
	}
	if err := s.Delete(ctx, sales, "api"); err != nil {
		t.Errorf("Delete = %v", err)
	}
	if got := stored(t, c, "alice-sales.app-api"); string(got.Data["token"]) != "theirs" {
		t.Errorf("their Secret now holds %q", got.Data["token"])
	}
}

// The values of people's secrets can be made again from the database, so
// their Secret goes with the app's Service, also when the sweep removes an
// orphaned one. The others hold the only copy of a token and stay.
func TestOnlyTheSecretsBundleGoesWithTheService(t *testing.T) {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "javo", Name: "alice-sales", UID: "svc-1"}}
	s, c := secretsOn(svc)
	ctx := context.Background()
	for _, use := range []string{"secrets", "api"} {
		if err := s.Put(ctx, sales, use, executor.Bundle{Data: map[string][]byte{"K": []byte("v")}}); err != nil {
			t.Fatal(err)
		}
	}
	if refs := stored(t, c, "alice-sales.app-secrets").OwnerReferences; len(refs) != 1 || refs[0].Kind != "Service" || refs[0].UID != "svc-1" {
		t.Errorf("secrets owners = %v", refs)
	}
	if refs := stored(t, c, "alice-sales.app-api").OwnerReferences; len(refs) != 0 {
		t.Errorf("api owners = %v", refs)
	}
}
